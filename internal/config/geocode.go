package config

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/CoderSyndicate/doleances/internal/geocode"
)

// Geocode configuration keys.
const (
	KeyGeocodeEnabled  = "geocode-enabled"
	KeyGeocodeEndpoint = "geocode-endpoint"
	KeyGeocodeContact  = "geocode-contact"
)

// KeyAPIKey names the setting that enables one geocoding provider.
//
// The per-provider settings are named from the provider itself rather than
// written out one by one: <name>-apikey, <name>-per-second and <name>-per-day,
// which the ordinary bootstrap reads from the environment as
// DOLEANCES_<NAME>_APIKEY and so on — the prefix is the binary name and the
// replacer turns each dash into an underscore.
//
// **The key is what enables a provider.** Supplying DOLEANCES_GEOAPIFY_APIKEY
// puts Geoapify in the queue; leaving it out leaves it out, and that is not an
// error. There is no second setting to agree with it — see geocode's registry
// for why, and for what adding a provider costs.
func KeyAPIKey(provider string) string { return provider + "-apikey" }

// KeyPerSecond and KeyPerDay name one provider's pace.
//
// Both are configuration because both are **bought**. Geoapify's free tier is
// 5 a second and 3,000 a day; every paid plan moves both, and a deployment
// that upgraded but could not say so would be paying for capacity this queue
// refuses to use. Nominatim's are the usage policy's when it is the public
// instance, and whatever the operator decides when they run their own — which
// is the usual reason to run one.
func KeyPerSecond(provider string) string { return provider + "-per-second" }

// KeyPerDay is the daily half of KeyPerSecond.
func KeyPerDay(provider string) string { return provider + "-per-day" }

// Geocode configures reverse geocoding — turning a pinned point into the name
// of a place.
type Geocode struct {
	// Enabled is on by default: without it a location is a dot with no name,
	// which is markedly worse for a register organised by place.
	Enabled bool

	// Endpoint is a Nominatim instance. The public one at
	// nominatim.openstreetmap.org has a usage policy that forbids heavy use,
	// so any deployment expecting traffic points this at its own.
	Endpoint string

	// Contact is an email address or URL put in the User-Agent, which
	// Nominatim's policy requires so an operator can be reached before being
	// blocked. It is the operator's address, never a contributor's.
	Contact string

	// Keys are the API keys of the providers that need one, by provider name,
	// and they are what decides which providers the queue has.
	//
	// A key that is absent means that provider is not used. That is not an
	// error: the queue simply has one fewer, and Nominatim answers everything
	// at its one request a second. Supplying one adds that provider's rate
	// **beside** Nominatim's rather than instead of it — see geocode.Service,
	// where the queue spans every provider and the rates add.
	//
	// They are credentials, so they come from the environment in any real
	// deployment: a key on a command line is visible in `ps` and in a
	// container spec.
	Keys map[string]string

	// Limits override each provider's defaults, keyed by provider name. A
	// zero in either field means "whatever that provider publishes", so a
	// deployment names only what its plan changed.
	Limits map[string]geocode.Limits
}

// RegisterGeocodeFlags declares the geocoding flags. Only the backend performs
// lookups — deliberately, so a contributor's browser never contacts a third
// party while they are writing.
func RegisterGeocodeFlags(cmd *cobra.Command) {
	f := cmd.PersistentFlags()
	f.Bool(KeyGeocodeEnabled, true, "name pinned locations by reverse geocoding")
	f.String(KeyGeocodeEndpoint, "https://nominatim.openstreetmap.org",
		"Nominatim instance; point this at your own for anything but a trial")
	f.String(KeyGeocodeContact, "",
		"operator contact put in the User-Agent, as Nominatim's usage policy requires")
	// Declared from geocode's own registry, so a provider added there arrives
	// here with its three settings and no edit to this file.
	for _, provider := range geocode.KeyedNames() {
		f.String(KeyAPIKey(provider), "", fmt.Sprintf(
			"%s API key; that provider is used only when this is set (DOLEANCES_%s_APIKEY)",
			provider, strings.ToUpper(strings.ReplaceAll(provider, "-", "_"))))
	}
	for _, provider := range geocode.Names() {
		published := geocode.Defaults(provider)
		f.Float64(KeyPerSecond(provider), 0, fmt.Sprintf(
			"%s requests a second; 0 keeps its published %g", provider, published.PerSecond))
		f.Int(KeyPerDay(provider), 0, fmt.Sprintf(
			"%s requests a day; 0 keeps its published %d", provider, published.PerDay))
	}
}

// LoadGeocode reads the resolved geocoding configuration.
func LoadGeocode() Geocode {
	return Geocode{
		Enabled:  viper.GetBool(KeyGeocodeEnabled),
		Endpoint: viper.GetString(KeyGeocodeEndpoint),
		Contact:  viper.GetString(KeyGeocodeContact),

		Keys:   loadProviderKeys(),
		Limits: loadProviderLimits(),
	}
}

// loadProviderKeys reads the keys that were configured, and only those. A
// blank one is left out rather than stored, so "has a key" and "is used" are
// one fact for every reader of this struct.
func loadProviderKeys() map[string]string {
	keys := map[string]string{}
	for _, provider := range geocode.KeyedNames() {
		if apiKey := strings.TrimSpace(viper.GetString(KeyAPIKey(provider))); apiKey != "" {
			keys[provider] = apiKey
		}
	}
	return keys
}

// loadProviderLimits reads a pace for every provider, keyed or not. A zero in
// either field means that provider's own published limit, so a deployment
// names only what its plan changed.
func loadProviderLimits() map[string]geocode.Limits {
	limits := map[string]geocode.Limits{}
	for _, provider := range geocode.Names() {
		limits[provider] = geocode.Limits{
			PerSecond: viper.GetFloat64(KeyPerSecond(provider)),
			PerDay:    viper.GetInt(KeyPerDay(provider)),
		}
	}
	return limits
}

// UserAgent renders the identification Nominatim requires. A request without
// one that names the application is refused, and an operator who set no
// contact still gets a truthful agent rather than a forged one.
func (g Geocode) UserAgent(version string) string {
	agent := "doleances/" + version
	if g.Contact != "" {
		agent += " (" + g.Contact + ")"
	}
	return agent
}

// KeyRegisterLanguages names the languages the register speaks.
const KeyRegisterLanguages = "register-languages"

// MaxLabelLanguages is how many languages one Wikidata label request may ask
// for. It is their limit, not ours: wbgetentities refuses 51 with
// "toomanyvalues" (the documented highlimit of 500 applies to bot accounts,
// which we are not). A longer list is not refused here — it is simply fetched
// in more than one request.
const MaxLabelLanguages = 50

// defaultRegisterLanguages is what a European register plausibly receives.
//
// Exactly fifty, because that is one request (see MaxLabelLanguages) and
// because the marginal language costs nothing else: labels come back in one
// call whatever the length of the list, and an alias only ever does anything
// if somebody actually writes in that language.
//
// The selection is deliberate rather than "the fifty biggest":
//
//   - The 24 official languages of the EU, in full. A register that organises
//     grievances by place inside Europe and cannot read one of its member
//     states' languages is making a statement it does not mean to make —
//     Maltese and Irish are small and they are official.
//   - Other European national languages, including the neighbours and the
//     countries people emigrate from and to.
//   - Breton, Basque, Catalan, Occitan and Welsh. These are the languages of
//     exactly the rural, peripheral places this register exists to hear from,
//     and the project's own i18n rule already names a doléance written in
//     Occitan as the case to handle. Leaving them out to fit a rounder number
//     would be the wrong fifty.
//   - The major global languages, for the diasporas that live in Europe and
//     write in the language they think in.
//
// What is left out is mostly a matter of arithmetic rather than judgement, and
// an operator elsewhere is expected to change it: --register-languages is a
// flag precisely because this list encodes where the deployment is.
var defaultRegisterLanguages = []string{
	// The 24 official languages of the European Union.
	"bg", "cs", "da", "de", "el", "en", "es", "et", "fi", "fr",
	"ga", "hr", "hu", "it", "lt", "lv", "mt", "nl", "pl", "pt",
	"ro", "sk", "sl", "sv",

	// Other European national languages.
	"sq", "be", "is", "mk", "nb", "ru", "sr", "tr", "uk",

	// Regional and minority languages of western Europe — the peripheries
	// this register is most likely to hear from.
	"br", "ca", "cy", "eu", "oc",

	// Major global languages.
	"ar", "bn", "fa", "he", "hi", "id", "ja", "ko", "sw", "ur",
	"vi", "zh",
}

// RegisterLanguageFlags declares the language list.
//
// It is a list rather than a constant because a deployment elsewhere speaks
// other languages, and because adding one has a real effect: every subject
// with a Wikidata identity learns its name in the new language, so doléances
// written in it are recognised from the first one rather than after a curator
// has answered a question about each subject.
func RegisterLanguageFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().StringSlice(KeyRegisterLanguages, defaultRegisterLanguages,
		"languages the register organises subjects in")
}

// LoadRegisterLanguages reads the resolved language list, always with at least
// one entry: a register that speaks no language would silently stop learning
// spellings, which looks exactly like a working one until the duplicates
// appear.
//
// Codes that are not two letters are dropped rather than sent. A typo reaching
// Wikidata is answered with an error for the whole request, so one bad entry
// would cost every language in the list — and the failure would look like a
// network problem rather than a configuration one.
func LoadRegisterLanguages() []string {
	languages := viper.GetStringSlice(KeyRegisterLanguages)

	kept := make([]string, 0, len(languages))
	seen := make(map[string]bool, len(languages))
	for _, language := range languages {
		language = strings.ToLower(strings.TrimSpace(language))
		if len(language) != 2 || seen[language] {
			continue
		}
		seen[language] = true
		kept = append(kept, language)
	}
	if len(kept) == 0 {
		return defaultRegisterLanguages
	}
	return kept
}

// KeySiteURL is where the public site answers, and KeySiteName is what it
// calls itself.
const (
	KeySiteURL  = "site-url"
	KeySiteName = "site-name"
)

// RegisterSiteFlags declares where the frontend lives and what it is called.
//
// It began as the address to put in an email. It now carries considerably more
// weight: **a passkey is bound to this host for ever**, because the relying
// party id is derived from it and the private halves of those credentials live
// on people's devices where nothing can re-key them. See config.Auth.
//
// The name is what a password manager shows beside the entry when somebody
// picks a passkey, so it is the one string in this configuration that a person
// reads in software this project does not control.
func RegisterSiteFlags(cmd *cobra.Command) {
	f := cmd.PersistentFlags()
	f.String(KeySiteURL, defaultSiteURL,
		"public URL readers reach this register on — passkeys are bound to its host for ever")
	f.String(KeySiteName, defaultSiteName,
		"what this register calls itself, as a password manager shows it")
}

// The defaults are a local development frontend, so a `go run ./test` instance
// has working accounts without being told anything.
const (
	defaultSiteURL  = "http://localhost:8201"
	defaultSiteName = "doléances"
)

// LoadSiteURL reads it, without a trailing slash.
func LoadSiteURL() string {
	return strings.TrimRight(strings.TrimSpace(viper.GetString(KeySiteURL)), "/")
}

// LoadSiteName reads what the register calls itself.
func LoadSiteName() string {
	name := strings.TrimSpace(viper.GetString(KeySiteName))
	if name == "" {
		return defaultSiteName
	}
	return name
}
