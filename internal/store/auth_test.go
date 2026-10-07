package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/models"
)

func provisioned() models.AuthSettings {
	return models.AuthSettings{
		Provisioned:   true,
		InstanceURL:   "https://authentik.example",
		ClientID:      "a-client-id",
		ClientSecret:  "a-client-secret-nobody-else-may-have",
		APIToken:      "a-token-that-can-make-users",
		AdminGroup:    "uuid-admins",
		CuratorGroup:  "uuid-curators",
		ProvisionedBy: "dominique",
	}
}

// TestNeitherSecretIsInTheDatabase. One lets somebody sign in as the console;
// the other lets them make users in the operator's own directory. Of
// everything this register stores, these are the two worth a backup being
// inert about.
func TestNeitherSecretIsInTheDatabase(t *testing.T) {
	s := keyedStore(t, filepath.Join(t.TempDir(), "test.db"), newKey(t))
	ctx := context.Background()

	if err := s.SaveAuthSettings(ctx, provisioned(), false); err != nil {
		t.Fatalf("SaveAuthSettings: %v", err)
	}

	var secret, token string
	err := s.DB().Raw("select client_secret, api_token from auth_settings where id = ?",
		models.AuthSettingsID).Row().Scan(&secret, &token)
	if err != nil {
		t.Fatalf("read the raw columns: %v", err)
	}
	if strings.Contains(secret, "nobody-else-may-have") {
		t.Error("the oauth client secret is in the database in clear")
	}
	if strings.Contains(token, "can-make-users") {
		t.Error("the authentik api token is in the database in clear")
	}

	read, err := s.AuthSettings(ctx)
	if err != nil {
		t.Fatalf("AuthSettings: %v", err)
	}
	if read.ClientSecret != "a-client-secret-nobody-else-may-have" {
		t.Errorf("client secret came back %q", read.ClientSecret)
	}
	if read.APIToken != "a-token-that-can-make-users" {
		t.Errorf("api token came back %q", read.APIToken)
	}
}

// TestAProvisionedInstallationIsNotRepointedByAccident.
//
// This is the one write that can hand administration of the register to
// whoever asked for it. The wizard runs before anybody can be identified, so
// there is no caller to check and the state of the row has to be the guard.
func TestAProvisionedInstallationIsNotRepointedByAccident(t *testing.T) {
	s := keyedStore(t, filepath.Join(t.TempDir(), "test.db"), newKey(t))
	ctx := context.Background()

	if err := s.SaveAuthSettings(ctx, provisioned(), false); err != nil {
		t.Fatalf("first SaveAuthSettings: %v", err)
	}

	elsewhere := provisioned()
	elsewhere.InstanceURL = "https://attacker.example"
	if err := s.SaveAuthSettings(ctx, elsewhere, false); !errors.Is(err, ErrAlreadyProvisioned) {
		t.Errorf("err = %v, want ErrAlreadyProvisioned", err)
	}

	read, err := s.AuthSettings(ctx)
	if err != nil {
		t.Fatalf("AuthSettings: %v", err)
	}
	if read.InstanceURL != "https://authentik.example" {
		t.Errorf("the instance was repointed to %q", read.InstanceURL)
	}

	// Deliberately, it still works — which is what the flag that reopens the
	// setup door is for.
	if err := s.SaveAuthSettings(ctx, elsewhere, true); err != nil {
		t.Fatalf("deliberate reprovision: %v", err)
	}
	read, _ = s.AuthSettings(ctx)
	if read.InstanceURL != "https://attacker.example" {
		t.Error("a deliberate reprovision did not take effect")
	}
}

// TestProvisionedIsAnswerableWithoutTheKey.
//
// It decides whether the setup route is registered at all, so it must not need
// to decrypt anything. An installation whose settings key is wrong should
// still know it is configured — otherwise a mistyped key reopens the setup
// door, which is the one thing that must never follow from a configuration
// mistake.
func TestProvisionedIsAnswerableWithoutTheKey(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "test.db")
	ctx := context.Background()

	configured := keyedStore(t, dsn, newKey(t))
	if err := configured.SaveAuthSettings(ctx, provisioned(), false); err != nil {
		t.Fatalf("SaveAuthSettings: %v", err)
	}
	configured.Close(ctx) //nolint:errcheck

	// A different key: the secrets are unreadable and the fact is not.
	wrongKey := keyedStore(t, dsn, newKey(t))
	defer wrongKey.Close(ctx) //nolint:errcheck

	if _, err := wrongKey.AuthSettings(ctx); err == nil {
		t.Error("the secrets were readable with the wrong key")
	}

	isProvisioned, err := wrongKey.AuthProvisioned(ctx)
	if err != nil {
		t.Fatalf("AuthProvisioned: %v", err)
	}
	if !isProvisioned {
		t.Error("a wrong settings key made a configured console look unconfigured")
	}
}

// TestAFreshInstallationIsUnprovisioned, which is what puts the wizard on the
// maintenance port in the first place.
func TestAFreshInstallationIsUnprovisioned(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	isProvisioned, err := s.AuthProvisioned(ctx)
	if err != nil {
		t.Fatalf("AuthProvisioned: %v", err)
	}
	if isProvisioned {
		t.Error("a fresh installation reports itself configured")
	}

	settings, err := s.AuthSettings(ctx)
	if err != nil {
		t.Fatalf("AuthSettings: %v", err)
	}
	if settings.Provisioned || settings.HasToken() {
		t.Errorf("a fresh installation has settings: %+v", settings)
	}
}
