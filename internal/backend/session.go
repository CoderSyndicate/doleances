package backend

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/cache"
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/store"
	"github.com/CoderSyndicate/doleances/internal/token"
)

// sessionHeader carries the session token.
//
// A bearer header rather than a cookie, because the browser never talks to
// this service: the frontend holds the cookie, this holds the rules. Keeping
// the credential out of a cookie on this side also means no request here is
// ambiently authenticated — a call is signed in because it said so, which is
// what makes CSRF a question for the frontend alone.
const sessionHeader = "Authorization"

// requiresSession is the operation metadata key that marks a route as needing
// a signed-in account.
//
// The check lives in middleware and the requirement lives on the operation,
// so a new route declares what it needs and cannot forget to enforce it. A
// handler that checked for itself would be a handler somebody copies without
// the check.
const requiresSession = "session"

// authenticated marks an operation as needing a signed-in account.
func authenticated(op huma.Operation) huma.Operation {
	if op.Metadata == nil {
		op.Metadata = map[string]any{}
	}
	op.Metadata[requiresSession] = true
	// Declared in the spec as well as enforced, so a client generated from it
	// knows to send the header rather than discovering a 401.
	op.Security = append(op.Security, map[string][]string{"session": {}})
	return op
}

// rateLimited is the operation metadata key that marks a route as bounded per
// caller.
//
// It sits beside the authentication requirement rather than in the handler for
// the same reason: the routes that need it are the ones a new endpoint is
// copied from, and a limit somebody has to remember to add is a limit that
// will be missing on the route that needed it most.
const rateLimited = "ratelimit"

// bounded marks an operation as rate limited per caller.
func bounded(op huma.Operation) huma.Operation {
	if op.Metadata == nil {
		op.Metadata = map[string]any{}
	}
	op.Metadata[rateLimited] = true
	return op
}

// invalidated is the operation metadata naming what a write makes wrong.
//
// Declared on the route rather than called from inside the handler, and that
// is the whole point: a write that forgets to drop the register leaves a
// cache serving yesterday's front page, and nothing about the handler looks
// wrong. Here it sits beside the path, where somebody adding an endpoint is
// already reading.
const invalidated = "invalidates"

// invalidates marks an operation as making some family of cached answers
// wrong. The drop happens after the handler, and only if it succeeded.
func invalidates(tags ...cache.Tag) func(huma.Operation) huma.Operation {
	return func(op huma.Operation) huma.Operation {
		if op.Metadata == nil {
			op.Metadata = map[string]any{}
		}
		op.Metadata[invalidated] = tags
		return op
	}
}

// callerKey is where the resolved account is kept for the request.
type callerKeyType struct{}

var callerKey callerKeyType

// caller is who is making a request.
type caller struct {
	Account models.Account
	Session models.Session

	// TokenHash is the stored form of the presented token, so signing out
	// this device needs no second lookup.
	TokenHash string
}

// authenticate resolves the session token on every request and enforces it
// where the operation says it is required.
//
// Resolving even where it is not required is deliberate: several routes behave
// differently for a signed-in reader — a group page showing "leave" rather
// than "join" — and a second, optional lookup inside those handlers is how the
// two paths drift apart.
func (a *API) authenticate(api huma.API) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		if limited, _ := ctx.Operation().Metadata[rateLimited].(bool); limited {
			if a.signups != nil && !a.signups.Allow(clientAddress(ctx)) {
				// 429 rather than a refusal that looks like a failure: a
				// person who cancelled a fingerprint prompt three times is
				// not an attacker and should be told to wait, not told that
				// something is broken.
				// The only way this fails is a connection that has already
				// gone, where there is nothing left to say.
				_ = huma.WriteErr(api, ctx, http.StatusTooManyRequests,
					"too many attempts from here; wait a minute and try again")
				return
			}
		}

		presented := bearer(ctx.Header(sessionHeader))

		if presented != "" {
			hash := token.Hash(presented)
			account, session, err := a.store.Authenticate(ctx.Context(), hash)
			switch {
			case err == nil:
				ctx = huma.WithValue(ctx, callerKey, &caller{
					Account: account, Session: session, TokenHash: hash,
				})
			case errors.Is(err, store.ErrSessionNotFound):
				// An expired or revoked token is not an error worth logging:
				// it is what happens to every session eventually.
			default:
				log.Error().Err(err).Msg("cannot resolve a session")
			}
		}

		if required, _ := ctx.Operation().Metadata[requiresSession].(bool); required {
			if ctx.Context().Value(callerKey) == nil {
				_ = huma.WriteErr(api, ctx, http.StatusUnauthorized, "sign in first")
				return
			}
		}

		next(ctx)

		// After the handler, and only if it worked. A refused write changed
		// nothing, and dropping the register because somebody failed to
		// publish would hand the cache's whole value to anybody who can make
		// a request fail.
		if tags, ok := ctx.Operation().Metadata[invalidated].([]cache.Tag); ok {
			if status := ctx.Status(); status >= 200 && status < 400 {
				a.cache.Drop(tags...)
			}
		}
	}
}

// clientAddress is who a request is from, for the purpose of bounding it.
//
// `X-Forwarded-For` is trusted, and that is only sound because of a property
// this deployment already has: **the backend is not publicly exposed.** It is
// reachable from the console and the frontend and from nothing else, so the
// only thing that can set the header is one of ours. An installation that put
// this service on the open internet would be handing every caller the ability
// to claim any address — and would have larger problems than this limiter.
func clientAddress(ctx huma.Context) string {
	if forwarded := ctx.Header("X-Forwarded-For"); forwarded != "" {
		// The first entry is the original client; the rest are the proxies it
		// passed through.
		if first, _, found := strings.Cut(forwarded, ","); found {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(forwarded)
	}

	address := ctx.RemoteAddr()
	// Without the port, or every request from one machine is a new caller.
	if host, _, err := net.SplitHostPort(address); err == nil {
		return host
	}
	return address
}

// bearer pulls the token out of an Authorization header, tolerating a bare
// token as well as the scheme.
func bearer(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if scheme, rest, found := strings.Cut(value, " "); found &&
		strings.EqualFold(scheme, "bearer") {
		return strings.TrimSpace(rest)
	}
	return value
}

// callerOf returns who is making the request, or nil.
func callerOf(ctx context.Context) *caller {
	who, _ := ctx.Value(callerKey).(*caller)
	return who
}

// mustCaller returns who is making the request on a route that requires one.
//
// The middleware has already refused an unauthenticated request to such a
// route, so a nil here is a route that forgot to declare itself — answered as
// a 401 rather than a panic, because the failure mode of a misdeclared route
// should be a refusal rather than a crash.
func mustCaller(ctx context.Context) (*caller, error) {
	who := callerOf(ctx)
	if who == nil {
		return nil, huma.Error401Unauthorized("sign in first")
	}
	return who, nil
}
