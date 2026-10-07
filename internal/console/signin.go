package console

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/authentik"
	"github.com/CoderSyndicate/doleances/internal/console/auth"
)

// The sign-in routes. The callback's path is a constant in the auth package
// because it is written into the provider's redirect URI at provisioning time
// and matched strictly on every sign-in afterwards.
const (
	LoginPath  = "/auth/login"
	LogoutPath = "/auth/logout"
)

// callerKey carries the signed-in person through a request.
type callerKeyType struct{}

var callerKey callerKeyType

// whoIs returns the person making a request, or nil.
func whoIs(ctx context.Context) *auth.Identity {
	who, _ := ctx.Value(callerKey).(*auth.Identity)
	return who
}

// registerSignIn declares the three routes the handshake needs.
//
// They are the only pages on this console that an unauthenticated browser may
// reach, which is what the gate below enforces — everything else is staff-only
// by default rather than by remembering to say so.
func (c *console) registerSignIn(mux *http.ServeMux) {
	mux.HandleFunc("GET "+LoginPath, c.beginSignIn)
	mux.HandleFunc("GET "+auth.CallbackPath, c.finishSignIn)
	mux.HandleFunc("POST "+LogoutPath, c.signOut)
}

func (c *console) beginSignIn(w http.ResponseWriter, r *http.Request) {
	if c.authenticator == nil {
		http.Error(w, "this console has no identity provider configured", http.StatusServiceUnavailable)
		return
	}

	// Where to put them afterwards. Only a path on this console: an absolute
	// URL here would make every sign-in link a way to bounce somebody to
	// somewhere else, with this console's name on it.
	returnTo := r.URL.Query().Get("next")
	if !strings.HasPrefix(returnTo, "/") || strings.HasPrefix(returnTo, "//") {
		returnTo = "/"
	}

	where, _, err := c.authenticator.Begin(returnTo)
	if err != nil {
		log.Error().Err(err).Msg("cannot begin a sign-in")
		http.Error(w, "cannot begin a sign-in", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, where, http.StatusSeeOther)
}

func (c *console) finishSignIn(w http.ResponseWriter, r *http.Request) {
	if c.authenticator == nil {
		http.Error(w, "this console has no identity provider configured", http.StatusServiceUnavailable)
		return
	}

	query := r.URL.Query()
	if refused := query.Get("error"); refused != "" {
		// The provider refused, which is its answer rather than a fault here.
		log.Warn().Str("error", refused).Msg("the identity provider refused a sign-in")
		http.Error(w, "the identity provider refused that sign-in", http.StatusForbidden)
		return
	}

	token, returnTo, err := c.authenticator.Complete(r.Context(),
		query.Get("state"), query.Get("code"))
	switch {
	case errors.Is(err, auth.ErrNotStaff):
		// They authenticated perfectly well and hold no role here. Said apart
		// from a failure because the remedy is different: an admin has to give
		// them one, and nothing about the sign-in is broken.
		//
		// **The account is named, and that is the whole value of the
		// message.** Without it, somebody who still held a session as another
		// account in their own authentik — the bootstrap `akadmin`, say, which
		// is exactly who sets this register up — is told their roles are
		// missing when what actually happened is that they signed in as
		// somebody else. The first live installation lost ten minutes to it.
		//
		// It leaks nothing: it names the account they have this second proved
		// they hold.
		who := auth.WhoWasRefused(err)
		said := "that account has no role in this register"
		if who != "" {
			said = "you signed in as " + who + ", which holds no role in this register"
		}
		http.Error(w, said+" — an admin has to give you one, or you are signed in "+
			"to the identity provider as somebody else", http.StatusForbidden)
		return
	case errors.Is(err, auth.ErrUnknownHandshake):
		http.Error(w, "that sign-in was not started here, or took too long",
			http.StatusBadRequest)
		return
	case errors.Is(err, authentik.ErrTokenRefused):
		// They authenticated perfectly well. What failed is this console's own
		// credential to the directory, which nobody signing in can do anything
		// about — so it is said as the operator's problem rather than as
		// theirs. The commonest cause is a stock authentik token, which
		// expires after thirty minutes: the console then works for half an
		// hour and refuses everybody afterwards.
		log.Error().Err(err).Msg(
			"the directory refused this console's API token: nobody can sign in until it is replaced")
		http.Error(w,
			"this console's own access to the directory has expired or been revoked — "+
				"an operator has to run the setup wizard again with a token that does not expire",
			http.StatusServiceUnavailable)
		return
	case err != nil:
		log.Error().Err(err).Msg("a sign-in could not be completed")
		http.Error(w, "that sign-in could not be completed", http.StatusBadGateway)
		return
	}

	auth.SetSession(w, token, c.secureCookies)
	if returnTo == "" {
		returnTo = "/"
	}
	http.Redirect(w, r, returnTo, http.StatusSeeOther)
}

func (c *console) signOut(w http.ResponseWriter, r *http.Request) {
	if c.authenticator != nil {
		c.authenticator.SignOut(auth.SessionOf(r))
	}
	auth.ClearSession(w, c.secureCookies)
	http.Redirect(w, r, LoginPath, http.StatusSeeOther)
}

// gate is what makes this console staff-only.
//
// # Everything is closed unless it is named open
//
// Service middleware rather than a decoration on the handlers that looked
// sensitive, for the reason the rate limiter is: the route somebody forgets to
// guard is always the one that needed it. A new curation page added next year
// is behind this without anybody remembering to do anything.
//
// # What is open, and why each
//
// The sign-in routes, or nobody could ever sign in. The static assets and the
// theme, because a login page with no stylesheet is a login page somebody
// reports as broken. Nothing else.
//
// # --development still turns it all off
//
// That flag is what the local runner and every checker in `.local/` rely on,
// which is why it exists. It is also the reason this console warns loudly on
// every startup that enables it.
func (c *console) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.development {
			next.ServeHTTP(w, r)
			return
		}

		if openToEverybody(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		if c.authenticator == nil {
			// Provisioned is a question the console answered at startup; this
			// is the other half. A console with no identity provider cannot
			// authenticate anybody, and must not therefore let everybody in.
			// Named by port rather than by address. An operator knows their
			// own maintenance port and "run the setup wizard" alone sends them
			// looking on this one, where it has never been; printing the full
			// address here would put it on a page anybody can reach.
			http.Error(w,
				"this console has no identity provider configured yet — "+
					"run the setup wizard at /setup on the maintenance port",
				http.StatusServiceUnavailable)
			return
		}

		who := c.authenticator.Identify(auth.SessionOf(r))
		if who == nil {
			// A browser gets sent to sign in; anything else is told plainly.
			// A redirect to an identity provider is a useless answer to a
			// form post, and a confusing one to a script.
			if r.Method == http.MethodGet {
				http.Redirect(w, r, LoginPath+"?next="+r.URL.EscapedPath(), http.StatusSeeOther)
				return
			}
			http.Error(w, "sign in first", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey, who)))
	})
}

// openToEverybody is the allowlist, kept small and in one place.
func openToEverybody(path string) bool {
	switch path {
	case LoginPath, auth.CallbackPath, LogoutPath:
		return true
	}
	for _, prefix := range []string{"/static/", "/theme/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// mayAdminister checks a role before a page is drawn.
//
// Reading is checked against the session's copy of the roles, which is at most
// an hour old. Writing is checked against the directory as it is now — see
// confirm, which is what every curation decision goes through.
func (c *console) mayAdminister(r *http.Request) bool {
	return c.holds(r, auth.RoleAdmin)
}

func (c *console) holds(r *http.Request, role auth.Role) bool {
	if c.development {
		return true
	}
	who := whoIs(r.Context())
	return who != nil && who.Is(role)
}

// confirm re-reads somebody's roles from the directory before they change
// anything.
//
// This is where "authentik is authoritative" stops being a sentence. The
// session carries a copy so that reading the queue costs no call; acting on it
// — accepting, rejecting, adding somebody, taking a role away — asks the
// directory as it is now. A curator removed five minutes ago is refused on
// their next press rather than when their session happens to lapse.
//
// A directory that cannot be reached refuses, deliberately. Carrying on with
// the session's copy because authentik was briefly down is exactly what the
// rule exists to prevent.
func (c *console) confirm(w http.ResponseWriter, r *http.Request, role auth.Role) (*auth.Identity, bool) {
	if c.development {
		return &auth.Identity{Username: developmentActor, Subject: developmentActor}, true
	}
	if c.authenticator == nil {
		http.Error(w, "this console has no identity provider configured", http.StatusServiceUnavailable)
		return nil, false
	}

	who, err := c.authenticator.Revalidate(r.Context(), auth.SessionOf(r))
	switch {
	case errors.Is(err, auth.ErrNotStaff), errors.Is(err, auth.ErrUnknownHandshake):
		auth.ClearSession(w, c.secureCookies)
		http.Error(w, "sign in again", http.StatusUnauthorized)
		return nil, false
	case errors.Is(err, authentik.ErrTokenRefused):
		log.Error().Err(err).Msg(
			"the directory refused this console's API token: nothing can be decided until it is replaced")
		http.Error(w,
			"this console's own access to the directory has expired or been revoked — "+
				"an operator has to run the setup wizard again with a token that does not expire",
			http.StatusServiceUnavailable)
		return nil, false
	case err != nil:
		log.Error().Err(err).Msg("cannot confirm a role against the directory")
		http.Error(w, "cannot reach the directory to confirm what you may do",
			http.StatusBadGateway)
		return nil, false
	}

	if !who.Is(role) {
		log.Warn().Str("username", who.Username).Str("wanted", string(role)).
			Msg("console: refused an action the account does not hold the role for")
		http.Error(w, "you do not hold the role that allows this", http.StatusForbidden)
		return nil, false
	}
	return who, true
}
