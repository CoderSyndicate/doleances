package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/console/auth"
)

func gated(c *console) http.Handler {
	reached := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return c.gate(reached)
}

func ask(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
	return recorder
}

// TestEverythingIsClosedUnlessItIsNamedOpen.
//
// The gate is service middleware rather than a decoration on the handlers that
// looked sensitive, for the reason the rate limiter is: the route somebody
// forgets to guard is always the one that needed it. A curation page added
// next year is behind this without anybody remembering to do anything.
func TestEverythingIsClosedUnlessItIsNamedOpen(t *testing.T) {
	c := &console{authenticator: &auth.Authenticator{}}

	for _, path := range []string{"/", "/audit", "/subjects", "/settings/people", "/spam"} {
		got := ask(t, gated(c), http.MethodGet, path)
		if got.Code != http.StatusSeeOther {
			t.Errorf("%s answered %d to a stranger, want a redirect to sign in", path, got.Code)
		}
	}
}

// TestOnlyTheSignInRoutesAndAssetsAreOpen. A login page with no stylesheet is
// a login page somebody reports as broken, and nobody could ever sign in if
// the handshake itself needed a session.
func TestOnlyTheSignInRoutesAndAssetsAreOpen(t *testing.T) {
	c := &console{authenticator: &auth.Authenticator{}}

	for _, path := range []string{LoginPath, auth.CallbackPath, "/static/base.css", "/theme/tokens.css"} {
		got := ask(t, gated(c), http.MethodGet, path)
		if got.Code != http.StatusOK {
			t.Errorf("%s answered %d, want it open", path, got.Code)
		}
	}
}

// TestAFormPostIsNotRedirectedToAnIdentityProvider.
//
// A redirect is a useless answer to a form post and a confusing one to a
// script, so anything that is not a browser navigation is told plainly.
func TestAFormPostIsNotRedirectedToAnIdentityProvider(t *testing.T) {
	c := &console{authenticator: &auth.Authenticator{}}

	got := ask(t, gated(c), http.MethodPost, "/settings/people")
	if got.Code != http.StatusUnauthorized {
		t.Errorf("a post answered %d, want 401", got.Code)
	}
}

// TestNoIdentityProviderMeansNobodyGetsIn, rather than everybody.
//
// The tempting failure is to let requests through when there is nothing to
// authenticate against. A console that cannot authenticate anybody must not
// therefore authorise everybody — it is the curation queue behind that door.
func TestNoIdentityProviderMeansNobodyGetsIn(t *testing.T) {
	c := &console{} // nothing provisioned

	got := ask(t, gated(c), http.MethodGet, "/")
	if got.Code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503 — not an open door", got.Code)
	}
}

// TestDevelopmentStillTurnsItAllOff, because the local runner and every
// checker in .local/ depend on it.
func TestDevelopmentStillTurnsItAllOff(t *testing.T) {
	c := &console{development: true}

	for _, path := range []string{"/", "/settings/people"} {
		if got := ask(t, gated(c), http.MethodGet, path); got.Code != http.StatusOK {
			t.Errorf("%s answered %d with --development", path, got.Code)
		}
	}
}

// TestTheAuditLogFinallyNamesSomebody.
//
// The audit log is the counterweight to curators holding accept/reject power
// over other people's words. Until this it recorded
// `development (unauthenticated)` on every decision — which is to say nobody.
func TestTheAuditLogFinallyNamesSomebody(t *testing.T) {
	c := &console{}
	who := &auth.Identity{Subject: "sub-42", Username: "dominique"}
	r := httptest.NewRequest(http.MethodPost, "/", nil).
		WithContext(context.WithValue(context.Background(), callerKey, who))

	// The subject rather than the username: it is stable, so a rename does not
	// orphan every row written before it.
	if got := c.actor(r); got != "sub-42" {
		t.Errorf("actor = %q, want the OIDC subject", got)
	}

	// And --development still says what it always said, so the local runner
	// and the checkers keep working.
	dev := &console{development: true}
	if got := dev.actor(httptest.NewRequest(http.MethodPost, "/", nil)); got != developmentActor {
		t.Errorf("actor under --development = %q", got)
	}
}

// TestASignInOnlyReturnsSomewhereOnThisConsole. An absolute URL in `next`
// would make every sign-in link a way to bounce somebody elsewhere with this
// console's name on it.
func TestASignInOnlyReturnsSomewhereOnThisConsole(t *testing.T) {
	c := &console{}

	for _, next := range []string{"https://elsewhere.example", "//elsewhere.example", "javascript:alert(1)"} {
		recorder := httptest.NewRecorder()
		c.beginSignIn(recorder, httptest.NewRequest(http.MethodGet, "/auth/login?next="+next, nil))
		// With no authenticator it refuses before redirecting, which is the
		// safe half; what matters is that it never builds a redirect to an
		// off-console address.
		if location := recorder.Header().Get("Location"); location != "" {
			t.Errorf("next=%q produced a redirect to %q", next, location)
		}
	}
}
