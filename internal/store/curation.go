package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CoderSyndicate/doleances/internal/models"
	"gorm.io/gorm"
)

// CurationSettings returns the stored policies, or the defaults.
func (s *Store) CurationSettings(ctx context.Context) (models.CurationSettings, error) {
	var settings models.CurationSettings
	err := s.db.WithContext(ctx).First(&settings, "id = ?", models.CurationSettingsID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.DefaultCurationSettings(), nil
	}
	if err != nil {
		return models.CurationSettings{}, fmt.Errorf("read curation settings: %w", err)
	}
	return settings, nil
}

// SaveCurationSettings writes the policies.
func (s *Store) SaveCurationSettings(ctx context.Context, settings models.CurationSettings) error {
	settings.ID = models.CurationSettingsID
	if err := s.db.WithContext(ctx).Save(&settings).Error; err != nil {
		return fmt.Errorf("save curation settings: %w", err)
	}
	return nil
}

// ListSpam returns the dropped submissions still inside the retention window,
// newest first.
//
// This is a sample to glance at, not a work queue, so it is bounded: nobody is
// expected to read all of it.
func (s *Store) ListSpam(ctx context.Context, retention time.Duration, limit, offset int) ([]models.Message, error) {
	if limit < 1 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	cutoff := time.Now().Add(-retention)

	var messages []models.Message
	err := s.db.WithContext(ctx).
		Where("status = ? AND updated_at >= ?", models.StatusDropped, cutoff).
		// Contested first, newest within that.
		//
		// A plea is a reader saying the filter got this one wrong, and the
		// whole point of letting them say so is that somebody then looks.
		// Ordered by time alone a contested submission sinks down the page as
		// the day's drops arrive and is purged unread — which would make the
		// control a gesture rather than a check.
		//
		// Ordered by the count, not decided by it: this moves a submission up
		// a page, and a human still rules on it.
		// Contested first, then oldest.
		//
		// A plea is a reader saying the filter got this one wrong, and the
		// point of letting them say so is that somebody then looks. Ordered
		// by time alone a contested submission sinks as the day's drops
		// arrive and is purged unread, which would make the control a gesture.
		//
		// Oldest first underneath that, because this sample is purged: the
		// one nearest its deletion is the one nobody will get another chance
		// to look at. Newest first put the most urgent at the bottom.
		//
		// Ordered by the count, never decided by it: this moves a submission
		// up a page, and a human still rules on it.
		Order("pleas > 0 desc, pleas desc, updated_at asc").
		Limit(limit).
		Offset(offset).
		Find(&messages).Error
	if err != nil {
		return nil, fmt.Errorf("list dropped messages: %w", err)
	}
	return messages, nil
}

// CountSpam reports how many dropped submissions are being held.
func (s *Store) CountSpam(ctx context.Context, retention time.Duration) (int64, error) {
	cutoff := time.Now().Add(-retention)

	var count int64
	err := s.db.WithContext(ctx).Model(&models.Message{}).
		Where("status = ? AND updated_at >= ?", models.StatusDropped, cutoff).
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("count dropped messages: %w", err)
	}
	return count, nil
}

// RescueSpam moves a dropped submission into the curation queue.
//
// It does not publish: the classifier was wrong about it being spam, which is
// not the same as it being ready for the register. A glance at a sampling page
// should never put text in front of the public on its own.
func (s *Store) RescueSpam(ctx context.Context, id string) error {
	result := s.db.WithContext(ctx).Model(&models.Message{}).
		Where("id = ? AND status = ?", id, models.StatusDropped).
		Update("status", models.StatusCurating)
	if result.Error != nil {
		return fmt.Errorf("rescue message %q: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrMessageNotFound
	}
	return nil
}

// DeleteSpam removes one dropped submission immediately.
func (s *Store) DeleteSpam(ctx context.Context, id string) error {
	result := s.db.WithContext(ctx).
		Where("id = ? AND status = ?", id, models.StatusDropped).
		Delete(&models.Message{})
	if result.Error != nil {
		return fmt.Errorf("delete message %q: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrMessageNotFound
	}
	return nil
}

// PurgeSpam deletes dropped submissions past the retention window and reports
// how many went.
//
// This is real deletion, not a flag: the content was judged not to belong
// here, and keeping it indefinitely would make the spam bin the largest thing
// in the database.
func (s *Store) PurgeSpam(ctx context.Context, retention time.Duration) (int64, error) {
	cutoff := time.Now().Add(-retention)

	result := s.db.WithContext(ctx).
		Where("status = ? AND updated_at < ?", models.StatusDropped, cutoff).
		Delete(&models.Message{})
	if result.Error != nil {
		return 0, fmt.Errorf("purge dropped messages: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// ErrMessageNotFound is returned when a message is not there, or is no longer
// in the state the operation expected.
var ErrMessageNotFound = errors.New("message not found")

// SeedHistoricalText stores a corpus passage only if one with that id is not
// already there.
//
// It never overwrites: a curator who corrected a transcription in the console
// keeps their correction across restarts. Changing a passage in the corpus
// file therefore reaches an existing installation only under a new id — which
// is deliberate, since silently rewriting a quotation is how a record stops
// being one.
func (s *Store) SeedHistoricalText(ctx context.Context, text models.HistoricalText) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&models.HistoricalText{}).
		Where("id = ?", text.ID).Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("count historical text %q: %w", text.ID, err)
	}
	if count > 0 {
		return false, nil
	}

	if err := s.db.WithContext(ctx).Create(&text).Error; err != nil {
		return false, fmt.Errorf("seed historical text %q: %w", text.ID, err)
	}
	return true, nil
}

// ListHistoricalTexts returns the corpus, oldest period first so the register
// reads chronologically.
// ErrHistoricalNotFound means no such passage.
var ErrHistoricalNotFound = errors.New("historical text not found")

// GetHistoricalText reads one passage with its translations.
func (s *Store) GetHistoricalText(ctx context.Context, id string) (models.HistoricalText, error) {
	var text models.HistoricalText
	err := s.db.WithContext(ctx).
		Preload("Translations").
		First(&text, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return text, ErrHistoricalNotFound
	}
	return text, err
}

// LikeHistoricalText records one more reader who recognised themselves in a
// passage, and returns the new count.
//
// An UPDATE that adds one, for the reason a doléance's does: two readers
// pressing at the same moment must both be counted.
func (s *Store) LikeHistoricalText(ctx context.Context, id string) (int, error) {
	result := s.db.WithContext(ctx).Model(&models.HistoricalText{}).
		Where("id = ?", id).
		UpdateColumn("likes", gorm.Expr("likes + 1"))
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected == 0 {
		return 0, ErrHistoricalNotFound
	}

	var text models.HistoricalText
	if err := s.db.WithContext(ctx).Select("likes").
		First(&text, "id = ?", id).Error; err != nil {
		return 0, err
	}
	return text.Likes, nil
}

func (s *Store) ListHistoricalTexts(ctx context.Context) ([]models.HistoricalText, error) {
	var texts []models.HistoricalText
	err := s.db.WithContext(ctx).
		Preload("Translations").
		Order("period asc, title asc").
		Find(&texts).Error
	if err != nil {
		return nil, fmt.Errorf("list historical texts: %w", err)
	}
	return texts, nil
}

// ---------------------------------------------------------------------------
// The curation queue
// ---------------------------------------------------------------------------

// ListCurationQueue returns every submission awaiting a human decision,
// oldest first so nothing is left at the bottom indefinitely.
//
// It deliberately includes **pending** as well as **curating**. Pending means
// the classifier has not ruled on it — because the LLM is unconfigured,
// unreachable, or simply has not got to it yet — and a submission in that
// state must still reach a person. Humans are the last line: if the machine
// cannot do the work, somebody does it by hand, and the register keeps moving.
// The alternative is a queue that looks reassuringly empty while doléances
// pile up in a state nobody is watching, which is how a register quietly
// stops being one.
func (s *Store) ListCurationQueue(ctx context.Context, limit, offset int) ([]models.Message, error) {
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	var messages []models.Message
	err := s.db.WithContext(ctx).
		Preload("Subjects").
		Where("status IN ?", []models.ReviewStatus{models.StatusCurating, models.StatusPending}).
		// Oldest first: a queue is worked from the front, and somebody who
		// wrote a week ago has been waiting a week.
		Order("created_at asc").
		Limit(limit).
		Offset(offset).
		Find(&messages).Error
	return messages, err
}

// CountCurationQueue is the badge on the page: how much is waiting.
func (s *Store) CountCurationQueue(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&models.Message{}).
		Where("status IN ?", []models.ReviewStatus{models.StatusCurating, models.StatusPending}).
		Count(&count).Error
	return count, err
}

// AcceptMessage publishes a doléance, and records that a human read it.
//
// A human decision is final and needs no machine agreement: a curator may
// accept something the classifier never scored, which is exactly what keeps
// the register moving when no classifier is available.
//
// # Verified is set here and nowhere else
//
// That is what makes the mark mean something. Most of the register publishes on
// a score with nobody involved, so "verified" is the difference between a
// machine's confidence and a person's judgement — and the only place a person
// renders judgement on a doléance is this function.
//
// # The publication time is stamped once, not on every accept
//
// A doléance can now be accepted twice: a reader reports it, it comes off the
// register, a curator looks and lets it stand. Stamping `published_at` again
// would move the moment it became public to the moment somebody complained
// about it, and for a project whose worth is being a faithful record that is a
// falsified row — quietly, in the one column that says when the public first
// could read it. So it is set only when it was never set.
//
// Read inside the transaction that writes, because deciding from a value read
// outside one is how two curators pressing together produce a row neither of
// them described.
func (s *Store) AcceptMessage(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()

		var message models.Message
		err := tx.Select("id", "published_at").
			Where("id = ? AND status IN ?", id,
				[]models.ReviewStatus{models.StatusCurating, models.StatusPending}).
			First(&message).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Either it is gone, or somebody else already decided. Both mean
			// this curator's decision does not apply, and saying so beats
			// silently overwriting a colleague.
			return ErrMessageNotFound
		}
		if err != nil {
			return err
		}

		fields := map[string]any{
			"status": models.StatusAccepted,
			// A curator read it. This is the only write of this column.
			"verified":   true,
			"updated_at": now,
		}
		if message.PublishedAt == nil {
			fields["published_at"] = now
		}

		result := tx.Model(&models.Message{}).
			Where("id = ? AND status IN ?", id,
				[]models.ReviewStatus{models.StatusCurating, models.StatusPending}).
			Updates(fields)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrMessageNotFound
		}
		return nil
	})
}

// RejectMessage discards a submission a curator judged not to be a doléance.
//
// Rejection is deletion. Content refused by a curator is not archived: there is
// no reason to spend storage, backup and attention on it, and a rejected-text
// table is a pile of other people's words nobody is accountable for. The audit
// log keeps the decision, the curator and the reason — never the text.
func (s *Store) RejectMessage(ctx context.Context, id string) error {
	var message models.Message
	err := s.db.WithContext(ctx).
		Where("id = ? AND status IN ?", id,
			[]models.ReviewStatus{models.StatusCurating, models.StatusPending}).
		First(&message).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrMessageNotFound
	}
	if err != nil {
		return err
	}
	return s.DeleteMessage(ctx, id)
}

// ListDropped is the dropped sample as the public page reads it.
//
// Deliberately a second method rather than a flag on ListSpam. The console's
// listing is a curator's working sample and carries the model's own sentence
// and its score; this one is answering a reader, and giving either of those
// to anybody would turn the page into a tuning instrument for exactly the
// flooding it reports — the same reason the submission receipt says `pending`
// whatever actually happened.
//
// It selects the columns the public page may show and no others, so a field
// added to the model later is absent here until somebody decides it belongs.
func (s *Store) ListDropped(ctx context.Context, retention time.Duration, limit, offset int) ([]models.Message, error) {
	if limit < 1 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	cutoff := time.Now().Add(-retention)

	var messages []models.Message
	err := s.db.WithContext(ctx).
		Select("id", "text", "drop_reason", "pleas", "updated_at", "language").
		Where("status = ? AND updated_at >= ?", models.StatusDropped, cutoff).
		// The same order the curator's listing uses, so a reader who objects
		// and a curator who looks are reading the same page in the same order.
		Order("pleas > 0 desc, pleas desc, updated_at asc").
		Limit(limit).
		Offset(offset).
		Find(&messages).Error
	if err != nil {
		return nil, fmt.Errorf("list dropped messages: %w", err)
	}
	return messages, nil
}

// PleadForMessage records a reader saying the filter refused this wrongly.
//
// Only a dropped submission can be pleaded for, and only while it is still
// held: once the purge has run there is nothing to reconsider, and a plea for
// a published doléance would be a reader objecting to a refusal that never
// happened.
func (s *Store) PleadForMessage(ctx context.Context, id string, retention time.Duration) (int, error) {
	cutoff := time.Now().Add(-retention)

	result := s.db.WithContext(ctx).Model(&models.Message{}).
		Where("id = ? AND status = ? AND updated_at >= ?", id, models.StatusDropped, cutoff).
		// UpdateColumn, so the row's own updated_at is left alone: it is what
		// the retention window is measured from, and a plea must not extend
		// the life of what it is pleading for.
		UpdateColumn("pleas", gorm.Expr("pleas + 1"))
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected == 0 {
		return 0, ErrMessageNotFound
	}

	var message models.Message
	if err := s.db.WithContext(ctx).Select("pleas").
		Where("id = ?", id).First(&message).Error; err != nil {
		return 0, err
	}
	return message.Pleas, nil
}
