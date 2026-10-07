package console

import (
	"io/fs"
	"regexp"
	"testing"
)

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
