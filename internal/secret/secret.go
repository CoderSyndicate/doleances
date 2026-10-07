// Package secret encrypts the credentials this register stores in its own
// database.
//
// # What it is for, and what it is not
//
// The backend keeps secrets in rows: the LLM API key today, an identity
// provider's token next. That is a deliberate trade — it reuses the storage
// the backend already has rather than adding a secret store — and the cost was
// written down plainly: *"The key lives in the database, so a database backup
// contains it. Treat backups accordingly."*
//
// This retires that caveat for the case it was really about. A dump, an old
// backup, a disk somebody forgot to wipe: all inert without the deployment's
// key, which does not live in the database.
//
// **It does nothing against a compromised running backend.** The process must
// hold the key to use the credentials, so whoever reads its memory or its
// environment has both. And the key must not be backed up beside the database,
// or nothing whatever was gained. Neither of those is a flaw in the mechanism;
// they are the limits of what encryption at rest means, and they are worth
// saying in the one place somebody reads before trusting it.
//
// # Authenticated, not merely encrypted
//
// AES-GCM, so a value somebody edited in the database is refused rather than
// decrypted into something. The failure mode this prevents is specific and
// nasty: a settings row quietly altered to point the console at a different
// identity provider would otherwise be an authentication takeover that looks
// like a configuration change.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// KeyBytes is the length of the key, in bytes. AES-256.
const KeyBytes = 32

// prefix marks a value this package wrote.
//
// It earns its place twice. It tells an encrypted value from a plaintext one,
// which is what lets an installation that has been running without a key adopt
// one without a migration step somebody has to remember to run. And it carries
// a version, so a later change of algorithm can read what this one wrote
// instead of failing on every row at once.
const prefix = "enc:v1:"

// ErrNoKey is returned when a Box was built without one.
//
// A nil Box is the normal state of an installation that has not configured a
// key: see Box.Seal.
var ErrNoKey = errors.New("no settings key configured")

// Box seals and opens the values a deployment stores.
//
// A nil *Box is valid and means "no key configured": it stores values in clear
// and reads both clear and — it cannot read encrypted ones, and says so. That
// is deliberate rather than lax. The absence of a key switches encryption off
// the way the absence of VAPID keys switches push off, because refusing to
// start would take a working register down over a credential it had been
// storing in clear perfectly happily the day before. What it must not do is
// pretend: startup logs which of the two it is.
type Box struct {
	aead cipher.AEAD
}

// NewKey generates one, encoded the way a deployment configures it.
//
// Here rather than in a script because the one thing an operator must not do
// is invent a key by hand, and the fastest way to stop them is to make the
// right thing one command away.
func NewKey() (string, error) {
	raw := make([]byte, KeyBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate a settings key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// New builds a Box from a configured key.
//
// An empty key gives a nil Box and no error: that is the unconfigured
// installation, and it is the caller's job to say so at startup.
//
// Anything else must be exactly KeyBytes of base64. A passphrase is refused
// rather than hashed into a key, and the refusal is the point: hashing
// "hunter2" into thirty-two bytes produces something that looks like a key and
// resists nothing, and an operator who was told it was fine would never find
// out. The error says how to make a real one.
func New(key string) (*Box, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil //nolint:nilnil // an unconfigured box is not an error
	}

	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return nil, fmt.Errorf(
			"the settings key is not base64: generate one rather than inventing it")
	}
	if len(raw) != KeyBytes {
		return nil, fmt.Errorf(
			"the settings key is %d bytes, want exactly %d: generate one rather than inventing it",
			len(raw), KeyBytes)
	}

	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, fmt.Errorf("build the cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("build the cipher: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Configured reports whether anything will actually be encrypted.
func (b *Box) Configured() bool { return b != nil && b.aead != nil }

// Seal encrypts a value for storage.
//
// An empty value stays empty rather than becoming a ciphertext of nothing:
// "no key is stored" is a state the rest of the code tests for by comparing
// against the empty string, and a sealed empty value would read as a key that
// is present and wrong.
//
// With no Box the value is returned unchanged. An installation without a key
// stores what it always stored.
func (b *Box) Seal(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !b.Configured() {
		return value, nil
	}

	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("read randomness: %w", err)
	}

	sealed := b.aead.Seal(nonce, nonce, []byte(value), nil)
	return prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Open decrypts a stored value.
//
// # A value with no prefix is returned as it is
//
// That is what lets an installation adopt a key without a migration: rows
// written before it was configured are read as what they are, and the next
// save seals them. Without it, configuring a key would take the register's LLM
// client offline until somebody re-entered a credential they may not have.
//
// # A value with the prefix and no key is an error, loudly
//
// Returning it raw would hand a caller a base64 blob to use as an API key,
// which fails somewhere far away with a message about the wrong thing. The
// honest answer is that this deployment cannot read its own settings, which is
// a configuration fault and should read as one.
func (b *Box) Open(stored string) (string, error) {
	if !strings.HasPrefix(stored, prefix) {
		// Written before a key was configured, or by a deployment that has
		// none. Either way it is the value itself.
		return stored, nil
	}
	if !b.Configured() {
		return "", fmt.Errorf(
			"%w: this value was encrypted and cannot be read without the key it was written with",
			ErrNoKey)
	}

	sealed, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, prefix))
	if err != nil {
		return "", fmt.Errorf("stored value is not readable: %w", err)
	}
	if len(sealed) < b.aead.NonceSize() {
		return "", errors.New("stored value is too short to be anything this wrote")
	}

	nonce, body := sealed[:b.aead.NonceSize()], sealed[b.aead.NonceSize():]
	opened, err := b.aead.Open(nil, nonce, body, nil)
	if err != nil {
		// GCM refuses a wrong key and a tampered ciphertext identically, and
		// the two really are one answer: this deployment cannot read this
		// value, and carrying on would mean acting on something nobody wrote.
		return "", errors.New(
			"stored value does not match the settings key: it was written with a " +
				"different key, or it has been altered")
	}
	return string(opened), nil
}
