package web

import (
	"strings"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/theme"
)

// TestTheShippedMarkIsNotSandboxed.
//
// `sandbox` makes a resource an opaque origin, and a browser will not paint an
// opaque-origin SVG as a favicon. The mark was therefore fetched on every page
// load, answered 200 with the right content type and a well-formed document,
// and the tab stayed empty — which is a header working exactly as specified
// against the one asset whose entire job is to be drawn by browser chrome.
func TestTheShippedMarkIsNotSandboxed(t *testing.T) {
	for _, slot := range []string{"mark", "logo"} {
		data, _, ok := theme.DefaultAsset(slot)
		if !ok {
			t.Fatalf("%s has no built-in default", slot)
		}
		policy := assetPolicy(slot, data)
		if strings.Contains(policy, "sandbox") {
			t.Errorf("%s is sandboxed, so a browser will not draw it: %q", slot, policy)
		}
		// Still no subresources, and still able to carry its own colours —
		// which a favicon must, because no page stylesheet reaches one.
		if !strings.Contains(policy, "default-src 'none'") {
			t.Errorf("%s lost its default-src: %q", slot, policy)
		}
		if !strings.Contains(policy, "style-src 'unsafe-inline'") {
			t.Errorf("%s cannot carry its own colours: %q", slot, policy)
		}
	}
}

// TestAnUploadedImageIsStillSandboxed is the half that must not regress.
//
// An SVG can carry script, an uploaded one comes from whoever can reach the
// console, and it is served from this origin — so without the sandbox an image
// upload is code execution here.
func TestAnUploadedImageIsStillSandboxed(t *testing.T) {
	uploaded := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	if policy := assetPolicy("logo", uploaded); !strings.Contains(policy, "sandbox") {
		t.Errorf("an uploaded image was not sandboxed: %q", policy)
	}
	// And a slot with no built-in default at all cannot be mistaken for one.
	if policy := assetPolicy("banner", uploaded); !strings.Contains(policy, "sandbox") {
		t.Errorf("an upload in a slot with no default was not sandboxed: %q", policy)
	}
}
