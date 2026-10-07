package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// ErrCeremonyNotFound means the challenge is not one this server is waiting
// for.
//
// Expired, already finished, issued for the other purpose and never issued at
// all are one answer. A client in that position has to start again whichever
// it was.
var ErrCeremonyNotFound = errors.New("no such ceremony")

// ErrLinkNotFound means the device-linking token is not usable.
var ErrLinkNotFound = errors.New("that link is not valid")

// BeginCeremony remembers what the server asked, so the answer can be checked.
//
// The challenge is the security property of the whole exchange: a client that
// carried its own could replay a captured signature, and the reason a
// WebAuthn assertion means anything is that the server remembers what it
// asked for and asked only once.
func (s *Store) BeginCeremony(ctx context.Context, purpose, accountID string, data, subject []byte) (models.WebAuthnCeremony, error) {
	ceremony := models.WebAuthnCeremony{
		AccountID: accountID,
		Data:      data,
		Subject:   subject,
		Purpose:   purpose,
		ExpiresAt: time.Now().Add(models.CeremonyLifetime),
	}
	if err := s.db.WithContext(ctx).Create(&ceremony).Error; err != nil {
		return models.WebAuthnCeremony{}, err
	}
	return ceremony, nil
}

// FinishCeremony takes a challenge back and spends it.
//
// Single use, deleted inside the transaction that read it, which is what makes
// a replay impossible rather than merely unlikely: the same challenge
// presented twice in the same instant succeeds once.
func (s *Store) FinishCeremony(ctx context.Context, id, purpose string) (models.WebAuthnCeremony, error) {
	var ceremony models.WebAuthnCeremony

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("id = ? AND purpose = ? AND expires_at > ?", id, purpose, time.Now()).
			First(&ceremony).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrCeremonyNotFound
		}
		if err != nil {
			return err
		}

		result := tx.Delete(&models.WebAuthnCeremony{}, "id = ?", ceremony.ID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrCeremonyNotFound
		}
		return nil
	})
	if err != nil {
		return models.WebAuthnCeremony{}, err
	}
	return ceremony, nil
}

// PurgeCeremonies deletes the exchanges nobody finished.
func (s *Store) PurgeCeremonies(ctx context.Context) (int64, error) {
	result := s.db.WithContext(ctx).
		Delete(&models.WebAuthnCeremony{}, "expires_at < ?", time.Now())
	return result.RowsAffected, result.Error
}

// CreateLinkToken opens a device-linking window on a signed-in device.
func (s *Store) CreateLinkToken(ctx context.Context, accountID, tokenHash string) (models.LinkToken, error) {
	link := models.LinkToken{
		AccountID: accountID,
		TokenHash: tokenHash,
		ExpiresAt: time.Now().Add(models.LinkLifetime),
	}
	if err := s.db.WithContext(ctx).Create(&link).Error; err != nil {
		return models.LinkToken{}, err
	}
	return link, nil
}

// ClaimLinkToken records that a new device has presented the code and is
// waiting to be approved.
//
// It does not authenticate anything. The new device learns only that the code
// was real, which is a fact it already had; nothing is attached to the account
// until the *old* device confirms — that is what stops a relayed QR code,
// because somebody who tricked a person into scanning theirs still has to get
// that person to approve a browser they do not recognise.
func (s *Store) ClaimLinkToken(ctx context.Context, tokenHash, label string) (models.LinkToken, error) {
	var link models.LinkToken

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("token_hash = ? AND expires_at > ? AND claimed_at IS NULL",
			tokenHash, time.Now()).First(&link).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrLinkNotFound
		}
		if err != nil {
			return err
		}

		now := time.Now()
		result := tx.Model(&models.LinkToken{}).
			Where("id = ? AND claimed_at IS NULL", link.ID).
			Updates(map[string]any{"claimed_at": now, "claim_label": label, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrLinkNotFound
		}
		link.ClaimedAt = &now
		link.ClaimLabel = label
		return nil
	})
	if err != nil {
		return models.LinkToken{}, err
	}
	return link, nil
}

// PendingLink is what the signed-in device is being asked to approve.
func (s *Store) PendingLink(ctx context.Context, accountID string) (models.LinkToken, error) {
	var link models.LinkToken
	err := s.db.WithContext(ctx).
		Where("account_id = ? AND expires_at > ? AND claimed_at IS NOT NULL AND confirmed_at IS NULL",
			accountID, time.Now()).
		Order("claimed_at desc").
		First(&link).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.LinkToken{}, ErrLinkNotFound
	}
	return link, err
}

// ConfirmLink is the old device approving the new one.
func (s *Store) ConfirmLink(ctx context.Context, accountID, linkID string) error {
	now := time.Now()
	result := s.db.WithContext(ctx).Model(&models.LinkToken{}).
		Where("id = ? AND account_id = ? AND claimed_at IS NOT NULL AND confirmed_at IS NULL AND expires_at > ?",
			linkID, accountID, now).
		Updates(map[string]any{"confirmed_at": now, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrLinkNotFound
	}
	return nil
}

// RejectLink is the old device refusing, which is the case that matters: a
// person who did not start a linking sees a prompt for a browser they do not
// recognise and says no.
func (s *Store) RejectLink(ctx context.Context, accountID, linkID string) error {
	result := s.db.WithContext(ctx).
		Delete(&models.LinkToken{}, "id = ? AND account_id = ?", linkID, accountID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrLinkNotFound
	}
	return nil
}

// SpendLinkToken is the new device collecting its account, once approved.
//
// Single use and deleted on collection: the code is on a screen being
// photographed, so its whole safety is that it is worth nothing a second time.
func (s *Store) SpendLinkToken(ctx context.Context, tokenHash string) (models.Account, error) {
	var account models.Account

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var link models.LinkToken
		err := tx.Where("token_hash = ? AND expires_at > ? AND confirmed_at IS NOT NULL",
			tokenHash, time.Now()).First(&link).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrLinkNotFound
		}
		if err != nil {
			return err
		}

		result := tx.Delete(&models.LinkToken{}, "id = ?", link.ID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrLinkNotFound
		}

		return tx.Preload("Credentials").First(&account, "id = ?", link.AccountID).Error
	})
	if err != nil {
		return models.Account{}, err
	}
	return account, nil
}

// ConfirmedLink reads an approved link without spending it.
//
// The new device needs its account's credentials before it can register a
// passkey — `excludeCredentials` has to be complete, or an authenticator that
// already holds one silently makes a second. So the token is read here and
// spent by SpendLinkToken at the end, which is the step that cannot be redone.
func (s *Store) ConfirmedLink(ctx context.Context, tokenHash string) (models.Account, error) {
	var link models.LinkToken
	err := s.db.WithContext(ctx).
		Where("token_hash = ? AND expires_at > ? AND confirmed_at IS NOT NULL",
			tokenHash, time.Now()).
		First(&link).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.Account{}, ErrLinkNotFound
	}
	if err != nil {
		return models.Account{}, err
	}
	return s.AccountWithCredentials(ctx, link.AccountID)
}

// LinkState is how far a link has got, for the device waiting on it.
type LinkState struct {
	Claimed   bool
	Confirmed bool
}

// LinkProgress is what the new device polls.
//
// It answers only about a token the caller already holds, so it tells a
// stranger nothing: without the token there is no question to ask.
func (s *Store) LinkProgress(ctx context.Context, tokenHash string) (LinkState, error) {
	var link models.LinkToken
	err := s.db.WithContext(ctx).
		Where("token_hash = ? AND expires_at > ?", tokenHash, time.Now()).
		First(&link).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return LinkState{}, ErrLinkNotFound
	}
	if err != nil {
		return LinkState{}, err
	}
	return LinkState{Claimed: link.ClaimedAt != nil, Confirmed: link.ConfirmedAt != nil}, nil
}

// PurgeLinkTokens deletes the windows nobody walked through.
func (s *Store) PurgeLinkTokens(ctx context.Context) (int64, error) {
	result := s.db.WithContext(ctx).
		Delete(&models.LinkToken{}, "expires_at < ?", time.Now())
	return result.RowsAffected, result.Error
}
