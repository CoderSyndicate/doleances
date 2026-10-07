package health

import (
	"sync"
	"testing"
)

func TestNewIsLiveButNotReady(t *testing.T) {
	s := New()
	if !s.Live() {
		t.Error("a new service should be live")
	}
	if s.Ready() {
		t.Error("a new service must not be ready before it has started up")
	}
}

func TestSetReady(t *testing.T) {
	s := New()

	s.SetReady(true)
	if !s.Ready() {
		t.Error("Ready() = false after SetReady(true)")
	}

	// This is the first move of a graceful shutdown.
	s.SetReady(false)
	if !s.Live() {
		t.Error("withdrawing readiness must not affect liveness")
	}
	if s.Ready() {
		t.Error("Ready() = true after SetReady(false)")
	}
}

func TestSetLive(t *testing.T) {
	s := New()
	s.SetLive(false)
	if s.Live() {
		t.Error("Live() = true after SetLive(false)")
	}
}

func TestConcurrentUseIsSafe(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.SetReady(i%2 == 0)
			_ = s.Ready()
			_ = s.Live()
		}()
	}
	wg.Wait()
}
