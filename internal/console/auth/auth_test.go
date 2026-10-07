package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CoderSyndicate/doleances/internal/authentik"
)

// roleStub stands in for the directory.
type roleStub struct {
	roles map[Role]bool
	calls int
	err   error
}

func (r *roleStub) RolesOf(context.Context, string, string) (map[Role]bool, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return r.roles, nil
}

// signedIn puts somebody in the session map directly.
//
// The handshake itself is go-oidc's job — signature, issuer, audience, expiry
// — and reimplementing a fake identity provider to exercise it would be
// testing the library rather than this file. What is this file's own, and is
// what these tests cover, is everything around it: the state and nonce it
// keeps, the sessions it issues, and the rule that a role is the directory's
// answer rather than a token's.
func signedIn(a *Authenticator, who *Identity) string {
	token, _ := randomToken()
	a.mu.Lock()
	a.sessions[token] = who
	a.mu.Unlock()
	return token
}

func blank(roles *roleStub) *Authenticator {
	return &Authenticator{
		roles:    roles,
		sessions: map[string]*Identity{},
		pending:  map[string]pendingHandshake{},
	}
}

// TestEveryHandshakeIsUnique. State is what stops somebody completing a login
// the person never started; a state that repeated would be one an attacker
// could predict and pre-empt.
func TestEveryHandshakeIsUnique(t *testing.T) {
	a := blank(&roleStub{})
	a.oauth.Endpoint.AuthURL = "https://authentik.example/authorize"

	seen := map[string]bool{}
	nonces := map[string]bool{}
	for range 25 {
		_, state, err := a.Begin("/")
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if seen[state] {
			t.Fatal("two handshakes got the same state")
		}
		seen[state] = true

		a.mu.RLock()
		pending := a.pending[state]
		a.mu.RUnlock()
		if nonces[pending.nonce] {
			t.Fatal("two handshakes got the same nonce")
		}
		nonces[pending.nonce] = true
		if pending.verifier == "" {
			t.Fatal("a handshake carries no PKCE verifier")
		}
	}
}

// TestACallbackForALoginNobodyStartedIsRefused — the whole point of state.
func TestACallbackForALoginNobodyStartedIsRefused(t *testing.T) {
	a := blank(&roleStub{})

	_, _, err := a.Complete(context.Background(), "a-state-nobody-issued", "a-code")
	if !errors.Is(err, ErrUnknownHandshake) {
		t.Errorf("err = %v, want ErrUnknownHandshake", err)
	}
}

// TestAStaleHandshakeIsRefused. A login left open for an hour is not one
// somebody is still completing; it is one whose state has had an hour to leak.
func TestAStaleHandshakeIsRefused(t *testing.T) {
	a := blank(&roleStub{})
	a.oauth.Endpoint.AuthURL = "https://authentik.example/authorize"

	_, state, err := a.Begin("/")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	a.mu.Lock()
	stale := a.pending[state]
	stale.started = time.Now().Add(-handshakeWindow - time.Minute)
	a.pending[state] = stale
	a.mu.Unlock()

	if _, _, err := a.Complete(context.Background(), state, "a-code"); !errors.Is(err, ErrUnknownHandshake) {
		t.Errorf("err = %v, want ErrUnknownHandshake", err)
	}
}

// TestAStateIsSpentOnce, so a callback replayed with the same state cannot
// mint a second session.
func TestAStateIsSpentOnce(t *testing.T) {
	a := blank(&roleStub{})
	a.oauth.Endpoint.AuthURL = "https://authentik.example/authorize"

	_, state, err := a.Begin("/")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	// The first attempt fails at the exchange — there is no provider here —
	// but it must still have consumed the state.
	a.Complete(context.Background(), state, "a-code") //nolint:errcheck

	if _, _, err := a.Complete(context.Background(), state, "a-code"); !errors.Is(err, ErrUnknownHandshake) {
		t.Errorf("a state was accepted twice: %v", err)
	}
}

// TestASessionCarriesTheRolesAndLapses.
func TestASessionCarriesTheRolesAndLapses(t *testing.T) {
	a := blank(&roleStub{})
	token := signedIn(a, &Identity{
		Subject: "sub-1", Username: "dominique",
		Roles: map[Role]bool{RoleCurator: true}, CheckedAt: time.Now(),
	})

	who := a.Identify(token)
	if who == nil {
		t.Fatal("a fresh session does not resolve")
	}
	if !who.Is(RoleCurator) || who.Is(RoleAdmin) {
		t.Errorf("roles = %v", who.Roles)
	}

	// Past its life it stops resolving, which bounds how long a stale read can
	// last for somebody the directory has since changed its mind about.
	a.mu.Lock()
	a.sessions[token].CheckedAt = time.Now().Add(-SessionLifetime - time.Minute)
	a.mu.Unlock()

	if a.Identify(token) != nil {
		t.Error("a lapsed session still resolves")
	}
}

// TestRevalidateAsksTheDirectoryAgain. This is the half that makes "authentik
// is authoritative" true rather than aspirational: a curator removed from the
// directory stops being able to accept on their next attempt, not when their
// session happens to lapse.
func TestRevalidateAsksTheDirectoryAgain(t *testing.T) {
	directory := &roleStub{roles: map[Role]bool{RoleCurator: true}}
	a := blank(directory)
	token := signedIn(a, &Identity{
		Subject: "sub-1", Username: "dominique",
		Roles: map[Role]bool{RoleCurator: true}, CheckedAt: time.Now(),
	})

	// The directory changes its mind: still staff, no longer a curator.
	directory.roles = map[Role]bool{RoleAdmin: true}

	who, err := a.Revalidate(context.Background(), token)
	if err != nil {
		t.Fatalf("Revalidate: %v", err)
	}
	if who.Is(RoleCurator) {
		t.Error("a revoked role survived revalidation")
	}
	if !who.Is(RoleAdmin) {
		t.Error("a granted role did not arrive")
	}
	if directory.calls != 1 {
		t.Errorf("the directory was asked %d times, want once", directory.calls)
	}

	// And the stored session carries the new answer, so the next read agrees
	// with the last write rather than reverting.
	stored := a.Identify(token)
	if stored.Is(RoleCurator) || !stored.Is(RoleAdmin) {
		t.Errorf("the session kept the old roles: %v", stored.Roles)
	}
}

// TestSomebodyWhoLostEveryRoleIsSignedOut, rather than left holding a session
// that resolves to nobody. Half-signed-in is a state no page should have to
// render.
func TestSomebodyWhoLostEveryRoleIsSignedOut(t *testing.T) {
	directory := &roleStub{roles: map[Role]bool{}}
	a := blank(directory)
	token := signedIn(a, &Identity{
		Subject: "sub-1", Username: "dominique",
		Roles: map[Role]bool{RoleCurator: true}, CheckedAt: time.Now(),
	})

	if _, err := a.Revalidate(context.Background(), token); !errors.Is(err, ErrNotStaff) {
		t.Errorf("err = %v, want ErrNotStaff", err)
	}
	if a.Identify(token) != nil {
		t.Error("somebody with no roles left still has a session")
	}
}

// TestADirectoryThatCannotBeReachedDoesNotGrantAnything.
//
// The failure that matters: if asking authentik fails, the answer must not be
// "carry on with what the session said". Revalidate is called before a write,
// and a write going ahead on a stale copy because the directory was briefly
// down is exactly the thing the rule exists to prevent.
func TestADirectoryThatCannotBeReachedDoesNotGrantAnything(t *testing.T) {
	directory := &roleStub{err: errors.New("authentik is down")}
	a := blank(directory)
	token := signedIn(a, &Identity{
		Subject: "sub-1", Username: "dominique",
		Roles: map[Role]bool{RoleCurator: true}, CheckedAt: time.Now(),
	})

	if _, err := a.Revalidate(context.Background(), token); err == nil {
		t.Error("an unreachable directory was treated as agreement")
	}
}

// TestSigningOutForgets.
func TestSigningOutForgets(t *testing.T) {
	a := blank(&roleStub{})
	token := signedIn(a, &Identity{Subject: "s", Roles: map[Role]bool{RoleAdmin: true}, CheckedAt: time.Now()})

	a.SignOut(token)
	if a.Identify(token) != nil {
		t.Error("a signed-out session still resolves")
	}
}

// TestTheMapsDoNotGrowForEver. Both are a record of everybody who ever started
// a login; unswept they are a leak and a list nobody asked us to keep.
func TestTheMapsDoNotGrowForEver(t *testing.T) {
	a := blank(&roleStub{})
	a.oauth.Endpoint.AuthURL = "https://authentik.example/authorize"

	_, state, _ := a.Begin("/")
	a.mu.Lock()
	stale := a.pending[state]
	stale.started = time.Now().Add(-handshakeWindow - time.Minute)
	a.pending[state] = stale
	a.sessions["old"] = &Identity{CheckedAt: time.Now().Add(-SessionLifetime - time.Minute)}
	a.mu.Unlock()

	// Any new activity sweeps.
	a.Begin("/") //nolint:errcheck
	signedIn(a, &Identity{CheckedAt: time.Now()})
	a.mu.Lock()
	a.sweepSessions()
	_, staleStillThere := a.pending[state]
	_, oldStillThere := a.sessions["old"]
	a.mu.Unlock()

	if staleStillThere {
		t.Error("a lapsed handshake was kept")
	}
	if oldStillThere {
		t.Error("a lapsed session was kept")
	}
}

// TestTheCookieIsNotReadableByScript. It authorises accepting and rejecting
// other people's words, so an injected script on a console page must not be
// able to read it.
func TestTheCookieIsNotReadableByScript(t *testing.T) {
	recorder := httptest.NewRecorder()
	SetSession(recorder, "a-token", true)

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("%d cookies set", len(cookies))
	}
	set := cookies[0]
	if !set.HttpOnly {
		t.Error("the console session cookie is readable by script")
	}
	if !set.Secure {
		t.Error("the cookie is not marked Secure on an https deployment")
	}
	if set.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", set.SameSite)
	}

	// And it comes back off a request.
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(set)
	if SessionOf(request) != "a-token" {
		t.Errorf("SessionOf = %q", SessionOf(request))
	}

	// Clearing really clears.
	cleared := httptest.NewRecorder()
	ClearSession(cleared, true)
	if got := cleared.Result().Cookies()[0]; got.MaxAge >= 0 || got.Value != "" {
		t.Errorf("clearing left %+v", got)
	}
}

// TestNobodyIsSomebodyWithNoRoles — Is and Any must be safe on a zero value,
// since a page asks them about a reader who may not be signed in at all.
func TestNobodyIsSomebodyWithNoRoles(t *testing.T) {
	var nobody Identity

	if nobody.Is(RoleAdmin) || nobody.Is(RoleCurator) || nobody.Any() {
		t.Error("a zero identity holds a role")
	}
}

// TestARefusedDirectoryTokenKeepsItsIdentity.
//
// The console answers a refused token differently from every other failure:
// the person signing in authenticated perfectly, and what died is this
// console's own credential, which only an operator can replace. That answer
// depends entirely on the sentinel surviving the wrap this file puts around
// it — reported as an ordinary failure, it read as "that sign-in could not be
// completed", which sent the one person who could fix it to the wrong half of
// the system.
func TestARefusedDirectoryTokenKeepsItsIdentity(t *testing.T) {
	refused := fmt.Errorf("ask authentik about %q: %w", "akadmin", authentik.ErrTokenRefused)
	a := blank(&roleStub{err: refused})
	token := signedIn(a, &Identity{
		Subject: "sub-1", Username: "akadmin",
		Roles: map[Role]bool{RoleAdmin: true}, CheckedAt: time.Now(),
	})

	_, err := a.Revalidate(context.Background(), token)
	if !errors.Is(err, authentik.ErrTokenRefused) {
		t.Errorf("err = %v, want the refusal still recognisable", err)
	}

	// And it is still a refusal: nothing is granted on the way past.
	if who := a.Identify(token); who != nil && who.Is(RoleAdmin) {
		if _, allowed := a.Revalidate(context.Background(), token); allowed == nil {
			t.Error("a write was allowed while the directory refused this console")
		}
	}
}
