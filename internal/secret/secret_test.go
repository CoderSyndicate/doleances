package secret

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func box(t *testing.T) *Box {
	t.Helper()

	key, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	b, err := New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

func TestAValueComesBack(t *testing.T) {
	b := box(t)

	const key = "sk-a-credential-somebody-pasted-into-the-console"
	sealed, err := b.Seal(key)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if strings.Contains(sealed, "credential") {
		t.Fatal("the stored value contains the plaintext")
	}

	opened, err := b.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened != key {
		t.Errorf("opened %q, want the value that was sealed", opened)
	}
}

// TestTheSameValueSealsDifferentlyEveryTime is the nonce doing its job.
//
// Without it, two installations with the same key and the same credential
// store identical bytes — and so does one installation before and after a
// change, which tells anybody reading a sequence of backups when the
// credential was rotated and when it was not.
func TestTheSameValueSealsDifferentlyEveryTime(t *testing.T) {
	b := box(t)

	first, err := b.Seal("the same key")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	second, err := b.Seal("the same key")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if first == second {
		t.Error("sealing the same value twice produced the same bytes")
	}

	// And both still open.
	for _, sealed := range []string{first, second} {
		opened, err := b.Open(sealed)
		if err != nil || opened != "the same key" {
			t.Errorf("Open = %q, %v", opened, err)
		}
	}
}

// TestATamperedValueIsRefused is why this is AES-GCM and not something that
// merely encrypts.
//
// The failure it prevents is specific: a settings row quietly altered in the
// database to point the console at a different identity provider would be an
// authentication takeover that looks like a configuration change. Authenticated
// encryption makes that a refusal rather than a silent new value.
func TestATamperedValueIsRefused(t *testing.T) {
	b := box(t)

	sealed, err := b.Seal("https://authentik.example")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, prefix))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// One bit, in the body rather than the nonce.
	raw[len(raw)-1] ^= 0x01
	altered := prefix + base64.StdEncoding.EncodeToString(raw)

	if _, err := b.Open(altered); err == nil {
		t.Error("a value somebody edited in the database was accepted")
	}
}

// TestTheWrongKeyIsRefused rather than yielding rubbish a caller would then
// use as a credential.
func TestTheWrongKeyIsRefused(t *testing.T) {
	sealed, err := box(t).Seal("sk-the-original")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if _, err := box(t).Open(sealed); err == nil {
		t.Error("a value written with a different key was read")
	}
}

// TestAPlaintextValueIsReadAsItself, which is what lets an installation adopt
// a key without a migration somebody has to remember to run.
//
// Rows written before a key was configured are the value itself. Without this,
// setting the key would take the LLM client offline until somebody re-entered
// a credential they may no longer have.
func TestAPlaintextValueIsReadAsItself(t *testing.T) {
	legacy := "sk-stored-before-there-was-a-key"

	opened, err := box(t).Open(legacy)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened != legacy {
		t.Errorf("opened %q, want the plaintext unchanged", opened)
	}
}

// TestAnEncryptedValueWithNoKeySaysSo.
//
// The tempting alternative is to hand the ciphertext back, and it is wrong:
// the caller would use a base64 blob as an API key and the failure would
// surface somewhere far away, as a message about the wrong thing. This
// deployment cannot read its own settings, which is a configuration fault and
// should read as one.
func TestAnEncryptedValueWithNoKeySaysSo(t *testing.T) {
	sealed, err := box(t).Seal("sk-a-credential")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	var none *Box
	opened, err := none.Open(sealed)
	if !errors.Is(err, ErrNoKey) {
		t.Errorf("err = %v, want ErrNoKey", err)
	}
	if opened != "" {
		t.Errorf("opened %q, want nothing at all", opened)
	}
}

// TestNoKeyStoresWhatItAlwaysStored. The absence of a key switches encryption
// off rather than failing, the way absent VAPID keys switch push off: refusing
// to start would take a working register down over a credential it had been
// storing in clear perfectly happily the day before.
func TestNoKeyStoresWhatItAlwaysStored(t *testing.T) {
	var none *Box

	if none.Configured() {
		t.Error("a box with no key reports itself configured")
	}

	sealed, err := none.Seal("sk-a-credential")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if sealed != "sk-a-credential" {
		t.Errorf("stored %q, want the value unchanged", sealed)
	}
	if strings.HasPrefix(sealed, prefix) {
		t.Error("an unconfigured box marked a value as encrypted")
	}
}

// TestNothingStaysNothing. "No key is stored" is a state the rest of the code
// tests by comparing against the empty string — SaveLLMSettings reads an empty
// key as "leave the stored one alone" — and a sealed empty value would read as
// a key that is present and wrong.
func TestNothingStaysNothing(t *testing.T) {
	sealed, err := box(t).Seal("")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if sealed != "" {
		t.Errorf("sealing nothing produced %q", sealed)
	}
}

// TestAPassphraseIsRefused, rather than hashed into something that looks like
// a key and resists nothing. An operator told that was fine would never find
// out, so the refusal names the remedy.
func TestAPassphraseIsRefused(t *testing.T) {
	for _, bad := range []string{
		"hunter2",
		"a-nice-long-passphrase-that-is-not-base64!",
		base64.StdEncoding.EncodeToString([]byte("too short")),
		base64.StdEncoding.EncodeToString(make([]byte, 16)),
	} {
		b, err := New(bad)
		if err == nil {
			t.Errorf("%q was accepted as a settings key", bad)
		}
		if b != nil {
			t.Errorf("%q produced a usable box", bad)
		}
		if err != nil && !strings.Contains(err.Error(), "generate one") {
			t.Errorf("the refusal of %q does not say how to make a real key: %v", bad, err)
		}
	}
}

// TestAnEmptyKeyIsTheUnconfiguredInstallation, not an error. That is the
// difference between "this deployment has not set one" and "this deployment
// set a broken one", and only the second should stop anything.
func TestAnEmptyKeyIsTheUnconfiguredInstallation(t *testing.T) {
	for _, blank := range []string{"", "   ", "\t\n"} {
		b, err := New(blank)
		if err != nil {
			t.Errorf("New(%q) = %v, want no error", blank, err)
		}
		if b.Configured() {
			t.Errorf("New(%q) produced a configured box", blank)
		}
	}
}

// TestAGeneratedKeyIsAcceptedByNew closes the loop: the one command an
// operator is told to run has to produce something this package takes.
func TestAGeneratedKeyIsAcceptedByNew(t *testing.T) {
	key, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}

	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		t.Fatalf("a generated key is not base64: %v", err)
	}
	if len(raw) != KeyBytes {
		t.Errorf("a generated key is %d bytes, want %d", len(raw), KeyBytes)
	}

	b, err := New(key)
	if err != nil || !b.Configured() {
		t.Errorf("New on a generated key: %v", err)
	}

	// And two of them differ, or it is not generating anything.
	other, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	if other == key {
		t.Error("two generated keys are the same")
	}
}
