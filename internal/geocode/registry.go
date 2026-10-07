package geocode

import "sort"

// The names providers are known by — in configuration, in logs, and as the
// middle word of DOLEANCES_<NAME>_APIKEY.
const (
	NominatimName = "nominatim"
	GeoapifyName  = "geoapify"
)

// registration is what this package tells the rest of the program about one
// provider: what it is called, what its own plan allows, and how to build it.
type registration struct {
	name     string
	defaults Limits

	// build is nil for a provider that needs no credential. For every other,
	// it is called with the configured key and returns nil when there is none.
	build func(endpoint, apiKey string) Provider
}

// registry is every provider this build knows.
//
// # A key is the switch, and there is no second one
//
// A provider that needs a credential is used when its key is configured and
// is not used when it is not: DOLEANCES_GEOAPIFY_APIKEY turns Geoapify on by
// existing. There is deliberately no `--geoapify-enabled` beside it, because
// two settings for one fact have a state where they disagree — a provider
// switched on with no credential, which fails on the first lookup, or a key
// somebody is paying for and a queue that never calls it. Neither is visible
// in any log until somebody goes looking.
//
// Absence is therefore not an error. The queue simply has one fewer provider,
// which is exactly what a deployment that configured no key asked for.
//
// # Nominatim is the one with no key, and that is the service rather than an
// exception
//
// OpenStreetMap's instance issues none, so there is nothing to switch it on
// with; it answers whenever geocoding is enabled at all. Its endpoint is what
// a deployment points at its own instance (--geocode-endpoint), and its pace
// is configured like everybody else's.
//
// # Adding a provider is one entry
//
// Append here and it picks up DOLEANCES_<NAME>_APIKEY, _PER_SECOND and
// _PER_DAY with no change to the configuration package and none to the
// backend's wiring.
var registry = []registration{
	{name: NominatimName, defaults: nominatimPolicy},
	{name: GeoapifyName, defaults: geoapifyFreeTier, build: NewGeoapify},
}

// Names lists every provider this build knows, keyed or not. Configuration
// reads it to declare a pace for each.
func Names() []string {
	names := make([]string, 0, len(registry))
	for _, entry := range registry {
		names = append(names, entry.name)
	}
	sort.Strings(names)
	return names
}

// KeyedNames lists the providers that need an API key — which is to say the
// ones a deployment switches on by supplying one.
func KeyedNames() []string {
	names := make([]string, 0, len(registry))
	for _, entry := range registry {
		if entry.build != nil {
			names = append(names, entry.name)
		}
	}
	sort.Strings(names)
	return names
}

// Defaults are a provider's own published limits, readable without building
// one — so a flag's help can say what a zero means in that provider's own
// numbers rather than in a sentence somebody has to keep up to date.
func Defaults(name string) Limits {
	for _, entry := range registry {
		if entry.name == name {
			return entry.defaults
		}
	}
	return Limits{}
}

// Keyed builds every keyed provider whose key is configured, in a stable
// order.
//
// keys is by provider name. A name this build does not know is ignored rather
// than refused: a configuration file carrying a key for a provider that has
// been removed, or that a future version adds, should not stop a register
// starting.
//
// The blank-key check here is deliberately the second one — every constructor
// answers nil for an empty key as well, which is what makes a provider
// built directly in a test or a tool behave like one built from
// configuration. Either alone is correct; keep both, because the one that is
// skipped is always the path somebody added later.
func Keyed(keys map[string]string) []Provider {
	providers := make([]Provider, 0, len(keys))
	for _, name := range KeyedNames() {
		apiKey := keys[name]
		if apiKey == "" {
			continue
		}
		for _, entry := range registry {
			if entry.name != name {
				continue
			}
			// The endpoint is the provider's own: these are commercial
			// services at one published address, unlike Nominatim, which a
			// deployment is expected to self-host.
			if provider := entry.build("", apiKey); provider != nil {
				providers = append(providers, provider)
			}
		}
	}
	return providers
}
