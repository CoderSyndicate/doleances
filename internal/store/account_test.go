package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/token"
)

// TestAnAccountAndItsFirstPasskeyAreOneFact.
//
// An account with no credential cannot be signed into and would sit in the
// table for ever as a row nobody can reach; a credential with no account has
// nothing to authenticate. So a registration that fails at the last step has
// to leave nothing behind.
func TestAnAccountAndItsFirstPasskeyAreOneFact(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	created := &models.Account{Handle: []byte("a-handle"), Name: "Camille"}
	first := &models.Credential{
		CredentialID: []byte("a-credential"),
		PublicKey:    []byte("a-key"),
		Name:         "a phone",
	}
	if err := s.CreateAccount(ctx, created, first); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	credentials, err := s.ListCredentials(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListCredentials: %v", err)
	}
	if len(credentials) != 1 {
		t.Fatalf("%d passkeys, want the one that was registered", len(credentials))
	}

	// A duplicate handle fails, and the credential must not survive it.
	clash := &models.Account{Handle: []byte("a-handle")}
	err = s.CreateAccount(ctx, clash, &models.Credential{
		CredentialID: []byte("another-credential"), PublicKey: []byte("k"),
	})
	if err == nil {
		t.Fatal("two accounts were allowed the same handle")
	}

	var orphans int64
	s.DB().Model(&models.Credential{}). //nolint:errcheck
						Where("credential_id = ?", []byte("another-credential")).Count(&orphans)
	if orphans != 0 {
		t.Error("a failed registration left a credential behind")
	}
}

// TestASignInFindsItsAccountEitherWay.
//
// By handle is the usernameless path: a discoverable credential hands one
// back. By credential identifier is the fallback for an authenticator that
// does not — an older security key, mostly.
func TestASignInFindsItsAccountEitherWay(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	camille := account(t, s, "Camille")

	found, err := s.AccountByHandle(ctx, camille.Handle)
	if err != nil {
		t.Fatalf("AccountByHandle: %v", err)
	}
	if found.ID != camille.ID {
		t.Error("the handle resolved to the wrong account")
	}
	// The credentials come with it, because verifying an assertion means
	// finding the public key of the credential that signed it.
	if len(found.Credentials) != 1 {
		t.Error("the account came back without its passkeys")
	}

	byCredential, err := s.AccountByCredential(ctx, found.Credentials[0].CredentialID)
	if err != nil {
		t.Fatalf("AccountByCredential: %v", err)
	}
	if byCredential.ID != camille.ID {
		t.Error("the credential resolved to the wrong account")
	}

	// Everything unknown is one answer. An attacker who could tell "this
	// credential is unknown" from "this account has no such credential" could
	// enumerate the register's members one guess at a time.
	for _, wrong := range [][]byte{nil, {}, []byte("not-a-handle")} {
		if _, err := s.AccountByHandle(ctx, wrong); !errors.Is(err, ErrAccountNotFound) {
			t.Errorf("AccountByHandle(%q) = %v, want ErrAccountNotFound", wrong, err)
		}
		if _, err := s.AccountByCredential(ctx, wrong); !errors.Is(err, ErrAccountNotFound) {
			t.Errorf("AccountByCredential(%q) = %v, want ErrAccountNotFound", wrong, err)
		}
	}
}

// TestTheLastPasskeyCannotBeRemoved.
//
// There is no address to mail a reset to, so a passkey list that reaches zero
// is an account nobody can ever sign into again — including the person who
// owns it. Removing the last one is deleting the account, and that is a
// different button with a different confirmation.
func TestTheLastPasskeyCannotBeRemoved(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	camille := account(t, s, "Camille")

	only, err := s.ListCredentials(ctx, camille.ID)
	if err != nil {
		t.Fatalf("ListCredentials: %v", err)
	}
	err = s.DeleteCredential(ctx, camille.ID, only[0].ID)
	if !errors.Is(err, ErrLastCredential) {
		t.Fatalf("err = %v, want ErrLastCredential", err)
	}

	// With a second one in place the first may go, which is how somebody
	// replaces a lost device rather than being stuck with it for ever.
	second := &models.Credential{
		AccountID: camille.ID, CredentialID: []byte("a-second"), PublicKey: []byte("k"),
	}
	if err := s.AddCredential(ctx, second); err != nil {
		t.Fatalf("AddCredential: %v", err)
	}
	if err := s.DeleteCredential(ctx, camille.ID, only[0].ID); err != nil {
		t.Errorf("DeleteCredential: %v", err)
	}
}

// TestAPasskeyBelongsToOneAccount. Somebody else's credential identifier must
// not be a way to remove or rename their device.
func TestAPasskeyBelongsToOneAccount(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	camille := account(t, s, "Camille")
	dominique := account(t, s, "Dominique")

	theirs, _ := s.ListCredentials(ctx, camille.ID)

	err := s.RenameCredential(ctx, dominique.ID, theirs[0].ID, "mine now")
	if !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("err = %v, want ErrAccountNotFound", err)
	}
	// Two passkeys on the other account, so the last-credential guard is not
	// what refuses this.
	if err := s.AddCredential(ctx, &models.Credential{
		AccountID: dominique.ID, CredentialID: []byte("second"), PublicKey: []byte("k"),
	}); err != nil {
		t.Fatalf("AddCredential: %v", err)
	}
	if err := s.DeleteCredential(ctx, dominique.ID, theirs[0].ID); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("err = %v, want ErrAccountNotFound", err)
	}
}

// TestASignInRecordsWhatTheAuthenticatorReported.
//
// The sign counter is a clone detector and is recorded rather than enforced:
// every synced passkey reports zero for ever, so refusing a stalled counter
// would lock out most of the people passkeys exist for.
func TestASignInRecordsWhatTheAuthenticatorReported(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	camille := account(t, s, "Camille")
	credentials, _ := s.ListCredentials(ctx, camille.ID)

	err := s.RecordCredentialUse(ctx, credentials[0].CredentialID, 0, true)
	if err != nil {
		t.Fatalf("RecordCredentialUse: %v", err)
	}

	after, _ := s.ListCredentials(ctx, camille.ID)
	if after[0].LastUsedAt == nil {
		t.Error("a sign-in was not recorded")
	}
	if !after[0].BackedUp {
		t.Error("the backup state was not recorded")
	}
	// Zero is accepted and stays zero. A store that refused it would be a
	// store no synced passkey could sign into twice.
	if after[0].SignCount != 0 {
		t.Errorf("sign count = %d, want the reported 0", after[0].SignCount)
	}
}

// TestARecoveryCodeWorksOnce.
//
// It is the only way back when every device is gone, and the only credential
// in this project that a person holds on paper. Single use, because a code
// somebody has written down and then used is a code that may have been read
// over their shoulder.
func TestARecoveryCodeWorksOnce(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	camille := account(t, s, "Camille")

	value, hash, err := token.Recovery()
	if err != nil {
		t.Fatalf("token.Recovery: %v", err)
	}
	if err := s.StoreRecoveryCodes(ctx, camille.ID, []string{hash}); err != nil {
		t.Fatalf("StoreRecoveryCodes: %v", err)
	}

	found, err := s.RedeemRecoveryCode(ctx, token.HashRecovery(value))
	if err != nil {
		t.Fatalf("RedeemRecoveryCode: %v", err)
	}
	if found.ID != camille.ID {
		t.Error("the code resolved to the wrong account")
	}

	// Spent. And a wrong code answers exactly as a spent one, so nothing can
	// be learned by trying.
	for _, wrong := range []string{token.HashRecovery(value), token.HashRecovery("AAAA-BBBB-CCCC-DDDD")} {
		if _, err := s.RedeemRecoveryCode(ctx, wrong); !errors.Is(err, ErrAccountNotFound) {
			t.Errorf("err = %v, want ErrAccountNotFound", err)
		}
	}

	left, err := s.CountRecoveryCodes(ctx, camille.ID)
	if err != nil {
		t.Fatalf("CountRecoveryCodes: %v", err)
	}
	if left != 0 {
		t.Errorf("%d codes left, want 0", left)
	}
}

// TestFreshCodesInvalidateTheOldSheet. Regenerating them achieves nothing if
// the sheet somebody decided to replace still works.
func TestFreshCodesInvalidateTheOldSheet(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	camille := account(t, s, "Camille")

	old, oldHash, _ := token.Recovery()
	if err := s.StoreRecoveryCodes(ctx, camille.ID, []string{oldHash}); err != nil {
		t.Fatalf("StoreRecoveryCodes: %v", err)
	}
	_, freshHash, _ := token.Recovery()
	if err := s.StoreRecoveryCodes(ctx, camille.ID, []string{freshHash}); err != nil {
		t.Fatalf("StoreRecoveryCodes: %v", err)
	}

	if _, err := s.RedeemRecoveryCode(ctx, token.HashRecovery(old)); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("err = %v, want the replaced code to be dead", err)
	}
}

// TestDeletingAnAccountLeavesTheCountsStanding.
//
// This is the erasure path, and the shape of it is the decision the register
// already made: **the identity goes, the counts stay.** A membership row with
// no account reads as an anonymous attendance — which is the default
// presentation anyway — so a group's history stays accurate while the person
// becomes unidentifiable.
func TestDeletingAnAccountLeavesTheCountsStanding(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")
	dominique := account(t, s, "Dominique")

	if err := s.JoinGroup(ctx, group.ID, dominique.ID); err != nil {
		t.Fatalf("JoinGroup: %v", err)
	}
	_, hash, _ := token.New()
	if _, err := s.CreateSession(ctx, dominique.ID, hash, "a phone"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	err := s.Subscribe(ctx, &models.PushSubscription{
		AccountID: dominique.ID, Endpoint: "https://push.example/1",
		P256dh: "p", Auth: "a",
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	err = s.Notify(ctx, &models.Notification{
		AccountID: dominique.ID, Kind: models.NotifyGroupMessage, Title: "something",
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	err = s.WriteToGroup(ctx, &models.GroupMessage{
		GroupID: group.ID, FromAccountID: dominique.ID, FromName: "Dominique", Text: "hello",
	})
	if err != nil {
		t.Fatalf("WriteToGroup: %v", err)
	}

	if err := s.DeleteAccount(ctx, dominique.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}

	// Everything that could point back at them is really gone.
	for name, model := range map[string]any{
		"passkeys":      &models.Credential{},
		"sessions":      &models.Session{},
		"subscriptions": &models.PushSubscription{},
		"notifications": &models.Notification{},
	} {
		var left int64
		s.DB().Model(model).Where("account_id = ?", dominique.ID).Count(&left) //nolint:errcheck
		if left != 0 {
			t.Errorf("%d %s survived the account", left, name)
		}
	}
	// What they said to a group stays and stops naming them. Deleting it
	// instead would be inconsistent: the notification that quoted it went to an
	// admin, so the text would survive in the less visible of the two places.
	var wrote []models.GroupMessage
	if err := s.DB().Find(&wrote).Error; err != nil {
		t.Fatalf("read the group messages: %v", err)
	}
	if len(wrote) != 1 {
		t.Fatalf("%d group messages, want the one that was written", len(wrote))
	}
	if wrote[0].Text != "hello" {
		t.Errorf("text = %q, want what was said", wrote[0].Text)
	}
	if wrote[0].FromAccountID != "" || wrote[0].FromName != "" {
		t.Errorf("a message still names its author: %+v", wrote[0])
	}

	// What stays is that somebody was in the group — without saying who.
	members, err := s.ListGroupMembers(ctx, group.ID)
	if err != nil {
		t.Fatalf("ListGroupMembers: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("%d members, want the attendance to survive", len(members))
	}
	for _, member := range members {
		if member.AccountID == dominique.ID {
			t.Error("a membership still names the account that was deleted")
		}
	}
	if _, err := s.Account(ctx, dominique.ID); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("err = %v, want the account to be gone", err)
	}
}

// TestASessionRenewsWhileItIsUsedAndExpiresWhenItIsNot.
func TestASessionRenewsWhileItIsUsedAndExpiresWhenItIsNot(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	camille := account(t, s, "Camille")

	value, hash, _ := token.New()
	session, err := s.CreateSession(ctx, camille.ID, hash, "a laptop")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	found, resolved, err := s.Authenticate(ctx, token.Hash(value))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if found.ID != camille.ID || resolved.ID != session.ID {
		t.Error("the token resolved to the wrong account or session")
	}
	// Fresh, so nothing is extended: renewing on every request would write a
	// row for every page view to record a fact that changes by minutes.
	if !resolved.ExpiresAt.Equal(session.ExpiresAt) {
		t.Error("a fresh session was extended")
	}

	// Past half its life it is extended, which is what keeps somebody reading
	// through an afternoon from being logged out mid-sentence.
	aged := time.Now().Add(-SessionRenewAfter - time.Minute)
	err = s.DB().Model(&models.Session{}).Where("id = ?", session.ID).
		Update("expires_at", aged.Add(SessionLifetime)).Error
	if err != nil {
		t.Fatalf("age the session: %v", err)
	}
	_, renewed, err := s.Authenticate(ctx, token.Hash(value))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !renewed.ExpiresAt.After(session.ExpiresAt) {
		t.Error("a session most of the way through its life was not extended")
	}

	// An expired one is not merely refused, it is deleted: the row is
	// worthless and keeping it is keeping a record of which device somebody
	// used and when.
	err = s.DB().Model(&models.Session{}).Where("id = ?", session.ID).
		Update("expires_at", time.Now().Add(-time.Hour)).Error
	if err != nil {
		t.Fatalf("expire the session: %v", err)
	}
	if _, _, err := s.Authenticate(ctx, token.Hash(value)); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("err = %v, want ErrSessionNotFound", err)
	}
	var left int64
	s.DB().Model(&models.Session{}).Where("id = ?", session.ID).Count(&left) //nolint:errcheck
	if left != 0 {
		t.Error("an expired session was kept")
	}
}

// TestSigningOneDeviceOutLeavesTheOthers. It is the whole reason sessions are
// one row per device.
func TestSigningOneDeviceOutLeavesTheOthers(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	camille := account(t, s, "Camille")

	tokens := map[string]string{}
	ids := map[string]string{}
	for _, label := range []string{"a phone", "a laptop"} {
		value, hash, _ := token.New()
		created, err := s.CreateSession(ctx, camille.ID, hash, label)
		if err != nil {
			t.Fatalf("CreateSession(%q): %v", label, err)
		}
		tokens[label], ids[label] = value, created.ID
	}

	sessions, err := s.ListSessions(ctx, camille.ID)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("%d sessions, want 2", len(sessions))
	}

	if err := s.DeleteSession(ctx, camille.ID, ids["a phone"]); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, _, err := s.Authenticate(ctx, token.Hash(tokens["a laptop"])); err != nil {
		t.Errorf("signing one device out signed another out too: %v", err)
	}
	if _, _, err := s.Authenticate(ctx, token.Hash(tokens["a phone"])); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("err = %v, want the signed-out device to be refused", err)
	}

	// And somebody else's session identifier is not a way to sign them out.
	dominique := account(t, s, "Dominique")
	err = s.DeleteSession(ctx, dominique.ID, ids["a laptop"])
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("err = %v, want ErrSessionNotFound", err)
	}
}

// TestAReadDoesNotWriteOnEveryRequest.
//
// `last_seen_at` used to be written on every authenticated request — one
// UPDATE per page view by a signed-in reader, to store a fact that is only
// ever read on a device list, in days and minutes.
//
// It is the only write this register performs on a read path, which is what
// makes it worth a test: a cache can relieve reads, and nothing relieves a
// write that happens because somebody looked at a page.
func TestAReadDoesNotWriteOnEveryRequest(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	camille := account(t, s, "Camille")

	value, hash, _ := token.New()
	created, err := s.CreateSession(ctx, camille.ID, hash, "a laptop")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// A fresh session has just recorded itself, so reading it again changes
	// nothing in the row.
	before := rowVersion(t, s, created.ID)
	for range 20 {
		if _, _, err := s.Authenticate(ctx, token.Hash(value)); err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
	}
	if after := rowVersion(t, s, created.ID); after != before {
		t.Errorf("twenty reads wrote to the session row: %v then %v", before, after)
	}

	// What the caller is told is still now, whether or not it was written:
	// the session is being used, the row is simply not re-stating it.
	_, session, err := s.Authenticate(ctx, token.Hash(value))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if time.Since(session.LastSeenAt) > time.Minute {
		t.Errorf("last seen = %v, want now", session.LastSeenAt)
	}

	// Past the resolution the device list is shown at, it is recorded again —
	// or the column would stop being true rather than merely coarse.
	stale := time.Now().Add(-seenResolution - time.Minute)
	err = s.DB().Model(&models.Session{}).Where("id = ?", created.ID).
		Update("last_seen_at", stale).Error
	if err != nil {
		t.Fatalf("age the session: %v", err)
	}
	if _, _, err := s.Authenticate(ctx, token.Hash(value)); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	var written models.Session
	if err := s.DB().First(&written, "id = ?", created.ID).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if time.Since(written.LastSeenAt) > time.Minute {
		t.Errorf("a stale last_seen_at was not refreshed: %v", written.LastSeenAt)
	}
}

// rowVersion is what the session row currently says about itself, so a test
// can tell whether anything wrote to it.
func rowVersion(t *testing.T, s *Store, id string) [2]time.Time {
	t.Helper()

	var session models.Session
	if err := s.DB().First(&session, "id = ?", id).Error; err != nil {
		t.Fatalf("read the session: %v", err)
	}
	return [2]time.Time{session.LastSeenAt, session.UpdatedAt}
}
