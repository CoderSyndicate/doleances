// Package auth signs console staff in against the identity provider the
// setup wizard provisioned.
//
// # Why a library rather than the hand-rolled client next door
//
// `internal/authentik` is hand-rolled because it makes nine REST calls and an
// SDK for that is a dependency to keep in step for no gain. This is the
// opposite case. Verifying an ID token means checking a signature against a
// key set fetched from a discovery document, the issuer, the audience, the
// expiry and the nonce — and getting any one of them wrong produces a console
// that signs people in and cannot say who they are. `go-oidc` is the
// well-trodden implementation and this is not the place to be original.
//
// # Where roles come from, and where they do not
//
// Not from the token. authentik can be told to put groups in a claim, and a
// claim is a snapshot of the moment somebody signed in — so a curator removed
// from the directory would keep curating until their token expired. The plan's
// rule is that **authentik is authoritative**, so roles are read from the
// directory itself.
//
// Reading them on every request would be a call to authentik per page. So the
// session carries them, briefly, and anything that writes re-reads them first:
// reading the queue is fast, and accepting a doléance — the act that takes
// somebody's words off or puts them on the register — is checked against the
// directory as it is now.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/rs/zerolog/log"
	"golang.org/x/oauth2"
)

// How long a session lasts, and how long the handshake it started may take.
//
// An hour because of what the session carries: the roles, which are a copy of
// something authentik owns. A day would mean somebody removed from the
// directory keeping their console for a day. Writes re-read the directory
// anyway, so the hour bounds how long a stale *read* can last — the queue, the
// listings — rather than how long somebody can act.
const (
	SessionLifetime = time.Hour
	handshakeWindow = 10 * time.Minute
)

// Role is what somebody may do.
type Role string

const (
	// RoleAdmin may add and remove people and change their roles.
	RoleAdmin Role = "admin"
	// RoleCurator may accept and reject.
	RoleCurator Role = "curator"
)

// Identity is a signed-in member of staff.
type Identity struct {
	// Subject is authentik's stable identifier for them, and is what the
	// audit log records. It does not change when somebody is renamed, which
	// is why it rather than the username is the thing written down.
	Subject string

	// Username and Name are for showing on a page and for reading in a log.
	Username string
	Name     string

	// Roles is what the directory said when this was last checked.
	Roles map[Role]bool

	// CheckedAt is when the directory last said it. Writes re-read if this is
	// older than they are willing to trust.
	CheckedAt time.Time
}

// Is reports whether this person holds a role. Safe on a zero Identity: a
// lookup in a nil map is false, which is the right answer for nobody.
func (i Identity) Is(role Role) bool { return i.Roles[role] }

// Any reports whether they hold any role at all. Somebody in the directory
// with neither is not staff here, whatever they are elsewhere.
func (i Identity) Any() bool { return i.Roles[RoleAdmin] || i.Roles[RoleCurator] }

// Config is what the console needs to talk to the provider.
type Config struct {
	// IssuerURL is the authentik application's OIDC issuer — discovery hangs
	// off it.
	IssuerURL string

	ClientID     string
	ClientSecret string

	// RedirectURL is this console's callback, and must match the redirect URI
	// the wizard registered on the provider exactly: authentik matches it
	// strictly, which is what stops somebody claiming a redirect of their own.
	RedirectURL string
}

// RoleReader is how the directory is asked what somebody may do.
//
// An interface because the answer comes from authentik in production and from
// a test here, and because it is the one dependency of this package that is
// genuinely about the directory rather than about tokens.
type RoleReader interface {
	RolesOf(ctx context.Context, subject, username string) (map[Role]bool, error)
}

// Authenticator runs the handshake and keeps the sessions it produces.
type Authenticator struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	roles    RoleReader

	// sessions is in process memory, which is the same trade the rate limiter
	// already makes and is worth stating: two console replicas do not share
	// them, so somebody signing in to one is not signed in to the other. For a
	// handful of curators behind one deployment that is invisible; for several
	// replicas it is a sign-in per replica, which is a nuisance rather than a
	// fault. Sharing them needs a store, and a store needs a deployment
	// decision this project has deferred once already.
	mu       sync.RWMutex
	sessions map[string]*Identity
	pending  map[string]pendingHandshake
}

// pendingHandshake is a login in flight: what was sent to the provider, kept
// until it answers.
type pendingHandshake struct {
	nonce    string
	verifier string
	returnTo string
	started  time.Time
}

// New builds an authenticator, discovering the provider.
//
// Discovery is a call to authentik, so this fails when the provider is
// unreachable — which is correct: a console that cannot verify a token must
// not start up pretending it can.
func New(ctx context.Context, cfg Config, roles RoleReader) (*Authenticator, error) {
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("discover the identity provider at %s: %w", cfg.IssuerURL, err)
	}

	return &Authenticator{
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		roles:    roles,
		sessions: map[string]*Identity{},
		pending:  map[string]pendingHandshake{},
	}, nil
}

// Begin starts a handshake and returns where to send the browser.
//
// # Three things travel with it and each stops something
//
// **State** is checked when the provider answers, which is what stops somebody
// completing a login the person never started — a cross-site request forgery
// against the login itself.
//
// **Nonce** is bound into the ID token, which stops a token captured from one
// login being replayed into another.
//
// **PKCE** stops an authorization code that leaked — from a log, a referrer, a
// proxy — being exchanged by anybody but the browser that asked for it.
//
// None is optional and all three are cheap.
func (a *Authenticator) Begin(returnTo string) (url, state string, err error) {
	state, err = randomToken()
	if err != nil {
		return "", "", err
	}
	nonce, err := randomToken()
	if err != nil {
		return "", "", err
	}
	challenge := oauth2.GenerateVerifier()

	a.mu.Lock()
	a.sweepPending()
	a.pending[state] = pendingHandshake{
		nonce: nonce, verifier: challenge, returnTo: returnTo, started: time.Now(),
	}
	a.mu.Unlock()

	url = a.oauth.AuthCodeURL(state,
		oidc.Nonce(nonce),
		oauth2.S256ChallengeOption(challenge),
	)
	return url, state, nil
}

// Errors the callback can produce, told apart because they mean different
// things to whoever is looking at the page.
var (
	// ErrUnknownHandshake is a callback for a login this console did not
	// start, or started so long ago it has been forgotten.
	ErrUnknownHandshake = errors.New("this sign-in was not started here")

	// notStaff carries who was refused, so the page can say it.
	//
	// The username is the whole value of that message: somebody still signed
	// in to their own authentik as another account — the bootstrap `akadmin`,
	// say, which is exactly who provisions this — is otherwise told their
	// roles are missing, when what happened is that they signed in as somebody
	// else.
	// ErrNotStaff is somebody authentik knows and this register does not
	// employ. They authenticated perfectly well; they simply hold no role.
	ErrNotStaff = errors.New("this account holds no role in this register")
)

// Complete finishes a handshake and issues a session.
func (a *Authenticator) Complete(ctx context.Context, state, code string) (token, returnTo string, err error) {
	a.mu.Lock()
	handshake, known := a.pending[state]
	delete(a.pending, state)
	a.mu.Unlock()

	if !known {
		return "", "", ErrUnknownHandshake
	}
	if time.Since(handshake.started) > handshakeWindow {
		return "", "", ErrUnknownHandshake
	}

	exchanged, err := a.oauth.Exchange(ctx, code, oauth2.VerifierOption(handshake.verifier))
	if err != nil {
		return "", "", fmt.Errorf("exchange the authorization code: %w", err)
	}

	raw, ok := exchanged.Extra("id_token").(string)
	if !ok || raw == "" {
		return "", "", errors.New("the provider returned no id token")
	}

	verified, err := a.verifier.Verify(ctx, raw)
	if err != nil {
		return "", "", fmt.Errorf("verify the id token: %w", err)
	}
	if verified.Nonce != handshake.nonce {
		// A token minted for a different login. Checked explicitly rather than
		// trusted to the library, because this is the one of the three guards
		// whose absence is invisible in testing.
		return "", "", errors.New("the id token belongs to a different sign-in")
	}

	var claims struct {
		Subject           string `json:"sub"`
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
		Email             string `json:"email"`
	}
	if err := verified.Claims(&claims); err != nil {
		return "", "", fmt.Errorf("read the id token: %w", err)
	}
	if claims.Subject == "" {
		claims.Subject = verified.Subject
	}

	// The directory, not the token. See the package comment.
	roles, err := a.roles.RolesOf(ctx, claims.Subject, claims.PreferredUsername)
	if err != nil {
		return "", "", fmt.Errorf("read the roles of %q: %w", claims.PreferredUsername, err)
	}

	who := &Identity{
		Subject:   claims.Subject,
		Username:  claims.PreferredUsername,
		Name:      claims.Name,
		Roles:     roles,
		CheckedAt: time.Now(),
	}
	if !who.Any() {
		// Authenticated and not employed here. Said apart from a failure
		// because the remedy is different: an admin has to give them a role,
		// not fix anything about the sign-in.
		log.Warn().Str("username", who.Username).
			Msg("console sign-in refused: the account holds no role in this register")
		return "", "", notStaff{Username: who.Username}
	}

	token, err = randomToken()
	if err != nil {
		return "", "", err
	}

	a.mu.Lock()
	a.sweepSessions()
	a.sessions[token] = who
	a.mu.Unlock()

	log.Info().Str("username", who.Username).Str("subject", who.Subject).
		Bool("admin", who.Roles[RoleAdmin]).Bool("curator", who.Roles[RoleCurator]).
		Msg("console sign-in")

	return token, handshake.returnTo, nil
}

// Identify resolves a session token, or nil.
func (a *Authenticator) Identify(token string) *Identity {
	if token == "" {
		return nil
	}

	a.mu.RLock()
	who, known := a.sessions[token]
	a.mu.RUnlock()

	if !known || time.Since(who.CheckedAt) > SessionLifetime {
		return nil
	}
	copied := *who
	return &copied
}

// Revalidate re-reads somebody's roles from the directory.
//
// Called before anything that writes. The session's copy is what makes reading
// fast; this is what makes acting correct — a curator removed from the
// directory stops being able to accept or reject on their next attempt rather
// than when their session happens to lapse.
func (a *Authenticator) Revalidate(ctx context.Context, token string) (*Identity, error) {
	who := a.Identify(token)
	if who == nil {
		return nil, ErrUnknownHandshake
	}

	roles, err := a.roles.RolesOf(ctx, who.Subject, who.Username)
	if err != nil {
		return nil, fmt.Errorf("re-read the roles of %q: %w", who.Username, err)
	}

	who.Roles = roles
	who.CheckedAt = time.Now()

	a.mu.Lock()
	if stored, known := a.sessions[token]; known {
		stored.Roles = roles
		stored.CheckedAt = who.CheckedAt
	}
	a.mu.Unlock()

	if !who.Any() {
		a.SignOut(token)
		return nil, notStaff{Username: who.Username}
	}
	return who, nil
}

// SignOut forgets a session.
func (a *Authenticator) SignOut(token string) {
	a.mu.Lock()
	delete(a.sessions, token)
	a.mu.Unlock()
}

// sweepPending and sweepSessions forget what has lapsed. The caller holds the
// lock. Without them the two maps are an unbounded record of everybody who
// ever started a login, which is both a leak and a list nobody asked us to keep.
func (a *Authenticator) sweepPending() {
	for state, handshake := range a.pending {
		if time.Since(handshake.started) > handshakeWindow {
			delete(a.pending, state)
		}
	}
}

func (a *Authenticator) sweepSessions() {
	for token, who := range a.sessions {
		if time.Since(who.CheckedAt) > SessionLifetime {
			delete(a.sessions, token)
		}
	}
}

// randomToken is 32 bytes, URL-safe.
func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("read randomness: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// SessionCookie is where the console keeps the token.
const SessionCookie = "doleances_console"

// SetSession writes the cookie.
//
// `HttpOnly`, so an injected script on a console page cannot read a token that
// authorises accepting and rejecting other people's words. `SameSite=Lax`
// rather than Strict for the reason the frontend gives: Strict drops the
// cookie on any navigation from another site, and nothing here writes on a GET.
func SetSession(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(SessionLifetime.Seconds()),
	})
}

// ClearSession removes it.
func ClearSession(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// SessionOf reads the token off a request.
func SessionOf(r *http.Request) string {
	cookie, err := r.Cookie(SessionCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// CallbackPath is where the provider sends a browser back.
//
// A constant rather than configuration because it is written into the
// provider's redirect URI at provisioning time and matched strictly on every
// sign-in afterwards. Two places that could disagree about it would be two
// places to get a login wrong.
const CallbackPath = "/auth/callback"

// notStaff is ErrNotStaff with a name attached.
//
// A distinct type rather than a wrapped string, so `errors.Is(err,
// ErrNotStaff)` keeps answering for every caller that only wants to know
// which refusal this is.
type notStaff struct{ Username string }

func (n notStaff) Error() string {
	if n.Username == "" {
		return ErrNotStaff.Error()
	}
	return n.Username + ": " + ErrNotStaff.Error()
}

func (n notStaff) Is(target error) bool { return target == ErrNotStaff }

// WhoWasRefused names the account a refusal was about, or "".
func WhoWasRefused(err error) string {
	var refused notStaff
	if errors.As(err, &refused) {
		return refused.Username
	}
	return ""
}
