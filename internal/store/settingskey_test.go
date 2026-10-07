package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/secret"
)

// keyedStore opens a store on one file, with one settings key, so a test can
// close it and reopen the same database with a different key.
func keyedStore(t *testing.T, dsn, key string) *Store {
	t.Helper()

	s, err := Open(Options{Driver: DriverSQLite, DSN: dsn, SettingsKey: key})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

func newKey(t *testing.T) string {
	t.Helper()

	key, err := secret.NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return key
}

// storedAPIKey reads the column as it really sits on disk, past every
// decrypting accessor — which is the only way to prove the file is protected
// rather than that the accessor is symmetrical.
func storedAPIKey(t *testing.T, s *Store) string {
	t.Helper()

	var raw string
	err := s.DB().Raw("select api_key from llm_settings where id = ?",
		models.LLMSettingsID).Scan(&raw).Error
	if err != nil {
		t.Fatalf("read the raw column: %v", err)
	}
	return raw
}

// TestTheCredentialIsNotInTheDatabase is the whole claim.
//
// The LLM key's own comment used to read "The key lives in the database, so a
// database backup contains it. Treat backups accordingly." This is what
// retires that: a dump, an old backup or a disk somebody forgot to wipe no
// longer carries anything usable.
func TestTheCredentialIsNotInTheDatabase(t *testing.T) {
	const credential = "sk-a-real-looking-credential-0123456789"
	s := keyedStore(t, filepath.Join(t.TempDir(), "test.db"), newKey(t))
	ctx := context.Background()

	err := s.SaveLLMSettings(ctx, models.LLMSettings{
		BaseURL: "https://synergia.example", APIKey: credential,
	})
	if err != nil {
		t.Fatalf("SaveLLMSettings: %v", err)
	}

	onDisk := storedAPIKey(t, s)
	if strings.Contains(onDisk, credential) {
		t.Fatal("the credential is in the database in clear")
	}
	if onDisk == "" {
		t.Fatal("nothing was stored at all")
	}

	// And the backend can still use it.
	read, err := s.LLMSettings(ctx)
	if err != nil {
		t.Fatalf("LLMSettings: %v", err)
	}
	if read.APIKey != credential {
		t.Errorf("read back %q, want the credential", read.APIKey)
	}
}

// TestAStolenDatabaseIsUselessWithoutTheKey: the same file, opened by a
// deployment that has a key but not *the* key, refuses rather than handing
// back something a caller would try to authenticate with.
func TestAStolenDatabaseIsUselessWithoutTheKey(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "test.db")
	ctx := context.Background()

	original := keyedStore(t, dsn, newKey(t))
	err := original.SaveLLMSettings(ctx, models.LLMSettings{APIKey: "sk-the-original"})
	if err != nil {
		t.Fatalf("SaveLLMSettings: %v", err)
	}
	original.Close(ctx) //nolint:errcheck

	thief := keyedStore(t, dsn, newKey(t))
	defer thief.Close(ctx) //nolint:errcheck

	if _, err := thief.LLMSettings(ctx); err == nil {
		t.Error("a database opened with the wrong key gave up its credential")
	}

	// And with no key at all, which is the likelier accident: somebody
	// restores a backup onto a deployment that was never configured.
	unkeyed := keyedStore(t, dsn, "")
	defer unkeyed.Close(ctx) //nolint:errcheck

	if _, err := unkeyed.LLMSettings(ctx); err == nil {
		t.Error("a database opened with no key gave up an encrypted credential")
	}
}

// TestAnInstallationCanAdoptAKeyWithoutLosingItsCredential.
//
// The migration nobody should have to run. A register that has been storing
// its LLM key in clear sets --settings-key, restarts, and goes on working: the
// old row reads as itself, and the next save seals it. Without this, adopting
// a key would take the classifier offline until somebody re-entered a
// credential they may no longer have.
func TestAnInstallationCanAdoptAKeyWithoutLosingItsCredential(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "test.db")
	ctx := context.Background()
	const credential = "sk-stored-before-there-was-a-key"

	// Yesterday: no key.
	before := keyedStore(t, dsn, "")
	if err := before.SaveLLMSettings(ctx, models.LLMSettings{APIKey: credential}); err != nil {
		t.Fatalf("SaveLLMSettings: %v", err)
	}
	if got := storedAPIKey(t, before); got != credential {
		t.Fatalf("the unkeyed store wrote %q, want the plaintext", got)
	}
	before.Close(ctx) //nolint:errcheck

	// Today: a key.
	after := keyedStore(t, dsn, newKey(t))
	defer after.Close(ctx) //nolint:errcheck

	read, err := after.LLMSettings(ctx)
	if err != nil {
		t.Fatalf("LLMSettings after adopting a key: %v", err)
	}
	if read.APIKey != credential {
		t.Errorf("read %q, want the credential written before the key", read.APIKey)
	}

	// The next save seals it, which is the whole migration.
	if err := after.SaveLLMSettings(ctx, models.LLMSettings{BaseURL: "https://x.example"}); err != nil {
		t.Fatalf("SaveLLMSettings: %v", err)
	}
	if got := storedAPIKey(t, after); strings.Contains(got, credential) {
		t.Error("the credential is still in clear after a save with a key configured")
	}
	// And it still reads — the save that sealed it did not lose it, which is
	// what the "empty means leave it alone" rule has to keep true.
	read, err = after.LLMSettings(ctx)
	if err != nil {
		t.Fatalf("LLMSettings: %v", err)
	}
	if read.APIKey != credential {
		t.Errorf("read %q after sealing, want the credential", read.APIKey)
	}
}

// TestABrokenKeyIsRefusedAtStartup, not at the first decryption — where the
// error would surface in whichever request happened to need a credential,
// far from the configuration that caused it. The same rule the passkey
// relying-party id follows.
func TestABrokenKeyIsRefusedAtStartup(t *testing.T) {
	for _, bad := range []string{"hunter2", "not base64 at all !!", "c2hvcnQ="} {
		_, err := Open(Options{
			Driver: DriverSQLite, DSN: filepath.Join(t.TempDir(), "x.db"), SettingsKey: bad,
		})
		if err == nil {
			t.Errorf("%q was accepted as a settings key", bad)
			continue
		}
		if !strings.Contains(err.Error(), "settings key") {
			t.Errorf("the refusal of %q does not name the setting: %v", bad, err)
		}
	}
}

// TestClearingStillClears, because the sealing path must not turn an empty
// key into a stored ciphertext of nothing — which would read as a credential
// that is present and wrong.
func TestClearingStillClears(t *testing.T) {
	s := keyedStore(t, filepath.Join(t.TempDir(), "test.db"), newKey(t))
	ctx := context.Background()

	if err := s.SaveLLMSettings(ctx, models.LLMSettings{APIKey: "sk-something"}); err != nil {
		t.Fatalf("SaveLLMSettings: %v", err)
	}
	if err := s.ClearLLMAPIKey(ctx); err != nil {
		t.Fatalf("ClearLLMAPIKey: %v", err)
	}

	read, err := s.LLMSettings(ctx)
	if err != nil {
		t.Fatalf("LLMSettings: %v", err)
	}
	if read.APIKey != "" {
		t.Errorf("api key = %q after clearing, want nothing", read.APIKey)
	}
	if got := storedAPIKey(t, s); got != "" {
		t.Errorf("the column holds %q after clearing", got)
	}
}
