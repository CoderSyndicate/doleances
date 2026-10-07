package frontend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/apiclient"
)

// sessionCookie is where a reader's session token lives.
//
// # A cookie, not local storage
//
// The plan this replaced said local storage, and said why it was uncomfortable
// in the same breath: "readable by any injected script". That was tolerable
// when the token arrived in an email and a page had to pick it out of a URL. It
// is not tolerable now, because the same token is what authorises registering a
// passkey and removing one.
//
// So the credential is an `HttpOnly` cookie set by this service and never
// visible to a script on the page. The passkey ceremony still runs in the
// browser — it has to, it is `navigator.credentials` — but what comes back is
// posted to this service, which sets the cookie and hands the page nothing.
const sessionCookie = "doleances_session"

// sameSite is Lax rather than Strict.
//
// Strict would drop the cookie on any navigation from another site, which
// includes every link somebody sends a friend — a group page, a notification,
// an action. The threat Strict answers is a cross-site *write*, and nothing
// here writes on a GET.
const sameSite = http.SameSiteLaxMode

// setSession stores a reader's session token.
func (s *site) setSession(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookie,
		Value: token,
		Path:  "/",
		// Not readable by any script on the page. This is the whole point.
		HttpOnly: true,
		// Only over TLS wherever the deployment is served over TLS. Derived
		// from the site's own URL rather than configured separately, so a
		// production deployment cannot be told one thing and behave another.
		Secure:   s.secureCookies,
		SameSite: sameSite,
		Expires:  expires,
	})
}

// clearSession signs a device out of this service.
func (s *site) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookies,
		SameSite: sameSite,
		MaxAge:   -1,
	})
}

// session reads the reader's token, or "" when they are not signed in.
func session(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// reader returns a backend client speaking for whoever is asking.
//
// Every handler that needs to know who somebody is goes through here, so there
// is one place where the cookie becomes a credential — and no handler can
// forget to pass it and silently render the anonymous version of a page.
func (s *site) reader(r *http.Request) *apiclient.Client {
	return s.backend.As(session(r))
}

// signedIn reports whether the request carries a session at all.
//
// It says nothing about whether the session is still valid — only the backend
// knows that. It is used to decide which page to render, never to decide what
// somebody may do: an expired token gets a sign-in page, not an authorisation.
func signedIn(r *http.Request) bool { return session(r) != "" }

// The two proxied API prefixes.
//
// # Why the frontend proxies at all
//
// The backend is not publicly exposed — that is a property the whole deployment
// rests on, and it is what lets the exposed services ship with no database
// driver and no credentials. So a browser cannot call it, and the parts of the
// account flow that *have* to happen in a browser — `navigator.credentials`,
// the Push API — need somewhere on this origin to post to.
//
// The proxy is deliberately dumb about the payloads. A WebAuthn answer is a
// signed structure: anything this service reshaped on the way through would be
// something the signature no longer covers.
const (
	accountAPIPrefix = "/api/account/"
	notifyAPIPrefix  = "/api/notifications/"
)

// maxProxyBytes bounds a proxied request body. A ceremony answer is a few
// kilobytes; a push subscription is smaller.
const maxProxyBytes = 64 << 10

func (s *site) registerAccountAPI(mux *http.ServeMux) {
	mux.HandleFunc(accountAPIPrefix, s.proxyAccount)
	mux.HandleFunc(notifyAPIPrefix, s.proxyNotifications)
}

func (s *site) proxyAccount(w http.ResponseWriter, r *http.Request) {
	s.proxy(w, r, accountAPIPrefix, "/v1/accounts/")
}

func (s *site) proxyNotifications(w http.ResponseWriter, r *http.Request) {
	s.proxy(w, r, notifyAPIPrefix, "/v1/notifications/")
}

// proxy forwards one call to the backend as the reader making it.
//
// Three things happen here that a plain reverse proxy would not do, and each is
// the reason this is hand-written:
//
//   - The cookie becomes a bearer header, so the backend is never ambiently
//     authenticated and CSRF stays a question for this service alone.
//   - A session in the answer becomes a cookie and is **removed from the
//     body**, so the page's own script never holds the credential it just
//     caused to be issued.
//   - The reader's address is forwarded, because the backend's signup limiter
//     would otherwise see every account in the world coming from this service.
func (s *site) proxy(w http.ResponseWriter, r *http.Request, prefix, target string) {
	path := strings.TrimPrefix(r.URL.Path, prefix)
	if path == "" || strings.Contains(path, "..") {
		http.NotFound(w, r)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxProxyBytes))
	if err != nil {
		http.Error(w, "that request is too large", http.StatusRequestEntityTooLarge)
		return
	}

	answer, status, err := s.backend.Forward(r.Context(), apiclient.Forwarded{
		Method:  r.Method,
		Path:    target + path,
		Query:   r.URL.RawQuery,
		Body:    body,
		Session: session(r),
		From:    s.clientAddress(r),
	})
	if err != nil {
		log.Error().Err(err).Str("path", path).Msg("cannot reach the backend")
		http.Error(w, "the register is having trouble", http.StatusBadGateway)
		return
	}

	answer = s.captureSession(w, answer)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Never cached. These answers are about one reader and several of them
	// carry what that reader may do.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(answer) //nolint:errcheck
}

// captureSession turns a session in the backend's answer into a cookie.
//
// The token is stripped from what reaches the page. A script that could read it
// could also exfiltrate it, and the whole reason the cookie is `HttpOnly` is
// that this token authorises adding and removing passkeys.
//
// Anything that is not an object with a session in it passes through untouched,
// so this cannot corrupt an answer it does not understand.
func (s *site) captureSession(w http.ResponseWriter, answer []byte) []byte {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(answer, &fields); err != nil {
		return answer
	}

	raw, carries := fields["session"]
	if !carries {
		return answer
	}

	var token string
	if err := json.Unmarshal(raw, &token); err != nil || token == "" {
		return answer
	}

	expires := time.Now().Add(24 * time.Hour)
	if at, present := fields["expires_at"]; present {
		var when time.Time
		if err := json.Unmarshal(at, &when); err == nil && !when.IsZero() {
			expires = when
		}
	}
	s.setSession(w, token, expires)

	delete(fields, "session")
	// Replaced by a flag, so the page knows it is signed in without ever
	// holding the thing that says so.
	fields["signed_in"] = json.RawMessage("true")

	trimmed, err := json.Marshal(fields)
	if err != nil {
		// Unreachable in practice, and if it were reachable the safe answer is
		// the one with no token in it.
		return []byte(`{"signed_in":true}`)
	}
	return trimmed
}

// clientAddress is who a request is really from.
//
// Forwarded to the backend so its ceremony limiter bounds readers rather than
// bounding this service — without it the backend sees every account in the
// world arriving from one place.
//
// It resolves through the same trusted-proxy list this service's own limiter
// uses, and that is the point of it being one method. The earlier version read
// `X-Forwarded-For` whenever it was present and said that a spoofed one buys
// somebody their own bucket, which is what the limiter was going to give them
// anyway. That was true while this was only a hint to a limiter keyed on the
// same header. It is not true now: two different answers to "who is this"
// would mean a caller who sets the header walks through the backend's ceremony
// limit while this service's limit still holds against their real address.
func (s *site) clientAddress(r *http.Request) string {
	return s.proxies.ClientAddress(r)
}

// secureFor reports whether cookies on this deployment should be TLS-only.
//
// Derived from the site's own URL rather than configured separately: a
// production deployment told one thing and behaving another is exactly the
// mistake a second setting invites, and the cost of getting it wrong is a
// session token on the wire.
func secureFor(siteURL string) bool {
	parsed, err := url.Parse(siteURL)
	if err != nil {
		return false
	}
	return parsed.Scheme == "https"
}

// serviceWorker serves the push receiver from the root.
//
// It lives in the embedded assets like everything else and is served here
// rather than under /static for one reason: a service worker's scope is the
// path it was served from, so one at /static/sw.js could only receive a push
// for /static.
func (s *site) serviceWorker(w http.ResponseWriter, r *http.Request) {
	worker, err := assets.ReadFile("static/sw.js")
	if err != nil {
		// Unreachable: the file is compiled into the binary. Answered as a
		// 404 rather than a 500 because a browser asking for a worker that is
		// not there should simply not register one.
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	// Short, not immutable. A worker is the one asset a browser keeps until it
	// is told otherwise, and a stale one is a push nobody receives.
	w.Header().Set("Cache-Control", "max-age=60")
	w.Write(worker) //nolint:errcheck
}
