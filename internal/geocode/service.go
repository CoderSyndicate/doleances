package geocode

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/geo"
)

// Provider is one reverse-geocoding service.
//
// Each knows its own name, the rate it tolerates, and how to turn a point into
// a place. Everything shared — the cache, the queue, the choice between them —
// belongs to the Service above, so adding a third is one file and no changes
// anywhere else.
type Provider interface {
	// Name identifies it in logs and configuration.
	Name() string

	// Defaults are the limits of the provider's free or published tier, used
	// when a deployment configured none. They are defaults rather than facts:
	// both numbers move with the plan, which is why Limits is configuration.
	Defaults() Limits

	// Reverse names the place a point falls in, at the requested granularity.
	// It does no caching and no pacing — the Service does both.
	Reverse(ctx context.Context, lat, lng float64, precision uint) (Place, error)
}

// Service is the queue, the cache, and the choice of provider.
//
// # The queue spans every provider it was given
//
// Each provider has its own pace, and the queue is the union of them: a
// lookup goes to whichever is free soonest, so two providers give the sum of
// their rates rather than the larger of them. With Nominatim and Geoapify
// together that is six requests a second instead of one, and the one with the
// tighter limit stops being the ceiling for everybody.
//
// # A provider that fails hands the lookup on
//
// An outage at one is not an outage of the feature, so a failure falls through
// to the next provider rather than to the caller. Only when all of them have
// refused does a lookup fail — and even then a doléance is recorded unnamed,
// because a place name is a nicety and losing somebody's text to a third-party
// outage would be indefensible.
type Service struct {
	providers []Provider
	budgets   []*budget

	// mu guards the cache, which every concurrent submission touches.
	mu    sync.Mutex
	cache map[string]Place
}

// NewService builds the queue over one or more providers.
//
// limits are keyed by provider name and may be nil or partial: anything a
// deployment did not set falls back to that provider's own defaults, so a
// configuration file naming only the plan it bought is enough.
func NewService(limits map[string]Limits, providers ...Provider) *Service {
	s := &Service{cache: map[string]Place{}}
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		s.providers = append(s.providers, provider)
		s.budgets = append(s.budgets, newBudget(limits[provider.Name()], provider.Defaults()))
	}
	return s
}

// Remaining reports what is left of each provider's day, -1 for unmetered.
// It is what an operator reads when labels stop appearing.
func (s *Service) Remaining() map[string]int {
	left := map[string]int{}
	for i, provider := range s.providers {
		left[provider.Name()] = s.budgets[i].remaining()
	}
	return left
}

// Providers names what this service will call, in configuration order.
func (s *Service) Providers() []string {
	names := make([]string, 0, len(s.providers))
	for _, provider := range s.providers {
		names = append(names, provider.Name())
	}
	return names
}

// ErrNoProvider means nothing is configured to answer.
var ErrNoProvider = errors.New("geocode: no provider configured")

// ErrDayExhausted means every provider that could have answered has spent its
// daily allowance. A doléance is still recorded, unnamed.
var ErrDayExhausted = errors.New("geocode: daily allowance spent")

// Reverse names the place a point falls in, at the requested granularity.
func (s *Service) Reverse(ctx context.Context, lat, lng float64, precision uint) (Place, error) {
	if len(s.providers) == 0 {
		return Place{}, ErrNoProvider
	}

	// The cache key is the cell, not the point: every doléance pinned in the
	// same town resolves to the same answer, so the second one should not cost
	// a request. This is also what keeps a busy day inside the rate limits.
	key := geo.Prefix(geo.Encode(lat, lng), precision) + "/" + strconv.Itoa(int(precision))

	s.mu.Lock()
	if place, ok := s.cache[key]; ok {
		s.mu.Unlock()
		return place, nil
	}
	s.mu.Unlock()

	place, err := s.ask(ctx, lat, lng, precision)
	if err != nil {
		return Place{}, err
	}

	s.mu.Lock()
	s.cache[key] = place
	s.mu.Unlock()
	return place, nil
}

// retryAfterOf reads the provider's own Retry-After out of a throttling error.
func retryAfterOf(err error) time.Duration {
	var said *throttled
	if errors.As(err, &said) {
		return said.retryAfter
	}
	return 0
}

// ask walks the providers, soonest-free first, until one answers.
func (s *Service) ask(ctx context.Context, lat, lng float64, precision uint) (Place, error) {
	// Soonest free first, so a lookup takes the shortest queue rather than
	// always the same provider. A provider whose day is spent sorts last and
	// is skipped when its turn comes: it is not an error, it is a plan.
	order := make([]int, len(s.providers))
	for i := range order {
		order[i] = i
	}
	free := make([]time.Time, len(s.providers))
	hasBudget := make([]bool, len(s.providers))
	for i := range s.budgets {
		free[i], hasBudget[i] = s.budgets[i].peek()
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := order[a], order[b]
		if hasBudget[x] != hasBudget[y] {
			return hasBudget[x]
		}
		return free[x].Before(free[y])
	})

	var errs []error
	var exhausted, backingOff []string
	for _, i := range order {
		provider := s.providers[i]
		slot, ok := s.budgets[i].reserve()
		if !ok {
			if s.budgets[i].spentForToday() {
				exhausted = append(exhausted, provider.Name())
			} else {
				// Backing off after a 429, and due back shortly. Not the same
				// answer as a spent day and not reported as one.
				//
				// It is skipped rather than waited for, even when it is the
				// only provider left. A submission must not sit for a minute
				// so that it can carry a place name: the name is a nicety and
				// the doléance is recorded unnamed.
				backingOff = append(backingOff, provider.Name())
			}
			continue
		}

		waited := s.budgets[i].wait(ctx, slot)
		if ctx.Err() != nil {
			return Place{}, ctx.Err()
		}

		started := time.Now()
		place, err := provider.Reverse(ctx, lat, lng, precision)
		if err == nil {
			// Which is what ends a backoff: the provider is answering again, so
			// whatever run of 429s preceded this was congestion rather than a
			// spent quota.
			s.budgets[i].served()
			log.Debug().
				Str("provider", provider.Name()).
				Dur("queued", waited).
				Dur("took", time.Since(started)).
				Int("remaining_today", s.budgets[i].remaining()).
				Str("label", place.Label).
				Msg("geocode: named a place")
			return place, nil
		}

		if errors.Is(err, ErrThrottled) {
			s.budgets[i].refused(retryAfterOf(err))
			log.Warn().Err(err).Str("provider", provider.Name()).
				Int("remaining_today", s.budgets[i].remaining()).
				Msg("geocode: provider is rate limiting, trying the next")
			errs = append(errs, err)
			continue
		}

		// Said at WARN with the provider named, because the useful question
		// when labels go missing is *which* service stopped answering.
		log.Warn().Err(err).Str("provider", provider.Name()).
			Msg("geocode: provider refused, trying the next")
		errs = append(errs, err)
	}

	if len(backingOff) > 0 {
		log.Warn().Strs("providers", backingOff).
			Msg("geocode: provider backing off after a refusal; skipped for now")
		errs = append(errs, fmt.Errorf("%w: %v", ErrThrottled, backingOff))
	}

	if len(exhausted) > 0 {
		// The one failure an operator can actually act on — by raising a plan
		// or adding a provider — so it is named rather than folded into a
		// generic error.
		log.Warn().Strs("providers", exhausted).
			Msg("geocode: daily allowance spent; places will be unnamed until it resets")
		errs = append(errs, fmt.Errorf("%w: %v", ErrDayExhausted, exhausted))
	}
	return Place{}, errors.Join(errs...)
}
