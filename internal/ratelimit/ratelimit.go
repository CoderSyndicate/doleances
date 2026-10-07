// Package ratelimit bounds how often one caller may do something.
//
// It exists because of what passkeys cost this register. An account used to
// require a working mailbox, which is a slow thing to acquire in bulk; an
// account now requires a fingerprint and a second, so the only barriers left
// between a script and ten thousand accounts are the curation queue and this.
//
// It is deliberately small and in-memory. A limiter shared across instances
// needs a store, a store needs a deployment decision, and a deployment
// decision is how "there is no rate limiting at all" survives for another six
// months. One process bounding itself is most of the value; the note in the
// plan says what would replace it.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter allows a burst and then a steady trickle, per key.
//
// A burst rather than a flat rate because the honest case is bursty: somebody
// registering a passkey retries after a cancelled prompt, and a limit that
// refused the second attempt would be a limit that only ever hurt real people.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	// burst is how many are allowed at once, refill how fast one comes back.
	burst  float64
	refill time.Duration

	// idle is how long an untouched bucket is kept. Without it the map is an
	// unbounded record of every address that ever called — which is both a
	// leak and a list this project should not hold.
	idle time.Duration

	now func() time.Time
}

type bucket struct {
	tokens float64
	seen   time.Time
}

// New builds a limiter allowing burst attempts, with one returning every
// refill interval.
func New(burst int, refill time.Duration) *Limiter {
	if burst < 1 {
		burst = 1
	}
	if refill <= 0 {
		refill = time.Minute
	}
	return &Limiter{
		buckets: map[string]*bucket{},
		burst:   float64(burst),
		refill:  refill,
		idle:    10 * refill,
		now:     time.Now,
	}
}

// Allow reports whether this key may act now, and spends an attempt if so.
func (l *Limiter) Allow(key string) bool {
	if key == "" {
		// A caller we cannot tell apart from any other is not one we can
		// limit, and refusing everybody because one address was unreadable
		// would be worse than not limiting.
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweep(now)

	held, known := l.buckets[key]
	if !known {
		held = &bucket{tokens: l.burst, seen: now}
		l.buckets[key] = held
	} else {
		held.tokens += now.Sub(held.seen).Seconds() / l.refill.Seconds()
		if held.tokens > l.burst {
			held.tokens = l.burst
		}
		held.seen = now
	}

	if held.tokens < 1 {
		return false
	}
	held.tokens--
	return true
}

// sweep forgets keys nobody has used, which is both housekeeping and the
// reason this holds no lasting record of who called.
func (l *Limiter) sweep(now time.Time) {
	for key, held := range l.buckets {
		if now.Sub(held.seen) > l.idle {
			delete(l.buckets, key)
		}
	}
}
