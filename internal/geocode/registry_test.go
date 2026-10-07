package geocode

import (
	"sort"
	"testing"
)

// TestAKeyIsTheOnlySwitch.
//
// There is no --geoapify-enabled and there must not be one: a provider is in
// the queue because a key for it was configured, and out of it because one was
// not. Two settings for one fact have a state where they disagree, and neither
// side of that disagreement is visible in any log.
func TestAKeyIsTheOnlySwitch(t *testing.T) {
	if providers := Keyed(nil); len(providers) != 0 {
		t.Errorf("no keys built %d providers", len(providers))
	}
	if providers := Keyed(map[string]string{GeoapifyName: ""}); len(providers) != 0 {
		t.Errorf("an empty key built %d providers", len(providers))
	}

	providers := Keyed(map[string]string{GeoapifyName: "a-key"})
	if len(providers) != 1 || providers[0].Name() != GeoapifyName {
		t.Fatalf("a key built %d providers, want one geoapify", len(providers))
	}
}

// TestEveryKeyedProviderActuallyBuilds.
//
// The registry is a table of function pointers, so an entry can be wrong in
// two silent ways: a constructor that answers nil whatever it is given, and
// one whose provider reports a different name from the key that switched it
// on — which would configure its pace under a name nothing reads.
func TestEveryKeyedProviderActuallyBuilds(t *testing.T) {
	for _, name := range KeyedNames() {
		providers := Keyed(map[string]string{name: "a-key"})
		if len(providers) != 1 {
			t.Errorf("%s: a key built %d providers, want 1", name, len(providers))
			continue
		}
		if got := providers[0].Name(); got != name {
			t.Errorf("%s: built a provider calling itself %q", name, got)
		}
		if published := providers[0].Defaults(); published != Defaults(name) {
			t.Errorf("%s: Defaults() is %+v, the registry says %+v — a pace configured "+
				"from the registry would not be the one the provider uses",
				name, published, Defaults(name))
		}
	}
}

// TestAKeyForAProviderThisBuildHasNeverHeardOf.
//
// A configuration file outlives a release. One carrying a key for a provider
// that was removed, or that the next version adds, must not stop a register
// starting over it.
func TestAKeyForAProviderThisBuildHasNeverHeardOf(t *testing.T) {
	if providers := Keyed(map[string]string{"cartographers-r-us": "a-key"}); len(providers) != 0 {
		t.Errorf("an unknown provider built %d providers", len(providers))
	}
}

// TestNominatimIsTheKeylessOne.
//
// It is known, it is metered, and it has nothing to switch it on, because
// OpenStreetMap issues no key.
func TestNominatimIsTheKeylessOne(t *testing.T) {
	known := Names()
	sort.Strings(known)
	if !contains(known, NominatimName) {
		t.Fatal("nominatim is not in the registry at all")
	}
	if contains(KeyedNames(), NominatimName) {
		t.Error("nominatim is listed as needing a key")
	}
	if Defaults(NominatimName) != nominatimPolicy {
		t.Error("the registry disagrees with nominatim's own published policy")
	}
}

// TestAnUnknownNameHasNoDefaults, rather than a panic or a zero that reads as
// a chosen limit somewhere else.
func TestAnUnknownNameHasNoDefaults(t *testing.T) {
	if got := Defaults("cartographers-r-us"); got != (Limits{}) {
		t.Errorf("Defaults for an unknown provider = %+v, want the zero value", got)
	}
}

func contains(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
