package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
)

// The Content-Security-Policy is the second line of defence, and it exists
// because the first one is a promise rather than a mechanism.
//
// Escaping is what stops stranger-supplied text becoming script: `html/template`
// escapes everything and the browser code builds no markup from data. That is
// the real defence and it is not changing. But it is a property somebody has to
// keep, in a register whose pages are made almost entirely of what strangers
// wrote, and one missed escape anywhere would otherwise be arbitrary script on
// this origin.
//
// # What that would cost, now that accounts exist
//
// The session cookie is `HttpOnly`, so injected script cannot read it. It does
// not need to. The browser attaches that cookie to same-origin requests by
// itself, so a script on the page can simply ask this site to register another
// passkey and be answered. That is persistent account takeover, it survives
// signing out, and `HttpOnly` does nothing whatever about it.
//
// # Nonces rather than hashes
//
// Several pages carry an inline script, and most of them are not static: the
// console renders its translated strings into one. A hash would have to be
// recomputed for every such block and would break the moment a translation
// changed. A nonce is minted per response and put on every inline block the
// templates render.
//
// The nonce is worth nothing unless it is unpredictable per response, which is
// why it comes from crypto/rand rather than from anything cheaper.

// nonceKey carries the per-request nonce.
type nonceKeyType struct{}

var nonceKey nonceKeyType

// NonceFrom returns the nonce minted for this request, or "".
//
// A page renders it into every inline script it carries. An empty one is not
// an error: it means no policy was set for this request, and an inline script
// then runs because nothing forbade it.
func NonceFrom(ctx context.Context) string {
	nonce, _ := ctx.Value(nonceKey).(string)
	return nonce
}

// nonceBytes is how much randomness each one carries. Sixteen is the amount
// the specification recommends, and the point is that it cannot be guessed by
// whoever is trying to inject the script it would authorise.
const nonceBytes = 16

// policy is everything this site is allowed to load, with one slot for the
// nonce.
//
// `default-src 'none'` and then the exceptions, so anything a future page
// reaches for is refused until somebody writes it down here — the direction
// that fails safe. The notable entries:
//
//   - **script-src is 'self' and a nonce, with no CDN.** That is what the map
//     libraries were vendored for; see static/third-party/leaflet/PROVENANCE.md.
//   - **img-src allows data: and the tile server.** A device-linking QR code is
//     a data URI on purpose — the token is in it, and an image URL carrying a
//     credential is one every cache on the way writes down — and the map is
//     made of tiles.
//   - **connect-src is 'self'.** The passkey and push flows post to this
//     origin, which forwards them; nothing in a page talks to the backend.
//   - **form-action 'self'** so an injected form cannot post somewhere else,
//     and **frame-ancestors 'none'** so no other site can frame this one and
//     collect a click meant for us.
//   - **base-uri 'none'**, because a `<base>` tag would otherwise re-point
//     every relative URL on the page, including the scripts.
const policy = "default-src 'none'; " +
	"script-src 'self' 'nonce-%s'; " +
	"style-src 'self'; " +
	"img-src 'self' data: https://tile.openstreetmap.org; " +
	"font-src 'self'; " +
	"connect-src 'self'; " +
	// Spelled out rather than left to the fallback chain. A worker source is
	// meant to fall back to child-src and then to script-src, and browsers
	// have not always agreed about that — a service worker that silently
	// fails to register is a notification nobody receives and nothing to see.
	"worker-src 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'"

// SecurityHeaders sets the Content-Security-Policy and the headers that go
// with it, and mints the nonce the page will use.
//
// A handler that has already set its own policy keeps it. That is not a
// courtesy: an uploaded SVG is served from this origin with
// `default-src 'none'; sandbox`, which is what stops an image upload being
// code execution, and a blanket header overwriting it would quietly undo that.
func SecurityHeaders() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := make([]byte, nonceBytes)
			if _, err := rand.Read(raw); err != nil {
				// Without randomness there is no nonce worth having, and a
				// guessable one is worse than none: it would authorise exactly
				// the script it is meant to refuse. The page is served with no
				// policy instead, which is where this site already was.
				next.ServeHTTP(w, r)
				return
			}
			// URL-safe base64, which is a deliberate choice rather than a
			// default. Standard base64 contains `+` and `/`, and `html/template`
			// escapes `+` to `&#43;` in an attribute — a browser decodes it back
			// and the match still holds, so it *works*, but only on the
			// responses whose random bytes happened to avoid the character.
			//
			// A security control that is intermittently right for a reason two
			// layers away is one nobody can debug. The alphabet here produces
			// nothing a template will touch.
			nonce := base64.RawURLEncoding.EncodeToString(raw)

			if w.Header().Get("Content-Security-Policy") == "" {
				w.Header().Set("Content-Security-Policy",
					strings.Replace(policy, "%s", nonce, 1))
			}

			// Not part of the policy, and cheap enough to set beside it.
			//
			// nosniff stops a browser deciding for itself that something we
			// served as text is script. Referrer-Policy keeps the address of
			// the page somebody is reading off every outbound request — on a
			// register of grievances, the URL of what you were reading is the
			// thing least worth leaking.
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "same-origin")

			next.ServeHTTP(w, r.WithContext(
				context.WithValue(r.Context(), nonceKey, nonce)))
		})
	}
}
