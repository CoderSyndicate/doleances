package frontend

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// TestTheMapLibrariesShipInTheBinary.
//
// They are `//go:embed`-ed like every other asset, which is what lets the
// container run with a read-only root filesystem and nothing mounted. A
// missing file here is a map that silently does not load, and only in the
// built image — the pattern is a directory glob, so a file in the wrong place
// is simply not included and nothing says so.
func TestTheMapLibrariesShipInTheBinary(t *testing.T) {
	for _, want := range []string{
		"static/third-party/leaflet/leaflet.js",
		"static/third-party/leaflet/leaflet.css",
		"static/third-party/leaflet/leaflet.markercluster.js",
		"static/third-party/leaflet/MarkerCluster.css",
		"static/third-party/leaflet/MarkerCluster.Default.css",
		// Leaflet finds these from its own script URL, so they have to sit in
		// `images/` beside it rather than anywhere else that would work.
		"static/third-party/leaflet/images/marker-icon.png",
		"static/third-party/leaflet/images/marker-icon-2x.png",
		"static/third-party/leaflet/images/marker-shadow.png",
		"static/third-party/leaflet/images/layers.png",
		"static/third-party/leaflet/images/layers-2x.png",
	} {
		if _, err := fs.Stat(assets, want); err != nil {
			t.Errorf("%s is not embedded: %v", want, err)
		}
	}
}

// TestNoPageLoadsForeignScript.
//
// The register is not allowed to hand a third party the address of everybody
// reading it — the same trade reverse geocoding refuses by running on the
// backend — and since accounts became passkeys, foreign script would run on
// the origin where a session cookie lives.
//
// The Content-Security-Policy enforces this in the browser. The test exists
// because a policy is only as good as nobody having quietly added an
// exception to it, and because a blocked resource fails silently by design:
// the map would simply stop working, in production, with nothing in a log.
func TestNoPageLoadsForeignScript(t *testing.T) {
	templates, err := fs.Glob(assets, "templates/*/*.html")
	if err != nil {
		t.Fatalf("glob templates: %v", err)
	}
	if len(templates) == 0 {
		t.Fatal("no templates were scanned, so this test proves nothing")
	}

	// Anything absolute in a src= or href= that is not this origin's own.
	foreign := regexp.MustCompile(`(?:src|href)="(https?:)?//[^"]+"`)

	for _, name := range templates {
		raw, err := fs.ReadFile(assets, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, match := range foreign.FindAllString(string(raw), -1) {
			// Two exceptions, both links a reader follows rather than
			// resources the page loads — the distinction this test exists to
			// police is the second kind, which runs with this origin's
			// authority and sees the address of everybody reading.
			//
			// The OSM attribution, which the tile licence requires.
			if strings.Contains(match, "openstreetmap.org/copyright") {
				continue
			}
			// And the source repository in the footer. The register's claim
			// is that it can be checked, copied and rehosted by anybody; a
			// claim like that is decoration unless the page says where.
			if strings.Contains(match, "github.com/CoderSyndicate/doleances") {
				continue
			}
			t.Errorf("%s loads %s", name, match)
		}
	}
}

// TestEveryInlineScriptCarriesTheNonce.
//
// Under the policy an inline `<script>` without the nonce is refused by the
// browser, silently and by design. So the failure mode of forgetting one is a
// page whose behaviour quietly stops — the theme flashing the wrong way, a
// console table never filling — with nothing in any log and nothing to see
// except in a browser console somebody has to think to open.
//
// The templates are scanned rather than the rendered pages, because a page
// that is never rendered by a test would otherwise never be checked.
func TestEveryInlineScriptCarriesTheNonce(t *testing.T) {
	templates, err := fs.Glob(assets, "templates/*/*.html")
	if err != nil {
		t.Fatalf("glob templates: %v", err)
	}
	if len(templates) == 0 {
		t.Fatal("no templates were scanned, so this test proves nothing")
	}

	// An opening script tag that has no src — that is, one with a body.
	inline := regexp.MustCompile(`<script(?:\s[^>]*)?>`)
	hasSrc := regexp.MustCompile(`\ssrc=`)
	hasNonce := regexp.MustCompile(`\snonce=`)

	var found int
	for _, name := range templates {
		raw, err := fs.ReadFile(assets, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, tag := range inline.FindAllString(string(raw), -1) {
			if hasSrc.MatchString(tag) {
				// Loaded from this origin, which `script-src 'self'` already
				// allows. A nonce on it would be noise.
				continue
			}
			found++
			if !hasNonce.MatchString(tag) {
				t.Errorf("%s has an inline script with no nonce: %s", name, tag)
			}
		}
	}
	if found == 0 {
		t.Fatal("no inline scripts were found, so this test proves nothing")
	}
}
