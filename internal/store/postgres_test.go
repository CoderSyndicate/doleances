package store

import (
	"context"
	"os"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// postgresDSN is the environment variable that switches these on.
//
// They are skipped when it is unset, which is almost always: `--database-driver`
// defaults to sqlite and so does every other test in this package, because a
// file is free and a server is not. CI sets it against a throwaway container.
const postgresDSN = "DOLEANCES_TEST_POSTGRES_DSN"

// openPostgres connects to the CI database, or skips.
func openPostgres(t *testing.T) *Store {
	t.Helper()

	dsn := os.Getenv(postgresDSN)
	if dsn == "" {
		t.Skipf("set %s to run this against a real PostgreSQL", postgresDSN)
	}

	s, err := Open(Options{Driver: DriverPostgres, DSN: dsn})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close(context.Background()) }) //nolint:errcheck
	return s
}

// TestTheSchemaMigratesOnPostgres is the test whose absence took a deployment
// down.
//
// Five fields pinned `type:blob`. SQLite has `blob` and PostgreSQL does not —
// `bytea` is its equivalent — so `AutoMigrate` died on its first statement with
// `type "blob" does not exist`, the backend crash-looped, and the frontend hung
// waiting on an API that was never going to answer. Every test and every
// development run had passed, because all of them use SQLite.
//
// `Migrate()` is documented as having to "produce the same result on both
// engines". This is the first thing that checks it rather than asserting it.
//
// It needs a server, so it is skipped unless one is configured — which means
// the cheap guard in schema_test.go is still what catches the common case at
// the moment somebody writes the tag. This catches what a tag scan cannot: DDL
// the engine parses differently, an index on a type that cannot carry one, a
// default one side rejects.
func TestTheSchemaMigratesOnPostgres(t *testing.T) {
	s := openPostgres(t)

	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate on PostgreSQL: %v", err)
	}

	// Again, because a deployment is almost never the first migration. The
	// second run has to be a no-op rather than an error — AutoMigrate comparing
	// what it finds against what it wants is exactly where a type that reads
	// back differently than it was written causes an endless ALTER, or a
	// failure on every restart after the first.
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate is not idempotent on PostgreSQL: %v", err)
	}
}

// TestBytesSurvivePostgres covers the half a migration cannot.
//
// The schema being accepted says the column exists; it does not say anything
// written to it comes back. These are the five columns the outage was about,
// and the one that matters most is the public key: it verifies WebAuthn
// signatures, so a byte out of place there is an account nobody can sign in to
// — discovered by the person it happens to, long after a deployment.
func TestBytesSurvivePostgres(t *testing.T) {
	s := openPostgres(t)
	ctx := context.Background()

	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Every byte value, so a driver that mishandled a NUL, a high bit or an
	// invalid UTF-8 sequence has nowhere to hide. This is the shape of a key
	// rather than text, which is the point: `bytea` has to be transparent.
	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = byte(i)
	}

	handle := append([]byte("handle-"), payload...)
	account := &models.Account{Handle: handle, Name: "Dominique"}
	credential := &models.Credential{
		CredentialID: append([]byte("cred-"), payload[:64]...),
		PublicKey:    payload,
		Name:         "a device",
	}
	if err := s.CreateAccount(ctx, account, credential); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	t.Cleanup(func() { s.DeleteAccount(ctx, account.ID) }) //nolint:errcheck

	var stored models.Credential
	err := s.DB().First(&stored, "account_id = ?", account.ID).Error
	if err != nil {
		t.Fatalf("read the credential back: %v", err)
	}

	if len(stored.PublicKey) != len(payload) {
		t.Fatalf("public key came back %d bytes, want %d", len(stored.PublicKey), len(payload))
	}
	for i := range payload {
		if stored.PublicKey[i] != payload[i] {
			t.Fatalf("public key byte %d came back %#x, want %#x",
				i, stored.PublicKey[i], payload[i])
		}
	}

	// And the theme asset, which is the only one of the five that holds
	// something a reader sees: an image that came back altered is a broken
	// logo on every page.
	asset := models.ThemeAsset{
		ThemeName:   "test-theme",
		Slot:        "logo",
		ContentType: "image/png",
		Data:        payload,
	}
	if err := s.DB().Create(&asset).Error; err != nil {
		t.Fatalf("store a theme asset: %v", err)
	}
	t.Cleanup(func() { s.DB().Delete(&models.ThemeAsset{}, "id = ?", asset.ID) })

	var readBack models.ThemeAsset
	if err := s.DB().First(&readBack, "id = ?", asset.ID).Error; err != nil {
		t.Fatalf("read the asset back: %v", err)
	}
	if len(readBack.Data) != len(payload) {
		t.Errorf("asset came back %d bytes, want %d", len(readBack.Data), len(payload))
	}
}

// TestBothOrdersWorkOnPostgres.
//
// The register offers a shuffled read and a paged one, and the shuffled one
// leans on `random()` — a function both engines have and spell the same way,
// which is the only reason it is allowed here at all. Their *seeding* is not
// portable (PostgreSQL has setseed(), SQLite has nothing), and that asymmetry
// is what decided the design, so the portable half is worth proving on the
// engine production actually runs rather than assuming ANSI goodwill.
//
// This is the lesson of `type:blob` applied before the fact instead of after.
func TestBothOrdersWorkOnPostgres(t *testing.T) {
	s := openPostgres(t)
	ctx := context.Background()
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	for i := 0; i < 60; i++ {
		m := models.Message{Text: "doléance", Status: models.StatusAccepted, TokenHash: "h"}
		if err := s.CreateMessage(ctx, &m); err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
	}

	// Shuffled: it runs, it fills a page, and two reads differ. The last is
	// what `random()` is for, and a silent fallback to insertion order would
	// otherwise look identical to success.
	first, _, err := s.FindMessages(ctx, MessageQuery{Shuffled: true, Limit: 40})
	if err != nil {
		t.Fatalf("shuffled read: %v", err)
	}
	if len(first) != 40 {
		t.Fatalf("shuffled read returned %d, want 40", len(first))
	}
	second, _, err := s.FindMessages(ctx, MessageQuery{Shuffled: true, Limit: 40})
	if err != nil {
		t.Fatalf("second shuffled read: %v", err)
	}
	same := true
	for i := range first {
		if first[i].ID != second[i].ID {
			same = false
			break
		}
	}
	if same {
		t.Error("two shuffled reads were identical: random() is not shuffling on this engine")
	}

	// Paged: offsets walk without repeating, which is the property the
	// shuffled read cannot have and the reason there are two.
	page1, _, err := s.FindMessages(ctx, MessageQuery{Limit: 40})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	page2, _, err := s.FindMessages(ctx, MessageQuery{Limit: 40, Offset: 40})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	seen := map[string]bool{}
	for _, m := range page1 {
		seen[m.ID] = true
	}
	for _, m := range page2 {
		if seen[m.ID] {
			t.Errorf("page 2 repeated %s from page 1", m.ID)
		}
	}
}
