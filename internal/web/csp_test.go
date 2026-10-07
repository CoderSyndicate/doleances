package web

import (
	"encoding/base64"
	"html/template"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func served(t *testing.T, handler http.Handler) *http.Response {
	t.Helper()

	recorder := httptest.NewRecorder()
	SecurityHeaders()(handler).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/", nil))
	return recorder.Result()
}

// TestTheNonceSurvivesBeingPutInATemplate.
//
// `html/template` escapes attribute values, and standard base64 contains `+`,
// which it turns into `&#43;`. A browser decodes that back, so the match still
// holds and the page works — on the responses whose random bytes happened to
// contain no `+`. The rest are a security control failing about a third of the
// time for a reason two layers away from anything anybody would look at.
//
// The alphabet is the fix, so this is the test that pins it.
func TestTheNonceSurvivesBeingPutInATemplate(t *testing.T) {
	page := template.Must(template.New("t").Parse(`<script nonce="{{ .Nonce }}">x</script>`))

	for range 200 {
		var rendered strings.Builder
		var nonce string

		response := served(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			nonce = NonceFrom(r.Context())
			if err := page.Execute(&rendered, struct{ Nonce string }{nonce}); err != nil {
				t.Fatalf("render: %v", err)
			}
		}))

		if nonce == "" {
			t.Fatal("no nonce was minted")
		}
		if !strings.Contains(rendered.String(), `nonce="`+nonce+`"`) {
			t.Fatalf("the template changed the nonce: header %q, page %q",
				nonce, rendered.String())
		}
		// And the header names the same one the page carries.
		if !strings.Contains(response.Header.Get("Content-Security-Policy"), "'nonce-"+nonce+"'") {
			t.Fatalf("the policy names a different nonce than the page got")
		}
	}
}

// TestEveryResponseGetsItsOwnNonce. A nonce shared between responses is one an
// attacker can read off a page they are allowed to see and then use in an
// injection into a page they are not.
func TestEveryResponseGetsItsOwnNonce(t *testing.T) {
	seen := map[string]bool{}

	for range 500 {
		var nonce string
		served(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			nonce = NonceFrom(r.Context())
		}))

		if seen[nonce] {
			t.Fatalf("the nonce %q was issued twice", nonce)
		}
		seen[nonce] = true

		// Unguessable is the whole property: sixteen bytes of it.
		raw, err := base64.RawURLEncoding.DecodeString(nonce)
		if err != nil {
			t.Fatalf("the nonce is not the base64 it claims: %v", err)
		}
		if len(raw) != nonceBytes {
			t.Fatalf("the nonce carries %d bytes, want %d", len(raw), nonceBytes)
		}
	}
}

// TestAHandlerKeepsItsOwnPolicy.
//
// An uploaded SVG is served from this origin under `default-src 'none';
// sandbox`, which is what stops an image upload being code execution in the
// console. A blanket header overwriting it would undo that silently — the
// image would still render and the sandbox would simply be gone.
func TestAHandlerKeepsItsOwnPolicy(t *testing.T) {
	const own = "default-src 'none'; sandbox"

	response := served(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Security-Policy", own)
	}))

	if got := response.Header.Get("Content-Security-Policy"); got != own {
		t.Errorf("policy = %q, want the handler's own %q", got, own)
	}
}

// TestThePolicyRefusesWhatItIsFor. The entries that matter, asserted by name
// so that loosening one is a deliberate edit to a test rather than a quiet
// change to a string.
func TestThePolicyRefusesWhatItIsFor(t *testing.T) {
	policy := served(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		Header.Get("Content-Security-Policy")

	for _, want := range []string{
		"default-src 'none'",     // everything is refused until named
		"form-action 'self'",     // an injected form cannot post elsewhere
		"frame-ancestors 'none'", // nobody may frame this and collect a click
		"base-uri 'none'",        // a <base> tag cannot re-point the scripts
		"connect-src 'self'",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("the policy is missing %q", want)
		}
	}

	// No CDN, and no escape hatch. 'unsafe-inline' in script-src would make
	// the nonce pointless: a browser that sees both ignores the nonce.
	for _, refused := range []string{"'unsafe-inline'", "'unsafe-eval'", "unpkg", "*"} {
		if strings.Contains(policy, refused) {
			t.Errorf("the policy contains %q, which undoes it", refused)
		}
	}

	// script-src carries exactly 'self' and one nonce.
	script := regexp.MustCompile(`script-src ([^;]+)`).FindStringSubmatch(policy)
	if script == nil {
		t.Fatal("the policy names no script-src")
	}
	if fields := strings.Fields(script[1]); len(fields) != 2 || fields[0] != "'self'" {
		t.Errorf("script-src = %q, want 'self' and a nonce", script[1])
	}
}
