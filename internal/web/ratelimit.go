package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CoderSyndicate/doleances/internal/ratelimit"
)

// Bounds is what one address may do in a minute, by what it costs.
//
// Three numbers rather than one, because the three are not the same request.
// An asset is read out of the binary and costs a memcpy; a page fans out to
// several backend calls and a database behind them; a write changes something
// and, for a submission, eventually costs a model call. An operator tuning one
// of those should not have to move the others.
type Bounds struct {
	// Assets is the embedded files and the theme overlay. High, because a
	// browser pulls a dozen per page and a reader must never spend their
	// allowance on a stylesheet.
	Assets int

	// Pages is everything that reads through to the backend.
	Pages int

	// Writes is everything that changes something.
	Writes int
}

// DefaultBounds is deliberately generous, and the reason is who reads this
// register.
//
// # Why an address is a poor name for a reader
//
// A village shares one address. So does a workplace, a library, a school, and
// so does a mobile carrier's whole CGNAT pool — which can be tens of thousands
// of people on one IPv4 address. This register exists for exactly the rural and
// peripheral places where that is most true, so a limit tight enough to stop a
// determined script is a limit that refuses a commune, and refusing a commune
// is a worse outcome than serving an attacker.
//
// # So what is this actually for
//
// Two things, honestly stated, and neither is "stopping a distributed attack".
//
// It stops one client taking the site down by accident — a broken loop, a
// crawler with no delay, a retry storm — which is the failure that actually
// happens. And it bounds **writes**, where the numbers are at least a
// different shape: a person writes one doléance, joins one group and presses
// "me too" a handful of times.
//
// # Where the write number comes from
//
// Not from what a flooder would want, because at any value a crowd behind one
// NAT can live with, a flooder with a generator is still comfortable. It comes
// from the other end: the busiest honest minute one address can produce. A "me
// too" is a form post, so a dozen people reading the register together on a
// day it is in the news is already in the dozens of writes a minute, and
// refusing them would take away the register's central reading gesture from a
// whole village.
//
// What actually stands between a script and ten thousand accounts is the
// **ceremony limiter in the backend** — ten at once and ten a minute per
// address, on beginning a signup — and the curation queue behind it. This
// bound is not that, and is not a substitute for it.
//
// Anything beyond belongs in front of this service, or in the proof-of-work
// this project has already named as the next step.
var DefaultBounds = Bounds{Assets: 3000, Pages: 600, Writes: 120}

// assetPaths are served without a database behind them.
//
// `/theme/` is in here although it does reach the backend, because it has a
// cache of its own and a reader pulls it on every page exactly like a
// stylesheet — which is what it is.
var assetPaths = []string{"/static/", "/theme/", "/sw.js", "/favicon.ico"}

// RateLimited bounds every route by the address it came from.
//
// Service-level middleware rather than a decoration on the handlers that
// looked risky: the route somebody forgets to bound is always the one that
// needed it, and a limit declared per handler is a limit the next handler is
// copied without. Everything is bounded and the classification decides how
// generously.
//
// A bound of zero or less switches that class off rather than refusing
// everything, so an operator cannot take their own site down with a typo.
func RateLimited(bounds Bounds, proxies TrustedProxies) func(http.Handler) http.Handler {
	assets := perMinute(bounds.Assets)
	pages := perMinute(bounds.Pages)
	writes := perMinute(bounds.Writes)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var class class
			switch {
			case r.Method != http.MethodGet && r.Method != http.MethodHead:
				// Checked first: a POST to an asset path is not an asset.
				class = writes
			case isAssetPath(r.URL.Path):
				class = assets
			default:
				class = pages
			}

			if class.limiter != nil && !class.limiter.Allow(proxies.ClientAddress(r)) {
				// Plain text and a Retry-After rather than a rendered page.
				// This runs before language resolution, and a translated
				// apology is not worth coupling the whole middleware chain to
				// the renderer for — every proxy and every browser already
				// understands this answer.
				w.Header().Set("Retry-After", strconv.Itoa(class.retryAfter))
				http.Error(w, "too many requests from here; slow down and try again",
					http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// class is one bucket and what to tell somebody who emptied it.
type class struct {
	limiter    *ratelimit.Limiter
	retryAfter int
}

// perMinute builds a bucket holding a minute's worth, refilling steadily.
//
// A burst rather than a flat rate because the honest case is bursty — a page
// pulls its assets all at once, somebody reading the register presses "me too"
// on several cards — and a flat rate would refuse exactly that while letting a
// patient script straight through.
func perMinute(allowance int) class {
	if allowance <= 0 {
		// Off. A bound of zero switches a class off rather than refusing
		// everything, so an operator cannot take their own site down with a
		// typo in a number.
		return class{}
	}

	refill := time.Minute / time.Duration(allowance)
	// Rounded up and never zero: a Retry-After of 0 says "now", which is what
	// the caller just tried.
	seconds := int((refill + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return class{limiter: ratelimit.New(allowance, refill), retryAfter: seconds}
}

func isAssetPath(path string) bool {
	for _, prefix := range assetPaths {
		if path == prefix || strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
