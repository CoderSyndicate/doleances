package console

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func wizardConsole(door SetupDoor) *console {
	return &console{setupDoor: door, siteURL: "https://console.example"}
}

func renderSetup(t *testing.T, c *console, method string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	c.registerWizard(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(method, WizardPath, nil))
	return recorder
}

// TestAProvisionedConsoleHasNoSetupRoute. Not a page that refuses — no route.
// The project's own line: switched off means invisible, because a feature
// still on screen and refusing to work reads as a broken one.
func TestAProvisionedConsoleHasNoSetupRoute(t *testing.T) {
	got := renderSetup(t, wizardConsole(SetupDoor{}), http.MethodGet)

	if got.Code != http.StatusNotFound {
		t.Errorf("a provisioned console answers %d at %s, want 404", got.Code, WizardPath)
	}
}

// TestAFreshConsoleOffersTheWizard, and the page says what it needs and why.
func TestAFreshConsoleOffersTheWizard(t *testing.T) {
	got := renderSetup(t, wizardConsole(SetupDoor{Open: true}), http.MethodGet)

	if got.Code != http.StatusOK {
		t.Fatalf("code = %d", got.Code)
	}
	body := got.Body.String()

	for _, want := range []string{
		"instance_url", "token", "console_url",
		// The bootstrap problem said in words, since it is the step an
		// operator cannot guess.
		"Directory → Tokens",
		// What the token can do, where they are about to paste it.
		"can make users in your directory",
		// The name, and the group names it builds — so an operator sees them
		// before they exist rather than afterwards.
		"app_name", "doleances_admin", "doleances_curator",
		// The exact redirect, so it can be checked rather than looked up.
		"/auth/callback",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the setup page does not mention %q", want)
		}
	}
}

// TestTheSetupPageCarriesAPolicy.
//
// Service middleware is applied to the application mux only, so the
// maintenance port has no Content-Security-Policy of its own. This is the one
// page there that renders HTML and takes a pasted credential, so it wraps
// itself — without that it would be the only HTML page in the project with no
// policy.
func TestTheSetupPageCarriesAPolicy(t *testing.T) {
	got := renderSetup(t, wizardConsole(SetupDoor{Open: true}), http.MethodGet)

	policy := got.Header().Get("Content-Security-Policy")
	if policy == "" {
		t.Fatal("the setup page is served with no Content-Security-Policy")
	}
	if !strings.Contains(policy, "default-src 'none'") {
		t.Errorf("policy = %q, want the project's default-src 'none'", policy)
	}
	if got.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("the setup page does not refuse content sniffing")
	}
}

// TestReprovisioningSaysWhatItReplaces. Somebody who reached this page with
// --superuser is about to hand the register to whichever directory they name,
// and the page has to say so in the plainest words it has.
func TestReprovisioningSaysWhatItReplaces(t *testing.T) {
	got := renderSetup(t, wizardConsole(SetupDoor{Open: true, Reprovision: true}), http.MethodGet)
	body := got.Body.String()

	if !strings.Contains(body, "already has one") {
		t.Error("the page does not say an identity provider is already configured")
	}
	if !strings.Contains(body, "Replace the identity provider") {
		t.Error("the button does not say it replaces anything")
	}

	// And a fresh console does not say any of that.
	fresh := renderSetup(t, wizardConsole(SetupDoor{Open: true}), http.MethodGet).Body.String()
	if strings.Contains(fresh, "already has one") {
		t.Error("a first run was warned about replacing something")
	}
}

// TestABadInstanceURLIsAnsweredOnThePage rather than with a stack trace or a
// bare status: the operator is mid-setup and the remedy is a field they can
// see.
func TestABadInstanceURLIsAnsweredOnThePage(t *testing.T) {
	c := wizardConsole(SetupDoor{Open: true})
	mux := http.NewServeMux()
	c.registerWizard(mux)

	form := strings.NewReader("instance_url=ftp://nope&token=t&console_url=https://c.example")
	request := httptest.NewRequest(http.MethodPost, WizardPath, form)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Errorf("code = %d, want 422", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "http or https") {
		t.Error("the page does not say what was wrong with the URL")
	}
	// And the form is still there to correct.
	if !strings.Contains(recorder.Body.String(), "instance_url") {
		t.Error("the form is gone after a mistake")
	}
}

// deadlineRecorder is a ResponseWriter that remembers deadline changes, which
// httptest.ResponseRecorder does not support.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	writeDeadline time.Time
	cleared       bool
}

func (d *deadlineRecorder) SetWriteDeadline(at time.Time) error {
	d.writeDeadline, d.cleared = at, at.IsZero()
	return nil
}

// TestTheWizardOutlivesTheServersWriteDeadline.
//
// Provisioning is twenty-odd calls to somebody else's authentik — forty
// seconds against a remote instance, against a thirty-second WriteTimeout.
// The deadline runs from when the headers were read, so the connection is
// closed before the handler writes anything and the handler finishes anyway:
// every object created, the settings stored, "setup complete" in the log, and
// a browser saying the server dropped the connection.
//
// What is lost with that response is the page shown once, carrying the first
// admin's only way in. This is the line that stops it.
func TestTheWizardOutlivesTheServersWriteDeadline(t *testing.T) {
	c := &console{setupDoor: SetupDoor{Open: true}}
	recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}

	c.wizard(recorder, httptest.NewRequest(http.MethodGet, WizardPath, nil))

	if !recorder.cleared {
		t.Errorf("write deadline = %v, want it cleared — provisioning takes longer than it",
			recorder.writeDeadline)
	}
	if recorder.Code != http.StatusOK {
		t.Errorf("code = %d, want the page", recorder.Code)
	}
}

// TestTheInstanceFieldIsPrefilledFromConfiguration.
//
// A deployment driven by automation already knows its own authentik, and
// making somebody read the address off another screen and retype it into the
// wizard is how a provider gets registered against the wrong host — a typo
// that fails much later, somewhere else.
//
// It fills a field and nothing more: after provisioning the address comes from
// the settings row, so an operator who corrects it here is corrected
// everywhere rather than in one of two places.
func TestTheInstanceFieldIsPrefilledFromConfiguration(t *testing.T) {
	c := &console{
		setupDoor:  SetupDoor{Open: true},
		oidcIssuer: "https://authentik.example",
	}

	recorder := httptest.NewRecorder()
	c.wizard(recorder, httptest.NewRequest(http.MethodGet, WizardPath, nil))

	body := recorder.Body.String()
	if !strings.Contains(body, `value="https://authentik.example"`) {
		t.Error("the instance field was not prefilled")
	}
	if !strings.Contains(body, "configuration") {
		t.Error("the page does not say where the address came from, so nobody checks it")
	}

	// And an empty one leaves the field empty: an address nobody typed is an
	// address nobody checks, so a guess would be worse than nothing.
	empty := &console{setupDoor: SetupDoor{Open: true}}
	plain := httptest.NewRecorder()
	empty.wizard(plain, httptest.NewRequest(http.MethodGet, WizardPath, nil))
	if strings.Contains(plain.Body.String(), `id="instance_url" name="instance_url" required
           placeholder="https://authentik.example" value="h`) {
		t.Error("an address was invented for a deployment that configured none")
	}
}
