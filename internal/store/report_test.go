package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// accepted puts a published, unverified doléance in the register — the state
// most of it is in, since a score above the accept threshold publishes with
// nobody involved.
func accepted(t *testing.T, s *Store, text string) models.Message {
	t.Helper()

	now := time.Now()
	message := models.Message{
		Text: text, Status: models.StatusAccepted, TokenHash: "h", PublishedAt: &now,
	}
	if err := s.CreateMessage(context.Background(), &message); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	return message
}

// TestAReportTakesItOffTheRegisterAndGivesItToAHuman is the whole of what a
// report does.
func TestAReportTakesItOffTheRegisterAndGivesItToAHuman(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	message := accepted(t, s, "La maternité a fermé et il faut une heure de route.")

	published, _, err := s.FindMessages(ctx, MessageQuery{Limit: 10})
	if err != nil {
		t.Fatalf("FindMessages: %v", err)
	}
	if len(published) != 1 {
		t.Fatalf("%d in the register before the report, want 1", len(published))
	}

	if err := s.ReportMessage(ctx, message.ID); err != nil {
		t.Fatalf("ReportMessage: %v", err)
	}

	// Off the register.
	published, _, err = s.FindMessages(ctx, MessageQuery{Limit: 10})
	if err != nil {
		t.Fatalf("FindMessages: %v", err)
	}
	if len(published) != 0 {
		t.Error("a reported doléance is still in the public register")
	}

	// And in front of a person, which is the half that makes the removal
	// temporary rather than a deletion somebody performed without saying so.
	queue, err := s.ListCurationQueue(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListCurationQueue: %v", err)
	}
	if len(queue) != 1 || queue[0].ID != message.ID {
		t.Error("a reported doléance did not reach the curation queue")
	}

	// The text is untouched. A report is a request to look, not an edit.
	stored, err := s.GetMessage(ctx, message.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if stored.Text != message.Text {
		t.Error("reporting changed the doléance")
	}
}

// TestAcceptingAReportedDoleancePutsItBackVerified closes the loop, and the
// mark is the point: the next reader sees that a person read this one.
func TestAcceptingAReportedDoleancePutsItBackVerified(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	message := accepted(t, s, "Le centre de santé a fermé ses portes.")

	before, err := s.GetMessage(ctx, message.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if before.Verified {
		t.Fatal("a doléance published on a score is marked as read by a human")
	}

	if err := s.ReportMessage(ctx, message.ID); err != nil {
		t.Fatalf("ReportMessage: %v", err)
	}
	if err := s.AcceptMessage(ctx, message.ID); err != nil {
		t.Fatalf("AcceptMessage: %v", err)
	}

	after, err := s.GetMessage(ctx, message.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if !after.Verified {
		t.Error("a curator accepted it and it is not marked as verified")
	}
	if after.Status != models.StatusAccepted {
		t.Errorf("status = %q, want accepted", after.Status)
	}

	// Back in the register.
	published, _, err := s.FindMessages(ctx, MessageQuery{Limit: 10})
	if err != nil {
		t.Fatalf("FindMessages: %v", err)
	}
	if len(published) != 1 {
		t.Error("the restored doléance is not back in the register")
	}
}

// TestThePublicationTimeIsStampedOnce.
//
// A doléance can now be accepted twice — reported, then let stand — and
// stamping `published_at` again would move the moment the public first could
// read it to the moment somebody complained about it. In a project whose worth
// is being a faithful record, that is a falsified row in the one column that
// answers when it became public.
func TestThePublicationTimeIsStampedOnce(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	original := time.Now().Add(-72 * time.Hour).Round(time.Second)
	message := models.Message{
		Text:        "On ferme la poste et la supérette.",
		Status:      models.StatusAccepted,
		TokenHash:   "h",
		PublishedAt: &original,
	}
	if err := s.CreateMessage(ctx, &message); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if err := s.ReportMessage(ctx, message.ID); err != nil {
		t.Fatalf("ReportMessage: %v", err)
	}
	if err := s.AcceptMessage(ctx, message.ID); err != nil {
		t.Fatalf("AcceptMessage: %v", err)
	}

	stored, err := s.GetMessage(ctx, message.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if stored.PublishedAt == nil {
		t.Fatal("the publication time was cleared")
	}
	if !stored.PublishedAt.Equal(original) {
		t.Errorf("published_at moved to %v, want the original %v",
			stored.PublishedAt, original)
	}
}

// TestAFirstAcceptStillStampsTheTime, or the fix above would have traded one
// wrong row for another: a doléance a curator published out of the queue has
// no publication time until they do.
func TestAFirstAcceptStillStampsTheTime(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	message := models.Message{
		Text: "Le bus ne passe plus le dimanche.", Status: models.StatusCurating, TokenHash: "h",
	}
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
	if stored.PublishedAt == nil {
		t.Error("a doléance published by a curator has no publication time")
	}
	if !stored.Verified {
		t.Error("a doléance published by a curator is not marked as verified")
	}
}

// TestWhatCannotBeReported. Each refusal answers identically, because a reader
// finding out which one applied would learn the state of a submission that is
// not on the register.
func TestWhatCannotBeReported(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	pending := models.Message{Text: "en attente d'un curateur", TokenHash: "h"}
	if err := s.CreateMessage(ctx, &pending); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	verified := accepted(t, s, "Une doléance qu'un curateur a déjà lue.")
	if err := s.DB().Model(&models.Message{}).Where("id = ?", verified.ID).
		Update("verified", true).Error; err != nil {
		t.Fatalf("mark verified: %v", err)
	}

	reportedTwice := accepted(t, s, "Signalée une première fois.")
	if err := s.ReportMessage(ctx, reportedTwice.ID); err != nil {
		t.Fatalf("first ReportMessage: %v", err)
	}

	for _, c := range []struct {
		what string
		id   string
	}{
		{"a submission nobody has published", pending.ID},
		{"a doléance a curator already let stand", verified.ID},
		{"one that is already in the queue", reportedTwice.ID},
		{"an identifier naming nothing", "no-such-identifier"},
	} {
		if err := s.ReportMessage(ctx, c.id); !errors.Is(err, ErrMessageNotFound) {
			t.Errorf("%s: err = %v, want ErrMessageNotFound", c.what, err)
		}
	}
}
