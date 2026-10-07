package ratelimit

import (
	"testing"
	"time"
)

func TestBurstThenRefusal(t *testing.T) {
	limiter := New(3, time.Minute)

	for attempt := 1; attempt <= 3; attempt++ {
		if !limiter.Allow("192.0.2.1") {
			t.Fatalf("attempt %d was refused inside the burst", attempt)
		}
	}
	if limiter.Allow("192.0.2.1") {
		t.Error("a fourth attempt was allowed past the burst")
	}
	// One caller's burst must not touch another's, or a single script would
	// lock the register's signup for everybody.
	if !limiter.Allow("192.0.2.2") {
		t.Error("a different caller was refused on somebody else's burst")
	}
}

func TestTokensComeBack(t *testing.T) {
	limiter := New(1, time.Minute)
	clock := time.Now()
	limiter.now = func() time.Time { return clock }

	if !limiter.Allow("a") {
		t.Fatal("the first attempt was refused")
	}
	if limiter.Allow("a") {
		t.Fatal("the second attempt was allowed immediately")
	}

	clock = clock.Add(time.Minute)
	if !limiter.Allow("a") {
		t.Error("the attempt did not come back after the refill interval")
	}
}

// TestIdleKeysAreForgotten: the map would otherwise be a growing record of
// every address that ever called, which is both a leak and a list this
// project should not keep.
func TestIdleKeysAreForgotten(t *testing.T) {
	limiter := New(1, time.Minute)
	clock := time.Now()
	limiter.now = func() time.Time { return clock }

	limiter.Allow("a")
	clock = clock.Add(time.Hour)
	limiter.Allow("b")

	if _, held := limiter.buckets["a"]; held {
		t.Error("an idle caller is still remembered")
	}
}
