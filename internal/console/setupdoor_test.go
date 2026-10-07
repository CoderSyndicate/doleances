package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/apiclient"
)

// backendSaying stands in for the backend's answer about the auth settings.
func backendSaying(t *testing.T, status int, settings any) *apiclient.Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if settings != nil {
			json.NewEncoder(w).Encode(settings) //nolint:errcheck
		}
	}))
	t.Cleanup(server.Close)

	c, err := apiclient.New(server.URL)
	if err != nil {
		t.Fatalf("apiclient.New: %v", err)
	}
	return c
}

// TestAFreshInstallationOpensTheWizard — the ordinary first run, which needs
// no flag. The wizard is on the maintenance port, which is never published, so
// what stands in front of it is the network rather than a secret.
func TestAFreshInstallationOpensTheWizard(t *testing.T) {
	backend := backendSaying(t, 200, map[string]any{"provisioned": false})

	door := decideSetupDoor(context.Background(), backend, false)
	if !door.Open {
		t.Error("a console with no identity provider does not offer the wizard")
	}
	if door.Reprovision {
		t.Error("a first run asked to replace a configuration that does not exist")
	}
}

// TestAProvisionedConsoleHasNoWizard. Not a page that refuses — no route at
// all. The project already draws this line for groups: switched off means
// invisible, because a feature still on screen and refusing to work reads as
// a broken one.
func TestAProvisionedConsoleHasNoWizard(t *testing.T) {
	backend := backendSaying(t, 200, map[string]any{
		"provisioned": true, "instance_url": "https://authentik.example",
	})

	door := decideSetupDoor(context.Background(), backend, false)
	if door.Open {
		t.Error("a configured console still offers its setup wizard")
	}
}

// TestSuperuserReopensIt, and says it is replacing something.
//
// This is the flag's whole job, and it is deliberately not --development:
// that one disables authentication, where this disables nothing and reopens
// one page on a console that is otherwise running normally. Needing to
// re-point an identity provider must not imply unlocking everything else.
func TestSuperuserReopensIt(t *testing.T) {
	backend := backendSaying(t, 200, map[string]any{
		"provisioned": true, "instance_url": "https://authentik.example",
	})

	door := decideSetupDoor(context.Background(), backend, true)
	if !door.Open {
		t.Error("--superuser did not reopen the setup wizard")
	}
	if !door.Reprovision {
		t.Error("--superuser did not tell the backend it means to replace a configuration")
	}
}

// TestAnUnreachableBackendKeepsTheDoorShut.
//
// The safe failure is the locked one. If the console cannot find out whether
// an identity provider is configured, it assumes one is — otherwise a backend
// having a bad second would be a way to make a provisioned console offer its
// setup page again, which is the one failure that hands a register to whoever
// is watching.
func TestAnUnreachableBackendKeepsTheDoorShut(t *testing.T) {
	backend := backendSaying(t, http.StatusInternalServerError, nil)

	door := decideSetupDoor(context.Background(), backend, false)
	if door.Open {
		t.Error("an unreachable backend opened the setup wizard")
	}
}

// TestSuperuserStillWorksWhenTheBackendIsDown, because at that point an
// operator is standing at a terminal having typed the flag, and a console that
// could not be re-pointed while its backend was unhappy would be one nobody
// could fix. The backend still refuses a write it does not like.
func TestSuperuserStillWorksWhenTheBackendIsDown(t *testing.T) {
	backend := backendSaying(t, http.StatusInternalServerError, nil)

	door := decideSetupDoor(context.Background(), backend, true)
	if !door.Open {
		t.Error("--superuser could not open the wizard against an unhappy backend")
	}
	if !door.Reprovision {
		t.Error("the deliberate path did not say it means to replace a configuration")
	}
}
