package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/token"
)

// TestAChallengeIsSpentWhenItIsAnswered.
//
// The challenge is the security property of the whole exchange: the reason a
// WebAuthn signature means anything is that the server remembers what it asked
// for and asked only once. A challenge that survived its answer would make a
// captured signature replayable for as long as it lived.
func TestAChallengeIsSpentWhenItIsAnswered(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	begun, err := s.BeginCeremony(ctx, "signup", "", []byte("challenge state"), []byte(`{"name":"x"}`))
	if err != nil {
		t.Fatalf("BeginCeremony: %v", err)
	}

	finished, err := s.FinishCeremony(ctx, begun.ID, "signup")
	if err != nil {
		t.Fatalf("FinishCeremony: %v", err)
	}
	if string(finished.Data) != "challenge state" {
		t.Errorf("data = %q, want the state that was kept", finished.Data)
	}
	// The subject travels with it, which is what lets a signup create an
	// account that did not exist when the challenge was issued — without
	// letting the client choose its own handle.
	if string(finished.Subject) != `{"name":"x"}` {
		t.Errorf("subject = %q, want what was remembered", finished.Subject)
	}

	if _, err := s.FinishCeremony(ctx, begun.ID, "signup"); !errors.Is(err, ErrCeremonyNotFound) {
		t.Error("a challenge was answerable twice")
	}
}

// TestAChallengeCannotBeFinishedAsSomethingElse. "Prove you hold a key" and
// "make me a key" are different questions, and an answer to one is not an
// answer to the other.
func TestAChallengeCannotBeFinishedAsSomethingElse(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	begun, err := s.BeginCeremony(ctx, "signin", "", []byte("state"), nil)
	if err != nil {
		t.Fatalf("BeginCeremony: %v", err)
	}
	if _, err := s.FinishCeremony(ctx, begun.ID, "signup"); !errors.Is(err, ErrCeremonyNotFound) {
		t.Errorf("err = %v, want a sign-in challenge to be unusable for a signup", err)
	}
	// And it is still there for its own purpose: refusing the wrong purpose
	// must not spend the challenge.
	if _, err := s.FinishCeremony(ctx, begun.ID, "signin"); err != nil {
		t.Errorf("the refusal spent the challenge: %v", err)
	}
}

// TestAnExpiredChallengeIsGone, swept rather than merely refused: a row that
// authorises something is worthless the moment it lapses, and retention that
// nothing enforces is not retention.
func TestAnExpiredChallengeIsGone(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	begun, err := s.BeginCeremony(ctx, "signin", "", []byte("state"), nil)
	if err != nil {
		t.Fatalf("BeginCeremony: %v", err)
	}
	err = s.DB().Model(&models.WebAuthnCeremony{}).Where("id = ?", begun.ID).
		Update("expires_at", time.Now().Add(-time.Minute)).Error
	if err != nil {
		t.Fatalf("expire the ceremony: %v", err)
	}

	if _, err := s.FinishCeremony(ctx, begun.ID, "signin"); !errors.Is(err, ErrCeremonyNotFound) {
		t.Errorf("err = %v, want an expired challenge to be unusable", err)
	}

	purged, err := s.PurgeCeremonies(ctx)
	if err != nil {
		t.Fatalf("PurgeCeremonies: %v", err)
	}
	if purged != 1 {
		t.Errorf("purged %d, want the expired challenge", purged)
	}
}

// TestADeviceLinkNeedsBothHalves is the whole security argument for the
// linking flow.
//
// The token is single-use and short-lived, so a photographed screen is worth
// little; and the **old** device confirms the new one before anything is
// attached, which is what stops a relayed code — somebody who tricks a person
// into scanning theirs still has to get that person to approve a browser they
// do not recognise. Either half alone is an attack waiting to happen.
func TestADeviceLinkNeedsBothHalves(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	camille := account(t, s, "Camille")

	value, hash, _ := token.New()
	if _, err := s.CreateLinkToken(ctx, camille.ID, hash); err != nil {
		t.Fatalf("CreateLinkToken: %v", err)
	}

	// Unclaimed and unconfirmed: nothing to approve and nothing to spend.
	if _, err := s.PendingLink(ctx, camille.ID); !errors.Is(err, ErrLinkNotFound) {
		t.Errorf("err = %v, want nothing waiting yet", err)
	}
	if _, err := s.SpendLinkToken(ctx, token.Hash(value)); !errors.Is(err, ErrLinkNotFound) {
		t.Error("an unapproved link was spendable")
	}

	// The new device claims it, which establishes nothing except that the code
	// was real — a fact the caller already had.
	claimed, err := s.ClaimLinkToken(ctx, token.Hash(value), "Firefox on Android")
	if err != nil {
		t.Fatalf("ClaimLinkToken: %v", err)
	}
	if claimed.ConfirmedAt != nil {
		t.Error("claiming a link confirmed it")
	}
	if _, err := s.SpendLinkToken(ctx, token.Hash(value)); !errors.Is(err, ErrLinkNotFound) {
		t.Error("a claimed but unapproved link was spendable")
	}

	waiting, err := s.PendingLink(ctx, camille.ID)
	if err != nil {
		t.Fatalf("PendingLink: %v", err)
	}
	// The label is the new device's own claim, shown to whoever approves it and
	// never treated as evidence.
	if waiting.ClaimLabel != "Firefox on Android" {
		t.Errorf("label = %q, want what the device said", waiting.ClaimLabel)
	}

	// Somebody else's account cannot approve it.
	dominique := account(t, s, "Dominique")
	if err := s.ConfirmLink(ctx, dominique.ID, waiting.ID); !errors.Is(err, ErrLinkNotFound) {
		t.Errorf("err = %v, want another account to be unable to approve", err)
	}

	if err := s.ConfirmLink(ctx, camille.ID, waiting.ID); err != nil {
		t.Fatalf("ConfirmLink: %v", err)
	}

	found, err := s.SpendLinkToken(ctx, token.Hash(value))
	if err != nil {
		t.Fatalf("SpendLinkToken: %v", err)
	}
	if found.ID != camille.ID {
		t.Error("the link resolved to the wrong account")
	}
	// Spent. The code is on a screen being photographed; its whole safety is
	// being worth nothing a second time.
	if _, err := s.SpendLinkToken(ctx, token.Hash(value)); !errors.Is(err, ErrLinkNotFound) {
		t.Error("a link was spendable twice")
	}
}

// TestRefusingALinkDestroysIt. This is the case the flow exists for: somebody
// who did not start a linking sees a prompt for a browser they do not
// recognise and says no, and the code must not still work afterwards.
func TestRefusingALinkDestroysIt(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	camille := account(t, s, "Camille")

	value, hash, _ := token.New()
	if _, err := s.CreateLinkToken(ctx, camille.ID, hash); err != nil {
		t.Fatalf("CreateLinkToken: %v", err)
	}
	claimed, err := s.ClaimLinkToken(ctx, token.Hash(value), "a browser")
	if err != nil {
		t.Fatalf("ClaimLinkToken: %v", err)
	}

	if err := s.RejectLink(ctx, camille.ID, claimed.ID); err != nil {
		t.Fatalf("RejectLink: %v", err)
	}
	if _, err := s.LinkProgress(ctx, token.Hash(value)); !errors.Is(err, ErrLinkNotFound) {
		t.Error("a refused link still answers")
	}
	if _, err := s.SpendLinkToken(ctx, token.Hash(value)); !errors.Is(err, ErrLinkNotFound) {
		t.Error("a refused link was spendable")
	}
}

// TestALinkTokenCanOnlyBeClaimedOnce, so a code somebody photographed off a
// screen cannot be raced against the person it was meant for.
func TestALinkTokenCanOnlyBeClaimedOnce(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	camille := account(t, s, "Camille")

	value, hash, _ := token.New()
	if _, err := s.CreateLinkToken(ctx, camille.ID, hash); err != nil {
		t.Fatalf("CreateLinkToken: %v", err)
	}
	if _, err := s.ClaimLinkToken(ctx, token.Hash(value), "first"); err != nil {
		t.Fatalf("ClaimLinkToken: %v", err)
	}
	if _, err := s.ClaimLinkToken(ctx, token.Hash(value), "second"); !errors.Is(err, ErrLinkNotFound) {
		t.Error("a link was claimed twice")
	}
}
