package frontend

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestASessionNeverReachesThePage is the security property the whole cookie
// arrangement rests on.
//
// The token authorises registering a passkey and removing one. A script that
// could read it could also send it somewhere, so the frontend turns it into an
// HttpOnly cookie and **strips it from the body** before the page's own code
// ever sees the answer.
func TestASessionNeverReachesThePage(t *testing.T) {
	s := &site{secureCookies: true}
	recorder := httptest.NewRecorder()

	expires := time.Now().Add(8 * time.Hour).UTC().Format(time.RFC3339Nano)
	answer := []byte(`{"session":"a-secret-token","expires_at":"` + expires +
		`","account":{"id":"an-account","name":"Camille"},"recovery_codes":["ABCD-EFGH"]}`)

	trimmed := s.captureSession(recorder, answer)

	if strings.Contains(string(trimmed), "a-secret-token") {
		t.Error("the session token reached the page")
	}
	// And what the page does need is still there: it has to know it is signed
	// in, and the recovery codes exist in this one response and nowhere else.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		t.Fatalf("the trimmed answer is not JSON: %v", err)
	}
	if string(fields["signed_in"]) != "true" {
		t.Error("the page was not told it is signed in")
	}
	if !strings.Contains(string(fields["recovery_codes"]), "ABCD-EFGH") {
		t.Error("the recovery codes were lost")
	}

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("%d cookies were set, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Value != "a-secret-token" {
		t.Errorf("the cookie holds %q, want the token", cookie.Value)
	}
	if !cookie.HttpOnly {
		t.Error("the session cookie is readable by any script on the page")
	}
	if !cookie.Secure {
		t.Error("the session cookie is sent over plain HTTP on an https deployment")
	}
	if cookie.Path != "/" {
		t.Errorf("cookie path = %q, want the whole site", cookie.Path)
	}
	// The cookie's life is the session's, so a browser stops sending a token
	// the backend would refuse anyway.
	if cookie.Expires.IsZero() {
		t.Error("the session cookie never expires")
	}
}

// TestAnAnswerWithNoSessionIsUntouched. This runs over every proxied call, and
// most of them carry no token: one that reshaped an answer it did not
// understand would corrupt the flow it was meant to be invisible to.
func TestAnAnswerWithNoSessionIsUntouched(t *testing.T) {
	s := &site{}

	for _, answer := range []string{
		`{"options":{"challenge":"abc"},"ceremony":"a-ceremony"}`,
		`{"detail":"that passkey was not accepted","status":401}`,
		`[]`,
		`not json at all`,
		``,
	} {
		recorder := httptest.NewRecorder()
		if got := string(s.captureSession(recorder, []byte(answer))); got != answer {
			t.Errorf("captureSession(%q) = %q, want it untouched", answer, got)
		}
		if len(recorder.Result().Cookies()) != 0 {
			t.Errorf("a cookie was set for %q", answer)
		}
	}
}

// TestAnEmptySessionIsNotACookie. An answer whose session field is present but
// empty is not a sign-in, and treating it as one would put an empty cookie on
// the browser and hide the real failure.
func TestAnEmptySessionIsNotACookie(t *testing.T) {
	s := &site{}
	recorder := httptest.NewRecorder()

	answer := `{"session":"","account":{"id":"x"}}`
	if got := string(s.captureSession(recorder, []byte(answer))); got != answer {
		t.Errorf("captureSession = %q, want it untouched", got)
	}
	if len(recorder.Result().Cookies()) != 0 {
		t.Error("an empty session became a cookie")
	}
}

// TestSignInOnlyReturnsToThisSite.
//
// A `next` that could name another origin is an open redirect, and an open
// redirect on the page where people sign in is how a convincing phishing link
// gets built out of a real domain.
func TestSignInOnlyReturnsToThisSite(t *testing.T) {
	for _, safe := range []string{"/account", "/groups/a-group", "/notifications?page=2"} {
		if got := safeNext(safe); got != safe {
			t.Errorf("safeNext(%q) = %q, want it kept", safe, got)
		}
	}

	for _, refused := range []string{
		"",
		"https://attacker.example/",
		"//attacker.example/",  // protocol-relative: a browser reads this as another host
		"account",              // relative, and so ambiguous
		"\\\\attacker.example", // a backslash pair some browsers normalise
		"javascript:alert(1)",  //nolint:misspell // a scheme, not a path
	} {
		if got := safeNext(refused); got != "" {
			t.Errorf("safeNext(%q) = %q, want it refused", refused, got)
		}
	}
}

// TestClearingASessionRemovesTheCookie, with the same attributes it was set
// with — a browser ignores a deletion that does not match.
func TestClearingASessionRemovesTheCookie(t *testing.T) {
	s := &site{secureCookies: true}
	recorder := httptest.NewRecorder()

	s.clearSession(recorder)

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("%d cookies, want the deletion", len(cookies))
	}
	if cookies[0].Value != "" || cookies[0].MaxAge >= 0 {
		t.Errorf("the cookie was not cleared: %+v", cookies[0])
	}
	if !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].Path != "/" {
		t.Error("the deletion does not match the cookie it removes")
	}
}
