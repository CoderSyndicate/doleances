package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// dropMessage inserts a dropped submission, backdated by age.
func dropMessage(t *testing.T, s *Store, text string, age time.Duration) models.Message {
	t.Helper()

	message := models.Message{Text: text, Status: models.StatusDropped, Confidence: 20}
	if err := s.DB().Create(&message).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}
	// GORM stamps UpdatedAt on write, so backdating needs a direct update.
	if age > 0 {
		err := s.DB().Model(&models.Message{}).Where("id = ?", message.ID).
			UpdateColumn("updated_at", time.Now().Add(-age)).Error
		if err != nil {
			t.Fatalf("backdate message: %v", err)
		}
	}
	return message
}

func TestCurationSettingsDefault(t *testing.T) {
	s := newStore(t)

	settings, err := s.CurationSettings(context.Background())
	if err != nil {
		t.Fatalf("CurationSettings: %v", err)
	}
	if settings.SpamRetentionHours != 24 {
		t.Errorf("retention = %d, want 24", settings.SpamRetentionHours)
	}
}

func TestCurationSettingsRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if err := s.SaveCurationSettings(ctx, models.CurationSettings{SpamRetentionHours: 6}); err != nil {
		t.Fatalf("SaveCurationSettings: %v", err)
	}
	settings, err := s.CurationSettings(ctx)
	if err != nil {
		t.Fatalf("CurationSettings: %v", err)
	}
	if settings.SpamRetentionHours != 6 {
		t.Errorf("retention = %d, want 6", settings.SpamRetentionHours)
	}
}

func TestListSpamHonoursTheWindow(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	dropMessage(t, s, "recent spam", time.Hour)
	dropMessage(t, s, "old spam", 48*time.Hour)

	items, err := s.ListSpam(ctx, 24*time.Hour, 50, 0)
	if err != nil {
		t.Fatalf("ListSpam: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("listed %d items, want only the one inside the window", len(items))
	}
	if items[0].Text != "recent spam" {
		t.Errorf("listed %q, want the recent one", items[0].Text)
	}
}

func TestListSpamExcludesOtherStatuses(t *testing.T) {
	// The page must show what the classifier dropped, not what a curator is
	// still deciding about.
	s := newStore(t)

	if err := s.DB().Create(&models.Message{Text: "waiting", Status: models.StatusCurating}).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}
	dropMessage(t, s, "dropped", 0)

	items, err := s.ListSpam(context.Background(), 24*time.Hour, 50, 0)
	if err != nil {
		t.Fatalf("ListSpam: %v", err)
	}
	if len(items) != 1 || items[0].Text != "dropped" {
		t.Errorf("listed %+v, want only the dropped message", items)
	}
}

func TestCountSpam(t *testing.T) {
	s := newStore(t)

	dropMessage(t, s, "one", 0)
	dropMessage(t, s, "two", time.Hour)
	dropMessage(t, s, "expired", 72*time.Hour)

	count, err := s.CountSpam(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("CountSpam: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

func TestRescueSendsToTheCurationQueue(t *testing.T) {
	// Rescuing must not publish: being wrongly called spam is not the same as
	// being ready for the register.
	s := newStore(t)
	message := dropMessage(t, s, "a real doléance", 0)

	if err := s.RescueSpam(context.Background(), message.ID); err != nil {
		t.Fatalf("RescueSpam: %v", err)
	}

	var rescued models.Message
	if err := s.DB().First(&rescued, "id = ?", message.ID).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if rescued.Status != models.StatusCurating {
		t.Errorf("status = %q, want %q", rescued.Status, models.StatusCurating)
	}
}

func TestRescueOnlyTouchesDroppedMessages(t *testing.T) {
	s := newStore(t)

	published := models.Message{Text: "published", Status: models.StatusAccepted}
	if err := s.DB().Create(&published).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}
	if err := s.RescueSpam(context.Background(), published.ID); err == nil {
		t.Error("a published message was rescued from spam it was never in")
	}
}

func TestDeleteSpam(t *testing.T) {
	s := newStore(t)
	message := dropMessage(t, s, "spam", 0)

	if err := s.DeleteSpam(context.Background(), message.ID); err != nil {
		t.Fatalf("DeleteSpam: %v", err)
	}
	if err := s.DeleteSpam(context.Background(), message.ID); err == nil {
		t.Error("deleting a gone message succeeded")
	}
}

func TestPurgeRemovesOnlyExpiredSpam(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	kept := dropMessage(t, s, "recent", time.Hour)
	dropMessage(t, s, "expired", 48*time.Hour)
	curating := models.Message{Text: "waiting", Status: models.StatusCurating}
	if err := s.DB().Create(&curating).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}
	// Backdate it well past the window: the sweep must still not touch it.
	if err := s.DB().Model(&models.Message{}).Where("id = ?", curating.ID).
		UpdateColumn("updated_at", time.Now().Add(-72*time.Hour)).Error; err != nil {
		t.Fatalf("backdate: %v", err)
	}

	purged, err := s.PurgeSpam(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("PurgeSpam: %v", err)
	}
	if purged != 1 {
		t.Errorf("purged %d, want 1", purged)
	}

	var remaining int64
	if err := s.DB().Model(&models.Message{}).Count(&remaining).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 2 {
		t.Errorf("%d messages remain, want 2", remaining)
	}
	if err := s.DB().First(&models.Message{}, "id = ?", kept.ID).Error; err != nil {
		t.Errorf("the message inside the window was purged: %v", err)
	}
}

func TestPurgeIsRealDeletion(t *testing.T) {
	// Not a flag: the content was judged not to belong here, and a soft delete
	// would quietly keep it forever.
	s := newStore(t)
	dropMessage(t, s, "expired", 48*time.Hour)

	if _, err := s.PurgeSpam(context.Background(), time.Hour); err != nil {
		t.Fatalf("PurgeSpam: %v", err)
	}

	var count int64
	if err := s.DB().Unscoped().Model(&models.Message{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("%d rows survive an unscoped count; the purge did not delete", count)
	}
}

// TestCurationQueueIncludesUnassessed is the fail-safe this project needs:
// when no classifier has ruled on a submission, it must still reach a person.
// Humans are the last line, so an unassessed doléance belongs in the queue
// rather than in a state nobody is watching.
func TestCurationQueueIncludesUnassessed(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	for _, status := range []models.ReviewStatus{
		models.StatusPending,  // no classifier has seen it
		models.StatusCurating, // the classifier referred it
		models.StatusAccepted, // already published
		models.StatusRejected,
		models.StatusDropped, // refused by the classifier alone
	} {
		message := models.Message{Text: string(status), Status: status, TokenHash: "h"}
		if err := s.CreateMessage(ctx, &message); err != nil {
			t.Fatalf("CreateMessage(%s): %v", status, err)
		}
	}

	queue, err := s.ListCurationQueue(ctx, 100, 0)
	if err != nil {
		t.Fatalf("ListCurationQueue: %v", err)
	}
	if len(queue) != 2 {
		t.Fatalf("queue holds %d submissions, want 2 (pending and curating)", len(queue))
	}

	waiting := map[models.ReviewStatus]bool{}
	for _, message := range queue {
		waiting[message.Status] = true
	}
	if !waiting[models.StatusPending] {
		t.Error("an unassessed submission is missing from the queue: nothing would ever decide it")
	}
	if !waiting[models.StatusCurating] {
		t.Error("a referred submission is missing from the queue")
	}

	count, err := s.CountCurationQueue(ctx)
	if err != nil {
		t.Fatalf("CountCurationQueue: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

// TestAcceptPublishesWithoutAScore covers the point of the fail-safe: a
// curator may publish something no classifier ever scored.
func TestAcceptPublishesWithoutAScore(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	message := models.Message{Text: "jamais évaluée", Status: models.StatusPending, TokenHash: "h"}
	if err := s.CreateMessage(ctx, &message); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if err := s.AcceptMessage(ctx, message.ID); err != nil {
		t.Fatalf("AcceptMessage: %v", err)
	}

	stored, err := s.GetMessage(ctx, message.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if stored.Status != models.StatusAccepted {
		t.Errorf("status = %q, want accepted", stored.Status)
	}
	if stored.PublishedAt == nil {
		t.Error("no publication time was recorded")
	}

	published, err := s.ListPublishedMessages(ctx, 10)
	if err != nil {
		t.Fatalf("ListPublishedMessages: %v", err)
	}
	if len(published) != 1 {
		t.Errorf("the register holds %d messages, want 1", len(published))
	}
}

// TestDecidingTwiceIsRefused: two curators working the queue must not silently
// overwrite one another.
func TestDecidingTwiceIsRefused(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	message := models.Message{Text: "une fois", Status: models.StatusCurating, TokenHash: "h"}
	if err := s.CreateMessage(ctx, &message); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if err := s.AcceptMessage(ctx, message.ID); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	if err := s.AcceptMessage(ctx, message.ID); !errors.Is(err, ErrMessageNotFound) {
		t.Errorf("second accept err = %v, want ErrMessageNotFound", err)
	}
	if err := s.RejectMessage(ctx, message.ID); !errors.Is(err, ErrMessageNotFound) {
		t.Errorf("rejecting an accepted message err = %v, want ErrMessageNotFound", err)
	}
}

// TestRejectDeletes: content a curator refused is discarded, not archived.
// The audit log keeps the decision; there is no table of other people's
// rejected words.
func TestRejectDeletes(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	message := models.Message{Text: "du spam", Status: models.StatusCurating, TokenHash: "h"}
	if err := s.CreateMessage(ctx, &message); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if err := s.RejectMessage(ctx, message.ID); err != nil {
		t.Fatalf("RejectMessage: %v", err)
	}
	if _, err := s.GetMessage(ctx, message.ID); !errors.Is(err, ErrMessageNotFound) {
		t.Error("a rejected submission survived")
	}
}
