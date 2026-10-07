package backend

import (
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// TestRoutesRegister builds the whole API surface.
//
// It exists because huma.Register panics on a malformed operation rather than
// returning an error — a pointer query parameter, a duplicated path, an input
// type it cannot reflect — and none of that is visible to the compiler. The
// first sign is the backend dying at startup, which every test can pass
// through without noticing: nothing else here registers a route.
//
// This caught exactly that: `*float64` query parameters compiled cleanly and
// panicked with "pointers are not supported for form/header/path/query
// parameters" the moment the service started.
func TestRoutesRegister(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("registering the API panicked: %v", recovered)
		}
	}()

	api := humago.New(http.NewServeMux(), huma.DefaultConfig("Doléances", "test"))
	registerEverything(&API{}, api)
}

// registerEverything puts the whole API surface on one adapter.
//
// Shared with the cache tests, which walk the registered operations to check
// that every write says what it makes wrong. Two lists of register calls would
// mean a route added to one and missing from the other, which is exactly the
// hole both tests exist to close.
func registerEverything(a *API, api huma.API) {
	a.registerMessageRoutes(api)
	a.registerCurationRoutes(api)
	a.registerSubjectQuestionRoutes(api)
	a.registerVocabularyRoutes(api)
	a.registerGroupRoutes(api)
	a.registerMembershipRoutes(api)
	a.registerActionRoutes(api)
	a.registerAuthRoutes(api)
	a.registerAccountRoutes(api)
	a.registerBookmarkRoutes(api)
	a.registerLinkRoutes(api)
	a.registerNotificationRoutes(api)
	a.registerThemeRoutes(api)
	a.registerThemeAssetRoutes(api)
	a.registerLLMRoutes(api)
	a.registerSpamRoutes(api)
	a.registerDroppedRoutes(api)
	a.registerHistoricalRoutes(api)
	a.registerSnapshotRoutes(api)
}

// TestParseBounds covers the viewport parameter, which arrives from a browser
// and so is arbitrary text until proven otherwise.
func TestParseBounds(t *testing.T) {
	box, err := parseBounds("43.55,43.30,-1.40,-1.80")
	if err != nil {
		t.Fatalf("parseBounds: %v", err)
	}
	if box.North != 43.55 || box.South != 43.30 || box.East != -1.40 || box.West != -1.80 {
		t.Errorf("parsed %+v", box)
	}

	for _, bad := range []string{
		"",                        // empty
		"43.5,43.3,-1.4",          // three edges describe no box
		"43.5,43.3,-1.4,-1.8,0",   // five
		"north,south,east,west",   // words
		"43.30,43.55,-1.40,-1.80", // north below south
		"95,-95,10,-10",           // off the planet
	} {
		if _, err := parseBounds(bad); err == nil {
			t.Errorf("parseBounds(%q) was accepted", bad)
		}
	}
}
