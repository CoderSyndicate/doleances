package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// ErrAccountNotFound means no account answers to that.
//
// It covers a handle nobody holds, a credential nobody registered and an
// identifier that was never issued, deliberately: a sign-in that failed must
// not say *which* part of it was unrecognised. An attacker who can tell "this
// credential is unknown" from "this account has no such credential" can
// enumerate the register's members one guess at a time.
var ErrAccountNotFound = errors.New("no such account")

// ErrLastCredential refuses to remove somebody's only way in.
//
// There is no address to mail a reset to, so a passkey list that reaches zero
// is an account nobody can ever sign into again — including the person who
// owns it. Removing the last one is deleting the account, and that is a
// different button with a different confirmation.
var ErrLastCredential = errors.New("that is the only passkey on this account")

// CreateAccount writes a new account and its first passkey together.
//
// One transaction, because the two halves are meaningless apart: an account
// with no credential cannot be signed into and would sit in the table for ever
// as a row nobody can reach, and a credential with no account has nothing to
// authenticate. A registration ceremony that fails at the last step must leave
// nothing behind.
func (s *Store) CreateAccount(ctx context.Context, account *models.Account, first *models.Credential) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		account.LastSeenAt = time.Now()
		if err := tx.Create(account).Error; err != nil {
			return err
		}
		first.AccountID = account.ID
		return tx.Create(first).Error
	})
}

// Account reads one by its identifier.
func (s *Store) Account(ctx context.Context, id string) (models.Account, error) {
	var account models.Account
	err := s.db.WithContext(ctx).First(&account, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.Account{}, ErrAccountNotFound
	}
	return account, err
}

// AccountWithCredentials reads an account and its passkeys.
//
// The credentials come with it because the WebAuthn library asks for them:
// verifying an assertion means finding the public key of the credential that
// signed it, and a login that fetched them separately would be two round trips
// to answer one question.
func (s *Store) AccountWithCredentials(ctx context.Context, id string) (models.Account, error) {
	var account models.Account
	err := s.db.WithContext(ctx).Preload("Credentials").First(&account, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.Account{}, ErrAccountNotFound
	}
	return account, err
}

// AccountByHandle finds an account by the user handle an authenticator
// returned.
//
// This is the usernameless path: nobody typed anything, the browser offered
// the credentials it holds for this site, and the assertion came back carrying
// the handle stored beside the chosen one.
func (s *Store) AccountByHandle(ctx context.Context, handle []byte) (models.Account, error) {
	if len(handle) == 0 {
		return models.Account{}, ErrAccountNotFound
	}

	var account models.Account
	err := s.db.WithContext(ctx).Preload("Credentials").
		First(&account, "handle = ?", handle).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.Account{}, ErrAccountNotFound
	}
	return account, err
}

// AccountByCredential finds the account a credential belongs to.
//
// The fallback for an authenticator that does not return a user handle — an
// older security key, mostly. The credential identifier is unique across the
// register, so it answers the same question by a different road.
func (s *Store) AccountByCredential(ctx context.Context, credentialID []byte) (models.Account, error) {
	if len(credentialID) == 0 {
		return models.Account{}, ErrAccountNotFound
	}

	var credential models.Credential
	err := s.db.WithContext(ctx).First(&credential, "credential_id = ?", credentialID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.Account{}, ErrAccountNotFound
	}
	if err != nil {
		return models.Account{}, err
	}
	return s.AccountWithCredentials(ctx, credential.AccountID)
}

// AddCredential attaches another passkey to an account.
func (s *Store) AddCredential(ctx context.Context, credential *models.Credential) error {
	return s.db.WithContext(ctx).Create(credential).Error
}

// ListCredentials is somebody's own device list, newest first.
func (s *Store) ListCredentials(ctx context.Context, accountID string) ([]models.Credential, error) {
	var credentials []models.Credential
	err := s.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("created_at desc").
		Find(&credentials).Error
	return credentials, err
}

// RecordCredentialUse stores what a successful sign-in changed.
//
// The sign counter is the authenticator's own, and it is a clone detector
// rather than a rate limit: it going backwards means two things are using one
// credential. It is recorded and not acted on here, because every synced
// passkey reports zero for ever — refusing a stalled counter would lock out
// most of the people this flow exists for.
func (s *Store) RecordCredentialUse(ctx context.Context, credentialID []byte, signCount uint32, backedUp bool) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&models.Credential{}).
		Where("credential_id = ?", credentialID).
		Updates(map[string]any{
			"sign_count":   signCount,
			"backed_up":    backedUp,
			"last_used_at": now,
			"updated_at":   now,
		}).Error
}

// RenameCredential changes what a device is called in its owner's list.
//
// A list reading "a passkey" six times is one nobody can revoke from safely,
// and revoking the wrong row is how somebody locks themselves out of the
// device they are holding.
func (s *Store) RenameCredential(ctx context.Context, accountID, credentialID, name string) error {
	result := s.db.WithContext(ctx).Model(&models.Credential{}).
		Where("id = ? AND account_id = ?", credentialID, accountID).
		Updates(map[string]any{"name": name, "updated_at": time.Now()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrAccountNotFound
	}
	return nil
}

// DeleteCredential removes one passkey, never the last one.
//
// The count and the delete are in one transaction because the check is the
// whole value of the method: two tabs each removing "the other" passkey would
// otherwise each pass the count and leave an account with none.
func (s *Store) DeleteCredential(ctx context.Context, accountID, credentialID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&models.Credential{}).
			Where("account_id = ?", accountID).Count(&count).Error; err != nil {
			return err
		}
		if count <= 1 {
			return ErrLastCredential
		}

		result := tx.Delete(&models.Credential{}, "id = ? AND account_id = ?", credentialID, accountID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrAccountNotFound
		}
		return nil
	})
}

// SetAccountName changes the name an account goes by inside its groups.
func (s *Store) SetAccountName(ctx context.Context, accountID, name string) error {
	result := s.db.WithContext(ctx).Model(&models.Account{}).
		Where("id = ?", accountID).
		Updates(map[string]any{"name": name, "updated_at": time.Now()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrAccountNotFound
	}
	return nil
}

// TouchAccount records that somebody was here.
//
// The only reason it is kept is dormancy: an account with no credential left
// and no sign-in for a year is a row that will never be used again, and a
// register that promises to hold nothing about its readers should not hold
// that either.
func (s *Store) TouchAccount(ctx context.Context, accountID string) error {
	return s.db.WithContext(ctx).Model(&models.Account{}).
		Where("id = ?", accountID).
		Update("last_seen_at", time.Now()).Error
}

// DeleteAccount erases somebody and leaves what they did standing.
//
// This is the erasure path, and the shape of it is a decision the register
// already made: **the identity goes, what was done stays.** A membership row
// with no account reads as an anonymous attendance — which is the default
// presentation anyway — so a group's history stays accurate while the person
// becomes unidentifiable.
//
// Everything that is *about* them is really deleted: passkeys, recovery codes,
// sessions, push endpoints, half-finished ceremonies, and their own
// notifications.
//
// What was *said to somebody else* is anonymised rather than deleted, and that
// includes the messages they wrote to a group. The first draft deleted those,
// on the argument that a private text with nobody left to ask about it should
// not sit in an inbox. That was inconsistent: a notification quoting the
// message went to an admin and stayed there, so the text survived in the less
// visible of the two places. Words said to a group are like the attendance —
// they happened, the group acted on them, and what has to go is the name
// against them.
func (s *Store) DeleteAccount(ctx context.Context, accountID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, model := range []any{
			&models.Credential{}, &models.RecoveryCode{}, &models.Session{},
			&models.WebAuthnCeremony{}, &models.LinkToken{},
			&models.PushSubscription{}, &models.Notification{},
			// The texts they kept. Erased rather than anonymised, which is
			// the opposite of the memberships below and is the whole
			// difference between the two kinds of row: a membership is
			// something a group acted on and the count has to stay true,
			// where a bookmark was only ever for the person who made it. An
			// anonymised one would be a record of what somebody read, kept
			// after they asked to stop existing, useful to nobody.
			&models.Bookmark{},
		} {
			if err := tx.Delete(model, "account_id = ?", accountID).Error; err != nil {
				return err
			}
		}

		// The messages they wrote to groups. The name goes with the reference:
		// it was freely chosen and unverified, but it is still what a group's
		// admins would read as "who wrote this", and erasure has to mean they
		// cannot.
		err := tx.Model(&models.GroupMessage{}).
			Where("from_account_id = ?", accountID).
			Updates(map[string]any{"from_account_id": "", "from_name": ""}).Error
		if err != nil {
			return err
		}

		// The rows that stay, with the reference removed rather than the row.
		// A count of who came to a meeting must not change because somebody
		// later deleted their account.
		for _, model := range []any{&models.GroupMembership{}, &models.Intent{}} {
			err := tx.Model(model).Where("account_id = ?", accountID).
				Update("account_id", "").Error
			if err != nil {
				return err
			}
		}

		return tx.Delete(&models.Account{}, "id = ?", accountID).Error
	})
}

// StoreRecoveryCodes replaces an account's recovery codes with a fresh set.
//
// Replaces rather than adds, because a set is shown once as a list somebody
// writes down: leaving the old ones valid would mean a person who regenerates
// their codes still has the old sheet working, which is the opposite of what
// regenerating them is for.
func (s *Store) StoreRecoveryCodes(ctx context.Context, accountID string, hashes []string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&models.RecoveryCode{}, "account_id = ?", accountID).Error; err != nil {
			return err
		}
		codes := make([]models.RecoveryCode, 0, len(hashes))
		for _, hash := range hashes {
			codes = append(codes, models.RecoveryCode{AccountID: accountID, CodeHash: hash})
		}
		if len(codes) == 0 {
			return nil
		}
		return tx.Create(&codes).Error
	})
}

// RedeemRecoveryCode spends one code and returns the account it belonged to.
//
// Single use, marked spent inside the transaction that found it, so a code
// presented twice in the same instant is accepted once. Being able to sign in
// afterwards is the caller's business: this method proves who somebody is and
// nothing else.
func (s *Store) RedeemRecoveryCode(ctx context.Context, codeHash string) (models.Account, error) {
	var account models.Account

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var code models.RecoveryCode
		err := tx.Where("code_hash = ? AND used_at IS NULL", codeHash).First(&code).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrAccountNotFound
		}
		if err != nil {
			return err
		}

		now := time.Now()
		result := tx.Model(&models.RecoveryCode{}).
			Where("id = ? AND used_at IS NULL", code.ID).
			Updates(map[string]any{"used_at": now, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			// Somebody spent it between the read and the write.
			return ErrAccountNotFound
		}

		return tx.Preload("Credentials").First(&account, "id = ?", code.AccountID).Error
	})
	if err != nil {
		return models.Account{}, err
	}
	return account, nil
}

// CountRecoveryCodes is how many are left unspent, which is what the account
// page tells somebody: a sheet with one code left is worth regenerating.
func (s *Store) CountRecoveryCodes(ctx context.Context, accountID string) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&models.RecoveryCode{}).
		Where("account_id = ? AND used_at IS NULL", accountID).
		Count(&count).Error
	return count, err
}
