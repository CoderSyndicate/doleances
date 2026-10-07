package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// ErrSessionNotFound means the token does not authenticate anybody.
//
// One error for expired, revoked and never issued: a client holding a stale
// token has to sign in again whichever it was, and telling the three apart
// would say whether a token had ever been real.
var ErrSessionNotFound = errors.New("no such session")

// SessionLifetime is how long a signed-in device stays signed in.
//
// Eight hours, renewed while it is in use — so somebody reading the register
// through an afternoon is never interrupted, and a borrowed laptop stops being
// signed in overnight.
const SessionLifetime = 8 * time.Hour

// SessionRenewAfter is how much of a session must be spent before a read
// extends it.
//
// Renewing on every request would write a row for every page view, which is a
// lot of writes to store a fact that changes by minutes. Half the lifetime is
// the point past which extending actually keeps somebody signed in.
const SessionRenewAfter = SessionLifetime / 2

// seenResolution is how stale `last_seen_at` may be before a read writes it.
//
// The column exists for one purpose: to tell somebody, on their own device
// list, when a device was last used. That is read in days and minutes, so
// recording it to the second was costing **one UPDATE per authenticated
// request** — every page view by a signed-in reader, to store a fact nobody
// can see the precision of.
//
// Five minutes keeps the device list truthful at the resolution it is shown
// at, and turns a write on every read into a write on almost none.
const seenResolution = 5 * time.Minute

// CreateSession signs a device in.
func (s *Store) CreateSession(ctx context.Context, accountID, tokenHash, label string) (models.Session, error) {
	now := time.Now()
	session := models.Session{
		AccountID:   accountID,
		TokenHash:   tokenHash,
		DeviceLabel: label,
		ExpiresAt:   now.Add(SessionLifetime),
		LastSeenAt:  now,
	}
	if err := s.db.WithContext(ctx).Create(&session).Error; err != nil {
		return models.Session{}, err
	}
	return session, nil
}

// Authenticate resolves a session token to the account behind it, renewing
// the session if it is far enough through its life.
//
// The renewal is here rather than in a separate call because a session that
// has to be renewed explicitly is one somebody forgets to renew: the frontend
// would work for eight hours and then log people out mid-sentence, and the
// bug would only ever appear in a long session.
func (s *Store) Authenticate(ctx context.Context, tokenHash string) (models.Account, models.Session, error) {
	var session models.Session
	err := s.db.WithContext(ctx).Where("token_hash = ?", tokenHash).First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.Account{}, models.Session{}, ErrSessionNotFound
	}
	if err != nil {
		return models.Account{}, models.Session{}, err
	}

	now := time.Now()
	if !session.ExpiresAt.After(now) {
		// Expired. Deleted on sight rather than swept later: the row is
		// worthless and keeping it is keeping a record of which device
		// somebody used and when.
		//
		// **Deliberately not inside a transaction with the refusal.** A
		// transaction that both deleted the row and returned an error would
		// roll the delete back with it, so the row would live for ever and the
		// only symptom would be a table that never shrinks. This project has
		// made that mistake once already, with an attempt counter.
		if err := s.db.WithContext(ctx).
			Delete(&models.Session{}, "id = ?", session.ID).Error; err != nil {
			return models.Account{}, models.Session{}, err
		}
		return models.Account{}, models.Session{}, ErrSessionNotFound
	}

	var account models.Account
	err = s.db.WithContext(ctx).First(&account, "id = ?", session.AccountID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// A session whose account is gone — deleted between one request and
		// the next. Refused as no session at all, which is what it is.
		return models.Account{}, models.Session{}, ErrSessionNotFound
	}
	if err != nil {
		return models.Account{}, models.Session{}, err
	}

	fields := map[string]any{}
	if now.Sub(session.LastSeenAt) >= seenResolution {
		fields["last_seen_at"] = now
	}
	if now.Sub(session.ExpiresAt.Add(-SessionLifetime)) > SessionRenewAfter {
		session.ExpiresAt = now.Add(SessionLifetime)
		fields["expires_at"] = session.ExpiresAt
		fields["updated_at"] = now
	}

	// What the caller is told is always now, whether or not it was written.
	// The session *is* being used; the row is simply not re-stating it.
	session.LastSeenAt = now

	if len(fields) == 0 {
		return account, session, nil
	}

	// Best effort. A session that could not record its own last use is still a
	// valid session, and failing the request would log somebody out over a
	// write that does not matter.
	err = s.db.WithContext(ctx).Model(&models.Session{}).
		Where("id = ?", session.ID).Updates(fields).Error
	if err != nil {
		return account, session, err
	}
	return account, session, nil
}

// ListSessions is somebody's own signed-in devices, most recent first.
func (s *Store) ListSessions(ctx context.Context, accountID string) ([]models.Session, error) {
	var sessions []models.Session
	err := s.db.WithContext(ctx).
		Where("account_id = ? AND expires_at > ?", accountID, time.Now()).
		Order("last_seen_at desc").
		Find(&sessions).Error
	return sessions, err
}

// DeleteSession signs one device out.
//
// Scoped to the account as well as the identifier, so a stolen session
// identifier cannot be used to sign out somebody else's devices.
func (s *Store) DeleteSession(ctx context.Context, accountID, sessionID string) error {
	result := s.db.WithContext(ctx).
		Delete(&models.Session{}, "id = ? AND account_id = ?", sessionID, accountID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// DeleteSessionByToken signs the calling device out.
func (s *Store) DeleteSessionByToken(ctx context.Context, tokenHash string) error {
	return s.db.WithContext(ctx).Delete(&models.Session{}, "token_hash = ?", tokenHash).Error
}

// PurgeSessions deletes what has expired.
//
// Retention that nothing enforces is not retention. An expired session is a
// record of which device somebody used and when, kept for no purpose.
func (s *Store) PurgeSessions(ctx context.Context) (int64, error) {
	result := s.db.WithContext(ctx).
		Delete(&models.Session{}, "expires_at < ?", time.Now())
	return result.RowsAffected, result.Error
}
