package store

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// bookmarkLimit bounds one account's list.
//
// Generous, because somebody who has been reading the register for a year may
// genuinely have kept a few hundred texts and a filing cabinet that silently
// stopped accepting things would be worse than one with a stated size. Bounded
// anyway, because it is a list a page renders.
const bookmarkLimit = 500

// KeptText is a bookmark with the text it points at.
//
// Resolved here rather than by the caller because the resolution is also the
// cleanup: see ListBookmarks.
type KeptText struct {
	Bookmark models.Bookmark

	// Exactly one of these is set, matching the bookmark's kind.
	Message    *models.Message
	Historical *models.HistoricalText
}

// AddBookmark records that an account asked to keep a text.
//
// Keeping something twice is not an error worth a different answer — somebody
// pressed the button twice, or their page was stale, and either way the text is
// in their list. The first press is the one the timestamp remembers: they kept
// it when they kept it.
func (s *Store) AddBookmark(ctx context.Context, accountID string,
	kind models.BookmarkKind, targetID string) error {

	bookmark := models.Bookmark{
		AccountID: accountID,
		Kind:      kind,
		TargetID:  targetID,
		KeptAt:    time.Now(),
	}
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&bookmark).Error
}

// RemoveBookmark takes a text out of somebody's list.
//
// Removing one that is not there is not an error either, and for the reason
// that matters more than symmetry: a reader taking something back must never
// be shown a failure for a row that is already gone.
func (s *Store) RemoveBookmark(ctx context.Context, accountID string,
	kind models.BookmarkKind, targetID string) error {

	return s.db.WithContext(ctx).
		Where("account_id = ? AND kind = ? AND target_id = ?", accountID, kind, targetID).
		Delete(&models.Bookmark{}).Error
}

// ListBookmarks is what an account kept, most recently kept first.
//
// # It deletes what it cannot resolve
//
// A bookmark points at a doléance or a passage by identifier and constrains
// neither table, so a text can go while the bookmark stays: a contributor
// deletes their own doléance, an expiry date passes, a curator's rejection
// removes it, a snapshot restore replaces the whole corpus. Chasing every one
// of those from here would mean a cleanup somebody forgets to add to the next
// deletion path, and the symptom would be a reader's own list showing gaps
// they cannot clear.
//
// So the resolution *is* the cleanup: a bookmark whose text is gone is deleted
// on sight. That is the same rule this project already applies to a dangling
// subject alias and to an expired session, and for the same reason — the row
// is worthless and keeping it is keeping a record of what somebody once read.
func (s *Store) ListBookmarks(ctx context.Context, accountID string) ([]KeptText, error) {
	var bookmarks []models.Bookmark
	err := s.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("kept_at desc").
		Limit(bookmarkLimit).
		Find(&bookmarks).Error
	if err != nil {
		return nil, err
	}

	kept := make([]KeptText, 0, len(bookmarks))
	var dangling []string

	for _, bookmark := range bookmarks {
		switch bookmark.Kind {
		case models.BookmarkMessage:
			message, err := s.GetMessage(ctx, bookmark.TargetID)
			if err != nil {
				dangling = append(dangling, bookmark.ID)
				continue
			}
			kept = append(kept, KeptText{Bookmark: bookmark, Message: &message})

		case models.BookmarkHistorical:
			text, err := s.GetHistoricalText(ctx, bookmark.TargetID)
			if err != nil {
				dangling = append(dangling, bookmark.ID)
				continue
			}
			kept = append(kept, KeptText{Bookmark: bookmark, Historical: &text})

		default:
			// A kind nothing can read is a row nothing can ever show.
			dangling = append(dangling, bookmark.ID)
		}
	}

	if len(dangling) > 0 {
		// Outside any transaction with the read, deliberately. A failure here
		// is housekeeping that did not happen, and it must not cost the reader
		// the list they asked for.
		if err := s.db.WithContext(ctx).
			Delete(&models.Bookmark{}, "id IN ?", dangling).Error; err != nil {
			return kept, nil //nolint:nilerr // the list is the answer; the tidy-up is not
		}
	}
	return kept, nil
}

// BookmarkedBy reports which of these texts an account has kept.
//
// One query for a page of cards rather than one per card: the register's
// listings run to fifty, and a button that cost a round trip each would be a
// button that made the page slower for everybody who is signed in.
//
// An empty account identifier answers an empty set rather than querying for
// bookmarks belonging to nobody — a reader who is not signed in has kept
// nothing, which is a different statement from "the rows with no owner".
func (s *Store) BookmarkedBy(ctx context.Context, accountID string,
	kind models.BookmarkKind, targetIDs []string) (map[string]bool, error) {

	kept := map[string]bool{}
	if accountID == "" || len(targetIDs) == 0 {
		return kept, nil
	}

	var found []models.Bookmark
	err := s.db.WithContext(ctx).
		Select("target_id").
		Where("account_id = ? AND kind = ? AND target_id IN ?", accountID, kind, targetIDs).
		Find(&found).Error
	if err != nil {
		return nil, err
	}
	for _, bookmark := range found {
		kept[bookmark.TargetID] = true
	}
	return kept, nil
}

// forgetBookmarksOf removes every bookmark pointing at one text.
//
// Called from the deletion of the text itself, inside the same transaction:
// "deleted is deleted" is a promise this project makes in as many words, and a
// row saying that somebody kept a doléance that no longer exists is a trace of
// it. The self-healing read in ListBookmarks is the backstop behind this, not
// a substitute for it — a trace nobody happens to read is still a trace.
func forgetBookmarksOf(tx *gorm.DB, kind models.BookmarkKind, targetID string) error {
	return tx.Where("kind = ? AND target_id = ?", kind, targetID).
		Delete(&models.Bookmark{}).Error
}
