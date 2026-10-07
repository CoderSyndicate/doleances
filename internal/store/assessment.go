package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// ClaimForAssessment takes one pending submission for a worker to assess.
//
// It claims by moving the row out of `pending` in the same statement that
// selects it, so two backends — or two goroutines — cannot take the same
// submission. Without that, a busy queue is assessed twice and charged twice,
// and the second decision silently overwrites the first.
//
// The claimed state is `assessing`, which is not a resting place: anything left
// there by a crash is returned to the queue by ReleaseStaleAssessments.
func (s *Store) ClaimForAssessment(ctx context.Context) (models.Message, error) {
	var claimed models.Message

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidate models.Message
		err := tx.Where("status = ?", models.StatusPending).
			Order("created_at asc"). // oldest first: nobody waits indefinitely
			First(&candidate).Error
		if err != nil {
			return err
		}

		result := tx.Model(&models.Message{}).
			Where("id = ? AND status = ?", candidate.ID, models.StatusPending).
			Updates(map[string]any{
				"status":     models.StatusAssessing,
				"updated_at": time.Now(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			// Somebody else took it between the select and the update.
			return gorm.ErrRecordNotFound
		}

		claimed = candidate
		claimed.Status = models.StatusAssessing
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.Message{}, ErrMessageNotFound
	}
	return claimed, err
}

// RecordAssessment stores a model's verdict and the status it implies.
// Verdict is everything a model concluded about one submission.
//
// A struct rather than six positional arguments, four of them strings in a
// row: transposing two of those is invisible at the call site and silent
// afterwards, which is a mistake this package has already made once with a
// column name.
type Verdict struct {
	Status     models.ReviewStatus
	Confidence int

	// Model is which model produced it, so a decision can be traced to a
	// version when thresholds are tuned or a model swapped.
	Model string

	// Refusal is the named ground the submission cannot be published on —
	// "threat", "contact", "identifies" — and empty when the score decided.
	Refusal string

	// Reason is the model's own sentence. See models.Message.AssessmentReason
	// for why it is worth a column.
	Reason string
}

func (s *Store) RecordAssessment(ctx context.Context, id string, verdict Verdict) error {
	now := time.Now()
	fields := map[string]any{
		"status":            verdict.Status,
		"confidence":        verdict.Confidence,
		"assessed_at":       now,
		"assessed_by":       verdict.Model,
		"assessment_reason": verdict.Reason,
		"updated_at":        now,
	}
	// Why it was refused, so the dropped sample can say "threat" rather than
	// showing a score of 85 with no explanation for why it is on that page.
	if verdict.Refusal != "" {
		fields["drop_reason"] = verdict.Refusal
	}
	// Publication time is the moment it became public, which for an
	// automatically accepted doléance is now.
	if verdict.Status == models.StatusAccepted {
		fields["published_at"] = now
	}

	return s.db.WithContext(ctx).Model(&models.Message{}).
		Where("id = ?", id).Updates(fields).Error
}

// ReturnForRetry puts a claimed submission back in the queue after a failure,
// counting the attempt.
//
// The count is what stops a submission cycling for ever against a model that
// cannot answer it: past the cap the caller sends it to a human instead.
func (s *Store) ReturnForRetry(ctx context.Context, id string) (attempts int, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var message models.Message
		if err := tx.Where("id = ?", id).First(&message).Error; err != nil {
			return err
		}
		attempts = message.AssessmentAttempts + 1

		return tx.Model(&models.Message{}).Where("id = ?", id).
			Updates(map[string]any{
				"status":              models.StatusPending,
				"assessment_attempts": attempts,
				"updated_at":          time.Now(),
			}).Error
	})
	return attempts, err
}

// SendToCuration hands a submission to a human, whatever the model did or
// failed to do.
func (s *Store) SendToCuration(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Model(&models.Message{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":     models.StatusCurating,
			"updated_at": time.Now(),
		}).Error
}

// ReleaseStaleAssessments returns submissions stuck mid-assessment to the
// queue.
//
// A backend killed between claiming and recording leaves a row in `assessing`,
// which no sweep would look at again and no curator would see: the doléance
// would be invisible to everybody. Anything held longer than the window is
// assumed abandoned.
func (s *Store) ReleaseStaleAssessments(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)

	result := s.db.WithContext(ctx).Model(&models.Message{}).
		Where("status = ? AND updated_at < ?", models.StatusAssessing, cutoff).
		Updates(map[string]any{
			"status":     models.StatusPending,
			"updated_at": time.Now(),
		})
	return result.RowsAffected, result.Error
}

// MarkClassified records that the subjects were read off a doléance.
func (s *Store) MarkClassified(ctx context.Context, id string) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&models.Message{}).
		Where("id = ?", id).
		Updates(map[string]any{"classified_at": now, "updated_at": now}).Error
}

// CountClassificationAttempt records a failed attempt and returns the running
// total.
func (s *Store) CountClassificationAttempt(ctx context.Context, id string) (int, error) {
	var attempts int
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var message models.Message
		if err := tx.Where("id = ?", id).First(&message).Error; err != nil {
			return err
		}
		attempts = message.ClassificationAttempts + 1

		return tx.Model(&models.Message{}).Where("id = ?", id).
			Update("classification_attempts", attempts).Error
	})
	return attempts, err
}

// ListUnclassified returns published doléances whose subjects were never read.
//
// Published, because classification only runs on what is in the register;
// never classified, because a classification time is set only on success; and
// under the attempt cap, because a text the model cannot handle should stop
// being retried rather than be retried for ever.
//
// Oldest first: a doléance that has been unfindable longest is the one most
// worth fixing.
func (s *Store) ListUnclassified(ctx context.Context, maxAttempts, limit int) ([]models.Message, error) {
	if limit <= 0 {
		limit = 20
	}

	var messages []models.Message
	err := s.db.WithContext(ctx).
		Where("status = ? AND classified_at IS NULL AND classification_attempts < ?",
			models.StatusAccepted, maxAttempts).
		Order("created_at asc").
		Limit(limit).
		Find(&messages).Error
	return messages, err
}

// CountPendingAssessment is how much is waiting for a model.
func (s *Store) CountPendingAssessment(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&models.Message{}).
		Where("status = ?", models.StatusPending).
		Count(&count).Error
	return count, err
}
