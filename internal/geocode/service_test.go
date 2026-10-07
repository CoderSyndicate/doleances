package geocode

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// stub is a provider that answers instantly and counts its calls.
type stub struct {
	name  string
	rate  float64
	fail  bool
	mu    sync.Mutex
	calls int
}

func (s *stub) Name() string     { return s.name }
func (s *stub) Defaults() Limits { return Limits{PerSecond: s.rate} }
func (s *stub) Reverse(context.Context, float64, float64, uint) (Place, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.fail {
		return Place{}, errors.New("refused")
	}
	return Place{Label: s.name, CountryCode: "FR"}, nil
}

func (s *stub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestTheQueueSpansEveryProvider.
//
// The point of a second provider is not a spare: it is capacity. Nominatim
// allows one request a second, which is the ceiling for everybody as long as
// it is the only one — so the queue hands each lookup to whichever provider is
// free soonest, and two providers deliver the sum of their rates rather than
// the larger.
//
// Twelve distinct cells against 1/s and 5/s should take about two seconds. On
// one provider alone the same work is eleven.
func TestTheQueueSpansEveryProvider(t *testing.T) {
	slow := &stub{name: "slow", rate: 1}
	fast := &stub{name: "fast", rate: 5}
	service := NewService(nil, slow, fast)

	started := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Distinct points, or the cache would answer and nothing would be
			// paced at all.
			if _, err := service.Reverse(context.Background(),
				48.0+float64(i)*0.5, 2.0+float64(i)*0.5, 5); err != nil {
				t.Errorf("lookup %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	took := time.Since(started)

	if took > 4*time.Second {
		t.Errorf("12 lookups took %v: the providers are not sharing the queue", took)
	}
	if fast.count() <= slow.count() {
		t.Errorf("slow served %d and fast %d: the queue is not following the rates",
			slow.count(), fast.count())
	}
	if slow.count()+fast.count() != 12 {
		t.Errorf("%d calls for 12 lookups", slow.count()+fast.count())
	}
}

// TestAFailingProviderHandsTheLookupOn.
//
// An outage at one service is not an outage of the feature. Only when every
// provider has refused does a lookup fail — and even then the caller records
// the doléance unnamed, because losing somebody's text to a third party being
// down would be indefensible.
func TestAFailingProviderHandsTheLookupOn(t *testing.T) {
	broken := &stub{name: "broken", rate: 5, fail: true}
	working := &stub{name: "working", rate: 1}

	place, err := NewService(nil, broken, working).Reverse(context.Background(), 48.85, 2.35, 5)
	if err != nil {
		t.Fatalf("a working provider was not reached: %v", err)
	}
	if place.Label != "working" {
		t.Errorf("label = %q, want the second provider's answer", place.Label)
	}
	if broken.count() != 1 {
		t.Errorf("the broken provider was called %d times", broken.count())
	}

	// And when all of them refuse, the error says so rather than inventing a
	// place.
	alsoBroken := &stub{name: "also-broken", rate: 5, fail: true}
	if _, err := NewService(nil, broken, alsoBroken).Reverse(
		context.Background(), 48.85, 2.35, 5); err == nil {
		t.Error("every provider refused and the lookup still succeeded")
	}
}

// TestTheCacheIsSharedAcrossProviders.
//
// The cache is keyed on the cell, above the providers, so a town looked up
// through one is not looked up again through the other — and switching
// provider never changes what a cached cell is called.
func TestTheCacheIsSharedAcrossProviders(t *testing.T) {
	first := &stub{name: "first", rate: 100}
	second := &stub{name: "second", rate: 100}
	service := NewService(nil, first, second)

	for i := 0; i < 5; i++ {
		if _, err := service.Reverse(context.Background(), 48.8566, 2.3522, 5); err != nil {
			t.Fatal(err)
		}
	}
	if total := first.count() + second.count(); total != 1 {
		t.Errorf("%d calls for one cell asked five times", total)
	}
}

// TestNoProviderIsAnAnswerRatherThanAPanic.
func TestNoProviderIsAnAnswerRatherThanAPanic(t *testing.T) {
	// NewGeoapify returns nil without a key, and a nil provider is skipped
	// rather than stored — otherwise the queue would call it.
	service := NewService(nil, NewGeoapify("", ""))
	if got := service.Providers(); len(got) != 0 {
		t.Errorf("providers = %v, want none", got)
	}
	if _, err := service.Reverse(context.Background(), 48.85, 2.35, 5); !errors.Is(err, ErrNoProvider) {
		t.Errorf("err = %v, want ErrNoProvider", err)
	}
}

// TestADailyAllowanceIsSpentAndThenSkipped.
//
// The per-second rate is about not being banned; the per-day allowance is
// about not being billed. A provider whose day is gone is **skipped**, not
// failed: the lookup falls to whoever has budget left, which is the whole
// reason a deployment pays for one provider and keeps another.
func TestADailyAllowanceIsSpentAndThenSkipped(t *testing.T) {
	metered := &stub{name: "metered", rate: 100}
	unmetered := &stub{name: "unmetered", rate: 100}
	service := NewService(map[string]Limits{
		"metered": {PerSecond: 100, PerDay: 3},
	}, metered, unmetered)

	for i := 0; i < 10; i++ {
		if _, err := service.Reverse(context.Background(),
			48.0+float64(i)*0.5, 2.0+float64(i)*0.5, 5); err != nil {
			t.Fatalf("lookup %d: %v", i, err)
		}
	}

	if metered.count() != 3 {
		t.Errorf("the metered provider served %d, want its allowance of 3", metered.count())
	}
	if unmetered.count() != 7 {
		t.Errorf("the unmetered provider served %d, want the remaining 7", unmetered.count())
	}
	if left := service.Remaining()["metered"]; left != 0 {
		t.Errorf("remaining = %d, want nothing left", left)
	}
	if left := service.Remaining()["unmetered"]; left != -1 {
		t.Errorf("remaining = %d, want -1 for unmetered", left)
	}
}

// TestEveryAllowanceSpentIsAnAnswerRatherThanASilence.
//
// When nothing is left anywhere the lookup fails, and it fails with a reason
// an operator can act on — raise a plan, add a provider — rather than a
// generic refusal. The caller records the doléance unnamed either way: a place
// name is a nicety and losing somebody's text to a spent quota would be
// indefensible.
func TestEveryAllowanceSpentIsAnAnswerRatherThanASilence(t *testing.T) {
	only := &stub{name: "only", rate: 100}
	service := NewService(map[string]Limits{"only": {PerSecond: 100, PerDay: 1}}, only)

	if _, err := service.Reverse(context.Background(), 48.85, 2.35, 5); err != nil {
		t.Fatalf("the first lookup should have been served: %v", err)
	}
	_, err := service.Reverse(context.Background(), 43.6, 1.44, 5)
	if !errors.Is(err, ErrDayExhausted) {
		t.Errorf("err = %v, want ErrDayExhausted", err)
	}
	if only.count() != 1 {
		t.Errorf("the provider was called %d times for an allowance of 1", only.count())
	}
}

// TestTheAllowanceComesBackTomorrow.
//
// Tested with a clock rather than by waiting, which is the only way this
// assertion exists at all — and the rollover is the half of a daily limit that
// is never exercised in development, where nobody reaches the cap.
func TestTheAllowanceComesBackTomorrow(t *testing.T) {
	provider := &stub{name: "p", rate: 100}
	service := NewService(map[string]Limits{"p": {PerSecond: 100, PerDay: 2}}, provider)

	today := time.Date(2026, 10, 5, 23, 0, 0, 0, time.UTC)
	service.budgets[0].now = func() time.Time { return today }

	for i := 0; i < 4; i++ {
		_, _ = service.Reverse(context.Background(),
			48.0+float64(i)*0.5, 2.0+float64(i)*0.5, 5)
	}
	if provider.count() != 2 {
		t.Fatalf("served %d today, want the allowance of 2", provider.count())
	}

	service.budgets[0].now = func() time.Time { return today.Add(2 * time.Hour) }
	if _, err := service.Reverse(context.Background(), 45.75, 4.85, 5); err != nil {
		t.Errorf("the allowance did not reset overnight: %v", err)
	}
	if provider.count() != 3 {
		t.Errorf("served %d after the day turned, want one more", provider.count())
	}
}
