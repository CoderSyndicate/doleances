package geocode

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Limits are what one provider's plan allows.
//
// Both numbers are configuration rather than constants, because both are
// bought: Geoapify's free tier is 5 a second and 3,000 a day, and every paid
// tier moves both. A deployment that upgraded its plan and could not say so
// would be paying for capacity this queue refuses to use.
//
// A zero in either field means "the provider's own default", so a deployment
// that configured nothing still gets the free tier's limits rather than none.
type Limits struct {
	// PerSecond is the burst rate. Exceeding it is not slowness, it is a ban.
	PerSecond float64

	// PerDay is the daily allowance. Zero means unmetered — which is right
	// for a Nominatim instance the operator runs themselves, and wrong for
	// anything billed.
	PerDay int
}

// How a provider saying "too many" is handled.
const (
	// firstBackoff is the pause after one 429. It doubles from there.
	firstBackoff = 5 * time.Second

	// maxBackoff caps it: past a minute, retrying is no longer the question.
	maxBackoff = time.Minute

	// drainAfter is how long a provider may answer 429 before this stops
	// treating it as congestion and starts treating it as a spent quota.
	//
	// Five minutes, because the two look identical from here and only time
	// tells them apart: a burst clears in seconds, and a day's allowance does
	// not clear at all. Waiting longer means hammering a service that has
	// already said no; waiting less would drop a provider over a blip.
	drainAfter = 5 * time.Minute
)

// budget is one provider's place in the queue: its pace, what is left of its
// day, and whether the provider is currently refusing.
type budget struct {
	interval time.Duration
	perDay   int

	// now is swappable so the daily rollover can be tested without waiting
	// until tomorrow.
	now func() time.Time

	mu    sync.Mutex
	next  time.Time
	day   time.Time
	spent int

	// Refusal state. throttledSince is when the provider first said 429
	// without a success in between; retryAfter is when to try it again;
	// backoff is the pause that produced it. drained says this provider is
	// done for the day whatever the count says.
	throttledSince time.Time
	retryAfter     time.Time
	backoff        time.Duration
	drained        bool
}

func newBudget(limits Limits, fallback Limits) *budget {
	perSecond := limits.PerSecond
	if perSecond <= 0 {
		perSecond = fallback.PerSecond
	}
	if perSecond <= 0 {
		perSecond = 1
	}
	perDay := limits.PerDay
	if perDay == 0 {
		perDay = fallback.PerDay
	}
	return &budget{
		interval: time.Duration(float64(time.Second) / perSecond),
		perDay:   perDay,
		now:      time.Now,
	}
}

// reserve claims the next slot, or reports that the day is spent.
//
// Claiming before waiting is what makes two callers queue rather than collide:
// the second is handed a slot one interval after the first, whether or not the
// first has run yet.
func (b *budget) reserve() (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	b.rollover(now)
	if b.exhausted() || now.Before(b.retryAfter) {
		return time.Time{}, false
	}
	b.spent++

	slot := b.next
	if slot.Before(now) {
		slot = now
	}
	b.next = slot.Add(b.interval)
	return slot, true
}

// peek says when the next caller would be served, and whether there is any day
// left. It claims nothing.
//
// It returns the raw instant, which may be in the past for a provider that is
// free. Clamping it to "now" instead looks tidier and is wrong: two idle
// providers would each answer with the moment they were asked, so comparing
// them returns whichever was asked about first, and a sort built on that swaps
// its own operands. The first version did exactly that and chose the wrong
// provider.
func (b *budget) peek() (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	b.rollover(now)
	if b.exhausted() {
		return b.next, false
	}
	if now.Before(b.retryAfter) {
		// Still available, just not yet: sorting by this is what sends the
		// next lookup to a provider that is answering.
		return b.retryAfter, true
	}
	return b.next, true
}

// spentForToday is exhausted asked from outside, under the lock.
//
// The queue needs it to tell two refusals apart: a provider in a few seconds'
// backoff is coming back, and one whose day is gone is not. Without it both
// would be reported as a spent allowance, which is the message an operator
// would act on by buying a bigger plan they did not need.
func (b *budget) spentForToday() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.rollover(b.now())
	return b.exhausted()
}

// exhausted says this provider will serve nothing more today — either because
// the count says so, or because it spent five minutes telling us so itself.
func (b *budget) exhausted() bool {
	return b.drained || (b.perDay > 0 && b.spent >= b.perDay)
}

// refused records a provider answering "too many requests".
//
// # Why a 429 is believed over the count
//
// The count is this process's guess and it starts at zero on every restart, so
// a backend that crash-loops could spend its day's allowance several times and
// know nothing about it. A 429 is the provider's own answer, and it is right
// across restarts, across replicas, and across whatever else is spending the
// same key.
//
// So a single one is treated as congestion — back off and try again — and a
// run of them lasting drainAfter is treated as the quota being gone. The
// distinction cannot be made any other way: the two look identical in a single
// response, and only persistence separates them.
//
// retryAfter is the provider's own Retry-After when it sent one, which is
// better information than any backoff computed here.
func (b *budget) refused(retryAfter time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	b.rollover(now)

	if b.throttledSince.IsZero() {
		b.throttledSince = now
		b.backoff = firstBackoff
	} else if b.backoff < maxBackoff {
		b.backoff *= 2
		if b.backoff > maxBackoff {
			b.backoff = maxBackoff
		}
	}

	pause := b.backoff
	if retryAfter > 0 {
		pause = retryAfter
	}
	b.retryAfter = now.Add(pause)

	if now.Sub(b.throttledSince) >= drainAfter {
		b.drained = true
	}
}

// served records a provider answering, which is what ends a backoff.
func (b *budget) served() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.throttledSince = time.Time{}
	b.retryAfter = time.Time{}
	b.backoff = 0
}

// remaining is what is left of the day, for the log line an operator reads
// when labels stop appearing.
func (b *budget) remaining() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.rollover(b.now())
	if b.drained {
		return 0
	}
	if b.perDay <= 0 {
		return -1 // unmetered
	}
	return b.perDay - b.spent
}

// rollover resets the count when the day turns.
//
// UTC midnight, which is an assumption rather than a fact: a provider resets
// on its own schedule and may do so on a billing anniversary instead. Being
// wrong by a few hours costs a provider being skipped slightly too long, which
// the other providers absorb — the opposite mistake would be a ban.
//
// **The count does not survive a restart**, and it is not the only thing
// counting. A backend that crash-loops starts every day's allowance again from
// zero, and persisting the number means a store dependency in a package that
// has none — so the count bounds ordinary running and nothing else.
//
// What covers the rest is the provider's own answer. A restart cannot make a
// spent quota unspent at the far end, so the requests come back 429, and five
// minutes of that drains the budget here whatever its count says. The count is
// how this stays inside a limit; the 429 is how it finds out it did not.
func (b *budget) rollover(now time.Time) {
	today := now.UTC().Truncate(24 * time.Hour)
	if b.day.Equal(today) {
		return
	}
	b.day = today
	b.spent = 0
	// A new day is a new quota, so a provider drained by yesterday's 429s is
	// tried again rather than written off until a restart.
	b.drained = false
	b.throttledSince = time.Time{}
	b.retryAfter = time.Time{}
	b.backoff = 0
}

// wait sleeps until a slot, or until the caller gives up.
//
// It measures the gap with the budget's own clock rather than time.Now, and
// that is not a testing nicety: a budget whose clock is injected would
// otherwise hand out a slot against one clock and sleep against another. The
// first version did exactly that, and a test that moved the clock to this
// evening asked a unit test to sleep for nine hours.
func (b *budget) wait(ctx context.Context, slot time.Time) time.Duration {
	gap := slot.Sub(b.now())
	if gap <= 0 {
		return 0
	}
	timer := time.NewTimer(gap)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
	return gap
}

// ErrThrottled is a provider answering "too many requests".
//
// It is a sentinel rather than one error among many because the queue acts on
// it: an ordinary failure is a provider having a bad second, and this is a
// provider telling us we are over its limit. See budget.refused.
var ErrThrottled = errors.New("geocode: provider is rate limiting")

// throttled is ErrThrottled carrying the provider's own Retry-After.
type throttled struct {
	provider   string
	status     string
	retryAfter time.Duration
}

func (t *throttled) Error() string {
	said := "geocode: " + t.provider + " answered " + t.status
	if t.retryAfter > 0 {
		said += ", retry after " + t.retryAfter.String()
	}
	return said
}

// Is makes errors.Is(err, ErrThrottled) answer for this.
func (t *throttled) Is(target error) bool { return target == ErrThrottled }

// rateLimited reads a 429 off a response, or returns nil.
//
// Retry-After is honoured where it is sent, because a provider saying how long
// to wait knows better than any backoff computed here. Both forms the header
// takes are read — a number of seconds, and an HTTP date — and an unreadable
// one is simply absent rather than an error: the point of the header is to be
// more polite than the default, and failing over a malformed one would be less.
func rateLimited(name string, resp *http.Response) error {
	if resp.StatusCode != http.StatusTooManyRequests {
		return nil
	}
	return &throttled{
		provider:   name,
		status:     resp.Status,
		retryAfter: retryAfter(resp.Header.Get("Retry-After")),
	}
}

func retryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(header); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(header)
	if err != nil {
		return 0
	}
	// A date in the past is a clock that disagrees with ours, not an
	// instruction to hammer the service.
	if wait := time.Until(when); wait > 0 {
		return wait
	}
	return 0
}
