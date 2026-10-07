package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func asked(remote string, forwarded ...string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/register", nil)
	request.RemoteAddr = remote
	for _, value := range forwarded {
		request.Header.Add("X-Forwarded-For", value)
	}
	return request
}

// TestAClientCannotNameItself is the property the whole limiter rests on.
//
// Without it the limit is decoration: a caller sets X-Forwarded-For to a fresh
// value on every request, gets a fresh bucket every time, and walks through
// any per-address bound as if it were not there. So a header is believed only
// from a hop that is in the trusted list — and a caller who reached this
// service directly is not.
func TestAClientCannotNameItself(t *testing.T) {
	proxies := DefaultTrustedProxies()

	// Straight off the internet, claiming to be somebody else.
	from := proxies.ClientAddress(asked("198.51.100.7:1234", "203.0.113.1"))
	if from != "198.51.100.7" {
		t.Errorf("address = %q, want the address it really came from", from)
	}

	// And a different claim must not buy a different bucket.
	again := proxies.ClientAddress(asked("198.51.100.7:1234", "203.0.113.2"))
	if again != from {
		t.Errorf("two claims from one address answered %q and %q", from, again)
	}
}

// TestAProxyIsBelieved is the other half, and it matters as much: behind an
// ingress every request arrives from the ingress, so a limiter that ignored
// the header would put every reader in the world in one bucket and refuse the
// site to everybody the moment one script ran.
func TestAProxyIsBelieved(t *testing.T) {
	proxies := DefaultTrustedProxies()

	from := proxies.ClientAddress(asked("10.4.2.1:38214", "198.51.100.7"))
	if from != "198.51.100.7" {
		t.Errorf("address = %q, want the reader behind the proxy", from)
	}
}

// TestTheChainIsReadFromTheRightEnd: each hop appends the address it heard
// from, so the rightmost entries were written by hops near this service and
// the leftmost by whoever sent the request — who may have written anything.
// Reading from the left would take the attacker's own invention.
func TestTheChainIsReadFromTheRightEnd(t *testing.T) {
	proxies := DefaultTrustedProxies()

	// A caller sent "203.0.113.1" themselves; the edge proxy appended their
	// real address; an inner proxy appended the edge's.
	from := proxies.ClientAddress(asked("10.4.2.1:38214", "203.0.113.1, 198.51.100.7, 10.9.9.9"))
	if from != "198.51.100.7" {
		t.Errorf("address = %q, want the first hop we have no reason to believe", from)
	}
}

// TestSeveralHeaderLinesAreOneChain, because a chain of proxies may each add
// their own header line rather than appending to one.
func TestSeveralHeaderLinesAreOneChain(t *testing.T) {
	proxies := DefaultTrustedProxies()

	from := proxies.ClientAddress(asked("10.4.2.1:38214", "203.0.113.1", "198.51.100.7", "10.9.9.9"))
	if from != "198.51.100.7" {
		t.Errorf("address = %q, want the chain read across both lines", from)
	}
}

// TestAnInternalCallerIsItself: when every hop in the chain is one of ours the
// chain never reached anybody else, so there is nobody else to name.
func TestAnInternalCallerIsItself(t *testing.T) {
	proxies := DefaultTrustedProxies()

	from := proxies.ClientAddress(asked("10.4.2.1:38214", "10.9.9.9, 10.1.1.1"))
	if from != "10.4.2.1" {
		t.Errorf("address = %q, want the peer", from)
	}
}

// TestRubbishInTheChainStopsBeingEvidence. A hop that cannot be parsed is a
// hop we cannot place, and guessing past it is how a forged entry gets read as
// a real one.
func TestRubbishInTheChainStopsBeingEvidence(t *testing.T) {
	proxies := DefaultTrustedProxies()

	from := proxies.ClientAddress(asked("10.4.2.1:38214", "198.51.100.7, not-an-address"))
	if from != "10.4.2.1" {
		t.Errorf("address = %q, want the peer", from)
	}
}

// TestNoTrustedProxiesMeansNobodySpeaksForAnybody — which is what an
// installation facing the internet with nothing in front wants.
func TestNoTrustedProxiesMeansNobodySpeaksForAnybody(t *testing.T) {
	var none TrustedProxies

	from := none.ClientAddress(asked("10.4.2.1:38214", "198.51.100.7"))
	if from != "10.4.2.1" {
		t.Errorf("address = %q, want the peer", from)
	}
}

// TestAProxyMayBeWrittenAsABareAddress, because "the proxy is at 10.4.2.1" is
// how an operator thinks about one hop, and making them write /32 is a way to
// collect a startup failure instead of a correct setting.
func TestAProxyMayBeWrittenAsABareAddress(t *testing.T) {
	proxies, err := ParseTrustedProxies([]string{"203.0.113.5", " 2001:db8::1 "})
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}

	if from := proxies.ClientAddress(asked("203.0.113.5:9000", "198.51.100.7")); from != "198.51.100.7" {
		t.Errorf("address = %q, want the reader behind the named proxy", from)
	}
	// And a neighbour of it is not it.
	if from := proxies.ClientAddress(asked("203.0.113.6:9000", "198.51.100.7")); from != "203.0.113.6" {
		t.Errorf("address = %q, want the peer", from)
	}
}

// TestABadRangeIsRefused: an operator who wrote a range wrong is one whose
// limiter silently puts every reader in one bucket, and finding that out from
// a graph is much worse than finding it out from a startup error.
func TestABadRangeIsRefused(t *testing.T) {
	if _, err := ParseTrustedProxies([]string{"10.0.0.0/8", "not a range"}); err == nil {
		t.Error("an unreadable range was accepted")
	}
}

// TestAMappedProxyMatchesItsOwnRange. Go reports some connections as
// ::ffff:10.0.0.1, and a private range written in IPv4 has to match it or the
// limiter quietly stops believing the proxy in front of it.
func TestAMappedProxyMatchesItsOwnRange(t *testing.T) {
	proxies := DefaultTrustedProxies()

	from := proxies.ClientAddress(asked("[::ffff:10.4.2.1]:38214", "198.51.100.7"))
	if from != "198.51.100.7" {
		t.Errorf("address = %q, want the reader behind the proxy", from)
	}
}
