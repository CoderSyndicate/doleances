package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func statusOf(t *testing.T, middleware func(http.Handler) http.Handler,
	method, path, remote string, forwarded ...string) int {

	t.Helper()

	request := httptest.NewRequest(method, path, nil)
	request.RemoteAddr = remote
	for _, value := range forwarded {
		request.Header.Add("X-Forwarded-For", value)
	}

	recorder := httptest.NewRecorder()
	middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(recorder, request)
	return recorder.Code
}

// TestOneAddressCannotHammerTheSite is the whole of what this is for: a broken
// loop or a crawler with no delay must not be able to take the register down,
// and the thing that happens instead is a 429 rather than a slow page.
func TestOneAddressCannotHammerTheSite(t *testing.T) {
	limited := RateLimited(Bounds{Pages: 3, Assets: 3, Writes: 3}, DefaultTrustedProxies())

	for attempt := 1; attempt <= 3; attempt++ {
		if code := statusOf(t, limited, http.MethodGet, "/register", "198.51.100.7:1234"); code != http.StatusOK {
			t.Fatalf("attempt %d answered %d, want 200", attempt, code)
		}
	}
	if code := statusOf(t, limited, http.MethodGet, "/register", "198.51.100.7:1234"); code != http.StatusTooManyRequests {
		t.Errorf("the fourth attempt answered %d, want 429", code)
	}

	// And one address running out does not refuse anybody else. A village
	// shares an address; a village does not share it with the whole internet.
	if code := statusOf(t, limited, http.MethodGet, "/register", "203.0.113.9:1234"); code != http.StatusOK {
		t.Errorf("a different address answered %d, want 200", code)
	}
}

// TestThePageBudgetIsNotSpentOnStylesheets: a browser pulls a dozen assets per
// page, so counting them against the page allowance would mean a reader who
// opened three pages had used up their reading for the minute.
func TestThePageBudgetIsNotSpentOnStylesheets(t *testing.T) {
	limited := RateLimited(Bounds{Pages: 2, Assets: 50, Writes: 2}, DefaultTrustedProxies())
	const from = "198.51.100.7:1234"

	for range 20 {
		if code := statusOf(t, limited, http.MethodGet, "/static/default.css", from); code != http.StatusOK {
			t.Fatalf("an asset answered %d, want 200", code)
		}
	}
	// The page allowance is untouched.
	for attempt := 1; attempt <= 2; attempt++ {
		if code := statusOf(t, limited, http.MethodGet, "/register", from); code != http.StatusOK {
			t.Errorf("page %d answered %d, want 200", attempt, code)
		}
	}
}

// TestWritesAreBoundedSeparately is the half of this that is worth having.
//
// A per-address limit on reads is nearly useless against a crowd behind one
// carrier NAT; a per-address limit on writes is not, because the write rate of
// a real person is tiny. So the two must not share a bucket — reading the
// register must never consume somebody's ability to write a doléance.
func TestWritesAreBoundedSeparately(t *testing.T) {
	limited := RateLimited(Bounds{Pages: 50, Assets: 50, Writes: 2}, DefaultTrustedProxies())
	const from = "198.51.100.7:1234"

	for range 20 {
		if code := statusOf(t, limited, http.MethodGet, "/register", from); code != http.StatusOK {
			t.Fatalf("reading answered %d, want 200", code)
		}
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if code := statusOf(t, limited, http.MethodPost, "/doleance", from); code != http.StatusOK {
			t.Errorf("write %d answered %d, want 200", attempt, code)
		}
	}
	if code := statusOf(t, limited, http.MethodPost, "/doleance", from); code != http.StatusTooManyRequests {
		t.Errorf("the third write answered %d, want 429", code)
	}
}

// TestAPostToAnAssetPathIsNotAnAsset: the asset allowance is generous because
// an asset costs nothing, and a write classified as one by its path would
// inherit that generosity.
func TestAPostToAnAssetPathIsNotAnAsset(t *testing.T) {
	limited := RateLimited(Bounds{Pages: 50, Assets: 50, Writes: 1}, DefaultTrustedProxies())
	const from = "198.51.100.7:1234"

	if code := statusOf(t, limited, http.MethodPost, "/static/anything", from); code != http.StatusOK {
		t.Fatalf("answered %d, want 200", code)
	}
	if code := statusOf(t, limited, http.MethodPost, "/static/anything", from); code != http.StatusTooManyRequests {
		t.Errorf("answered %d, want 429 — it was counted as an asset", code)
	}
}

// TestAnAllowanceOfZeroSwitchesTheClassOff rather than refusing everything. An
// operator must not be able to take their own site down with a typo in a
// number, and "0" is what somebody types meaning "no limit".
func TestAnAllowanceOfZeroSwitchesTheClassOff(t *testing.T) {
	limited := RateLimited(Bounds{Pages: 0, Assets: 0, Writes: 0}, DefaultTrustedProxies())

	for range 50 {
		if code := statusOf(t, limited, http.MethodGet, "/register", "198.51.100.7:1234"); code != http.StatusOK {
			t.Fatal("a bound of zero refused a request")
		}
	}
}

// TestARefusalSaysWhenToComeBack: a 429 with no Retry-After tells a client to
// guess, and a client that guesses retries immediately.
func TestARefusalSaysWhenToComeBack(t *testing.T) {
	limited := RateLimited(Bounds{Pages: 1, Assets: 1, Writes: 1}, DefaultTrustedProxies())

	request := httptest.NewRequest(http.MethodGet, "/register", nil)
	request.RemoteAddr = "198.51.100.7:1234"
	handler := limited(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))

	handler.ServeHTTP(httptest.NewRecorder(), request)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("code = %d, want 429", recorder.Code)
	}
	// One a minute, so a minute — and never "0", which means "now", which is
	// what the caller just tried.
	if after := recorder.Header().Get("Retry-After"); after != "60" {
		t.Errorf("Retry-After = %q, want 60", after)
	}
}
