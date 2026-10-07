package config

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/CoderSyndicate/doleances/internal/geocode"
)

// newGeocodeCommand wires a command the way the backend's root command is.
func newGeocodeCommand(t *testing.T, args ...string) {
	t.Helper()
	Reset()
	t.Cleanup(Reset)

	cmd := &cobra.Command{Use: "test", RunE: func(*cobra.Command, []string) error { return nil }}
	RegisterCommonFlags(cmd, 8080, 8081)
	RegisterGeocodeFlags(cmd)
	cmd.SetArgs(args)
	cmd.SetOut(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if err := Bootstrap(cmd); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
}

// TestNoKeyMeansNoProvider.
//
// The absence of a key is a deployment saying it does not want that provider,
// not a misconfiguration. It has to be silent and it has to be complete:
// nothing in the queue, nothing calling a commercial endpoint with an empty
// credential.
func TestNoKeyMeansNoProvider(t *testing.T) {
	newGeocodeCommand(t)

	cfg := LoadGeocode()
	if len(cfg.Keys) != 0 {
		t.Errorf("Keys = %v with nothing configured, want none", cfg.Keys)
	}
	if providers := geocode.Keyed(cfg.Keys); len(providers) != 0 {
		t.Errorf("built %d providers with no key", len(providers))
	}
}

// TestTheAPIKeyEnvVarEnablesTheProvider.
//
// This is the whole contract: DOLEANCES_GEOAPIFY_APIKEY is what puts Geoapify
// in the queue. It runs through the real bootstrap rather than calling viper
// directly, because the thing most likely to break it is the env key
// replacer — "geoapify-apikey" has to reach DOLEANCES_GEOAPIFY_APIKEY, and
// without the replacer the lookup silently never matches.
func TestTheAPIKeyEnvVarEnablesTheProvider(t *testing.T) {
	t.Setenv("DOLEANCES_GEOAPIFY_APIKEY", "a-key")
	newGeocodeCommand(t)

	cfg := LoadGeocode()
	if cfg.Keys[geocode.GeoapifyName] != "a-key" {
		t.Fatalf("Keys = %v, want geoapify from the environment", cfg.Keys)
	}

	providers := geocode.Keyed(cfg.Keys)
	if len(providers) != 1 {
		t.Fatalf("built %d providers, want 1", len(providers))
	}
	if providers[0].Name() != geocode.GeoapifyName {
		t.Errorf("built %q, want geoapify", providers[0].Name())
	}
}

// TestABlankKeyIsNoKey.
//
// An env var set to nothing is how a container spec says "unset" by accident,
// and a provider built from it would answer 401 for every lookup rather than
// being absent.
func TestABlankKeyIsNoKey(t *testing.T) {
	t.Setenv("DOLEANCES_GEOAPIFY_APIKEY", "   ")
	newGeocodeCommand(t)

	cfg := LoadGeocode()
	if len(cfg.Keys) != 0 {
		t.Errorf("Keys = %v for a blank key, want none", cfg.Keys)
	}
}

// TestEveryProviderHasItsThreeSettings.
//
// The flags are declared by walking geocode's registry, which is what makes
// adding a provider one entry there. The failure this guards is the registry
// growing and the configuration not: a provider nobody can switch on, with
// nothing to say so.
func TestEveryProviderHasItsThreeSettings(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	RegisterGeocodeFlags(cmd)
	flags := cmd.PersistentFlags()

	for _, provider := range geocode.KeyedNames() {
		if flags.Lookup(KeyAPIKey(provider)) == nil {
			t.Errorf("%s has no --%s, so it can never be switched on", provider, KeyAPIKey(provider))
		}
	}
	for _, provider := range geocode.Names() {
		for _, name := range []string{KeyPerSecond(provider), KeyPerDay(provider)} {
			if flags.Lookup(name) == nil {
				t.Errorf("%s has no --%s, so its plan cannot be configured", provider, name)
			}
		}
	}
}

// TestNominatimTakesNoKey.
//
// It is the one provider with nothing to switch it on, because OpenStreetMap
// issues no key — so it answers whenever geocoding is enabled at all. An
// --nominatim-apikey would be a setting that does nothing, which is worse than
// none: somebody would set it and conclude the provider was on.
func TestNominatimTakesNoKey(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	RegisterGeocodeFlags(cmd)

	if cmd.PersistentFlags().Lookup(KeyAPIKey(geocode.NominatimName)) != nil {
		t.Error("nominatim has an api-key flag, which nothing reads")
	}
	for _, provider := range geocode.KeyedNames() {
		if provider == geocode.NominatimName {
			t.Error("nominatim is registered as needing a key; it issues none")
		}
	}
}

// TestZeroMeansTheProvidersOwnLimit.
//
// A deployment that configured nothing must not be paced at zero requests a
// second, which is the shape of mistake the project has already made once with
// a threshold stored as zero.
func TestZeroMeansTheProvidersOwnLimit(t *testing.T) {
	newGeocodeCommand(t)

	for provider, limits := range LoadGeocode().Limits {
		if limits.PerSecond != 0 || limits.PerDay != 0 {
			t.Errorf("%s = %+v with nothing configured, want zeroes meaning its own defaults",
				provider, limits)
		}
		if published := geocode.Defaults(provider); published.PerSecond <= 0 {
			t.Errorf("%s publishes no per-second default, so a zero would pace it at nothing",
				provider)
		}
	}
}
