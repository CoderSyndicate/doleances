package geocode

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// throttler is a provider that answers 429 until it is told to stop.
type throttler struct {
	stub
	refuse     bool
	retryAfter time.Duration
}

func (t *throttler) Reverse(ctx context.Context, lat, lng float64, precision uint) (Place, error) {
	if !t.refuse {
		return t.stub.Reverse(ctx, lat, lng, precision)
	}
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	return Place{}, &throttled{
		provider:   t.name,
		status:     "429 Too Many Requests",
		retryAfter: t.retryAfter,
	}
}

// TestFiveMinutesOfRefusalsDrainsAProvider.
//
// This is the hole the in-memory count cannot cover. A backend that restarts
// begins the day's allowance again from zero, so it will happily spend a quota
// it has already spent — and the only thing that knows better is the provider,
// which answers 429. A single one is congestion. Five minutes of them, with no
// answer in between, is a quota that is gone, and the queue has to stop asking
// and go elsewhere.
//
// Driven by the clock rather than by waiting, which is the only way this
// assertion exists at all.
func TestFiveMinutesOfRefusalsDrainsAProvider(t *testing.T) {
	provider := &throttler{stub: stub{name: "p", rate: 1000}, refuse: true}
	service := NewService(map[string]Limits{"p": {PerSecond: 1000, PerDay: 86400}}, provider)

	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	service.budgets[0].now = func() time.Time { return now }

	// The first refusal is congestion: backed off, not written off.
	if _, err := service.Reverse(context.Background(), 48.85, 2.35, 5); !errors.Is(err, ErrThrottled) {
		t.Fatalf("first refusal reported %v, want ErrThrottled", err)
	}
	if left := service.budgets[0].remaining(); left == 0 {
		t.Fatal("one 429 wrote the provider off for the day; it is a pause, not a verdict")
	}

	// Then five minutes of the same, each attempt made once its backoff has
	// elapsed — which is what a caller doing ordinary work would produce.
	for attempt := 0; attempt < 20 && service.budgets[0].remaining() != 0; attempt++ {
		now = now.Add(70 * time.Second)
		_, _ = service.Reverse(context.Background(),
			48.0+float64(attempt)*0.5, 2.0+float64(attempt)*0.5, 5)
	}

	if left := service.budgets[0].remaining(); left != 0 {
		t.Fatalf("after five minutes of 429s the provider has %d left; want it drained", left)
	}

	// And a drained provider is skipped rather than asked again.
	before := provider.count()
	_, err := service.Reverse(context.Background(), 45.75, 4.85, 5)
	if !errors.Is(err, ErrDayExhausted) {
		t.Errorf("a drained provider answered %v, want ErrDayExhausted", err)
	}
	if provider.count() != before {
		t.Error("a drained provider was called again")
	}
}

// TestAnAnswerBetweenRefusalsIsNotADrainedQuota.
//
// The five minutes measure a provider that never recovers. One that answers in
// between was busy, and treating that as a spent quota would take a working
// provider out of the queue for the rest of the day over a bad minute.
func TestAnAnswerBetweenRefusalsIsNotADrainedQuota(t *testing.T) {
	provider := &throttler{stub: stub{name: "p", rate: 1000}, refuse: true}
	service := NewService(map[string]Limits{"p": {PerSecond: 1000, PerDay: 86400}}, provider)

	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	service.budgets[0].now = func() time.Time { return now }

	// Four minutes of refusals.
	for i := 0; i < 4; i++ {
		_, _ = service.Reverse(context.Background(), 48.0+float64(i)*0.5, 2.0+float64(i)*0.5, 5)
		now = now.Add(time.Minute)
	}

	// One answer, which is what resets the clock.
	provider.refuse = false
	if _, err := service.Reverse(context.Background(), 43.30, -1.78, 5); err != nil {
		t.Fatalf("the provider answered and the queue reported %v", err)
	}

	// Then four minutes more. Eight minutes have passed and the longest run
	// without an answer is four, so nothing is drained.
	provider.refuse = true
	for i := 0; i < 4; i++ {
		_, _ = service.Reverse(context.Background(), 50.0+float64(i)*0.5, 3.0+float64(i)*0.5, 5)
		now = now.Add(time.Minute)
	}

	if left := service.budgets[0].remaining(); left == 0 {
		t.Error("a provider that answered in between was written off; the five minutes are consecutive")
	}
}

// TestTheBackoffGrowsAndIsCappedAtAMinute.
//
// Retrying a refusal at the same pace is how a client that was asked to slow
// down becomes the reason it was asked. The pause doubles, and it stops
// doubling at a minute because past that the question is no longer whether to
// retry.
func TestTheBackoffGrowsAndIsCappedAtAMinute(t *testing.T) {
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	budget := newBudget(Limits{PerSecond: 1, PerDay: 86400}, Limits{})
	budget.now = func() time.Time { return now }

	want := []time.Duration{
		5 * time.Second,
		10 * time.Second,
		20 * time.Second,
		40 * time.Second,
		time.Minute,
		time.Minute,
	}
	for i, pause := range want {
		budget.refused(0)
		if got := budget.retryAfter.Sub(now); got != pause {
			t.Errorf("refusal %d paused %v, want %v", i+1, got, pause)
		}
		now = now.Add(pause)
	}
}

// TestTheProvidersOwnRetryAfterOutranksTheBackoff.
//
// A service that says how long to wait knows something no backoff computed
// here does, so it is believed over the doubling.
func TestTheProvidersOwnRetryAfterOutranksTheBackoff(t *testing.T) {
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	budget := newBudget(Limits{PerSecond: 1, PerDay: 86400}, Limits{})
	budget.now = func() time.Time { return now }

	budget.refused(90 * time.Second)
	if got := budget.retryAfter.Sub(now); got != 90*time.Second {
		t.Errorf("waited %v, want the 90s the provider asked for", got)
	}
}

// TestADrainedProviderRecoversOvernight.
//
// Five minutes of 429s is a verdict about today, not about the provider. A new
// day is a new quota, so it has to be tried again rather than written off
// until somebody restarts the backend.
func TestADrainedProviderRecoversOvernight(t *testing.T) {
	provider := &throttler{stub: stub{name: "p", rate: 1000}, refuse: true}
	service := NewService(map[string]Limits{"p": {PerSecond: 1000, PerDay: 86400}}, provider)

	today := time.Date(2026, 10, 5, 23, 0, 0, 0, time.UTC)
	now := today
	service.budgets[0].now = func() time.Time { return now }

	for attempt := 0; attempt < 20 && service.budgets[0].remaining() != 0; attempt++ {
		_, _ = service.Reverse(context.Background(),
			48.0+float64(attempt)*0.5, 2.0+float64(attempt)*0.5, 5)
		now = now.Add(70 * time.Second)
	}
	if service.budgets[0].remaining() != 0 {
		t.Fatal("the provider was never drained, so there is nothing to recover")
	}

	now = today.Add(2 * time.Hour)
	provider.refuse = false
	if _, err := service.Reverse(context.Background(), 45.75, 4.85, 5); err != nil {
		t.Errorf("a drained provider did not come back with the new day: %v", err)
	}
}

// TestTheRefusalDoesNotStopTheOtherProviders.
//
// The whole point of a queue over several providers: one of them hitting its
// limit is not the feature hitting its limit.
func TestTheRefusalDoesNotStopTheOtherProviders(t *testing.T) {
	refusing := &throttler{stub: stub{name: "refusing", rate: 1000}, refuse: true}
	answering := &stub{name: "answering", rate: 1000}
	service := NewService(nil, refusing, answering)

	place, err := service.Reverse(context.Background(), 48.85, 2.35, 5)
	if err != nil {
		t.Fatalf("a 429 at one provider failed the lookup: %v", err)
	}
	if place.Label != "answering" {
		t.Errorf("answered by %q, want the provider that was not refusing", place.Label)
	}
}

// TestRetryAfterIsReadInBothItsForms.
//
// The header is specified as either a number of seconds or an HTTP date, and
// services send both. An unreadable one is absent rather than an error: the
// header exists to be more polite than the default, and failing over a
// malformed one would be less.
func TestRetryAfterIsReadInBothItsForms(t *testing.T) {
	if got := retryAfter("30"); got != 30*time.Second {
		t.Errorf("seconds gave %v, want 30s", got)
	}
	if got := retryAfter("  12 "); got != 12*time.Second {
		t.Errorf("padded seconds gave %v, want 12s", got)
	}
	if got := retryAfter(""); got != 0 {
		t.Errorf("an absent header gave %v, want 0", got)
	}
	if got := retryAfter("soon please"); got != 0 {
		t.Errorf("nonsense gave %v, want 0", got)
	}
	if got := retryAfter("0"); got != 0 {
		t.Errorf("zero seconds gave %v, want 0", got)
	}

	// A date, which is the other half of the specification — and one in the
	// past is a clock that disagrees with ours rather than an invitation.
	soon := time.Now().UTC().Add(time.Minute).Format(http.TimeFormat)
	if got := retryAfter(soon); got < 50*time.Second || got > time.Minute {
		t.Errorf("an HTTP date a minute away gave %v", got)
	}
	past := time.Now().UTC().Add(-time.Hour).Format(http.TimeFormat)
	if got := retryAfter(past); got != 0 {
		t.Errorf("a date in the past gave %v, want 0", got)
	}
}

// TestBothProvidersReportA429AsItself.
//
// The distinction only exists if the providers draw it. A 429 reported as an
// ordinary failure would be retried at once, which is how a client that was
// asked to slow down gets banned.
func TestBothProvidersReportA429AsItself(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	providers := []Provider{
		NewNominatim(server.URL, "doleances/test"),
		NewGeoapify(server.URL, "a-key"),
	}
	for _, provider := range providers {
		_, err := provider.Reverse(context.Background(), 43.3883, -1.6626, 5)
		if !errors.Is(err, ErrThrottled) {
			t.Errorf("%s reported %v, want ErrThrottled", provider.Name(), err)
		}
		if got := retryAfterOf(err); got != 42*time.Second {
			t.Errorf("%s carried a Retry-After of %v, want 42s", provider.Name(), got)
		}
	}
}

// TestAnOrdinaryFailureIsNotAThrottling.
//
// The other direction, which is the one that would quietly drain a provider
// over an outage: a 500 is a service having a bad minute and says nothing
// about anybody's quota.
func TestAnOrdinaryFailureIsNotAThrottling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := NewNominatim(server.URL, "doleances/test").Reverse(
		context.Background(), 43.3883, -1.6626, 5)
	if err == nil {
		t.Fatal("a 500 was swallowed")
	}
	if errors.Is(err, ErrThrottled) {
		t.Error("a 500 was reported as rate limiting; five minutes of an outage would drain the provider")
	}
}
