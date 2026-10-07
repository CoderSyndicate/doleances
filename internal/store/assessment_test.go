package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CoderSyndicate/doleances/internal/models"
)

func pendingMessage(t *testing.T, s *Store, text string) models.Message {
	t.Helper()

	message := models.Message{Text: text, TokenHash: "h"}
	if err := s.CreateMessage(context.Background(), &message); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	return message
}

// TestClaimIsExclusive is the property the whole sweep rests on: two workers
// must never assess the same submission, or it is scored twice, charged twice,
// and the second verdict silently overwrites the first.
func TestClaimIsExclusive(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	pendingMessage(t, s, "une seule doléance")

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		claimed int
	)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ClaimForAssessment(ctx); err == nil {
				mu.Lock()
				claimed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if claimed != 1 {
		t.Errorf("%d workers claimed the same submission, want exactly 1", claimed)
	}
}

func TestClaimTakesTheOldestFirst(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	first := pendingMessage(t, s, "écrite en premier")
	time.Sleep(10 * time.Millisecond)
	pendingMessage(t, s, "écrite ensuite")

	claimed, err := s.ClaimForAssessment(ctx)
	if err != nil {
		t.Fatalf("ClaimForAssessment: %v", err)
	}
	// Oldest first, so nobody waits indefinitely behind a busy hour.
	if claimed.ID != first.ID {
		t.Errorf("claimed %q, want the oldest submission", claimed.Text)
	}
}

func TestClaimReportsAnEmptyQueue(t *testing.T) {
	s := newStore(t)

	if _, err := s.ClaimForAssessment(context.Background()); !errors.Is(err, ErrMessageNotFound) {
		t.Errorf("err = %v, want ErrMessageNotFound on an empty queue", err)
	}
}

func TestRecordAssessment(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	message := pendingMessage(t, s, "la maternité a fermé")

	if _, err := s.ClaimForAssessment(ctx); err != nil {
		t.Fatalf("ClaimForAssessment: %v", err)
	}
	err := s.RecordAssessment(ctx, message.ID, Verdict{
		Status:     models.StatusAccepted,
		Confidence: 94,
		Model:      "a-model",
		Reason:     "grief concret sur un service public",
	})
	if err != nil {
		t.Fatalf("RecordAssessment: %v", err)
	}

	stored, err := s.GetMessage(ctx, message.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	// The model's own sentence is kept: a score cannot be argued with and a
	// sentence can, which is the whole point of the dropped sample.
	if stored.AssessmentReason != "grief concret sur un service public" {
		t.Errorf("reason = %q, want the model's sentence", stored.AssessmentReason)
	}
	if stored.Status != models.StatusAccepted {
		t.Errorf("status = %q, want accepted", stored.Status)
	}
	if stored.Confidence != 94 {
		t.Errorf("confidence = %d, want 94", stored.Confidence)
	}
	// Which model decided has to be recoverable when thresholds are tuned or
	// a model is swapped.
	if stored.AssessedBy != "a-model" {
		t.Errorf("assessed_by = %q, want the model name", stored.AssessedBy)
	}
	if stored.AssessedAt == nil {
		t.Error("no assessment time was recorded")
	}
	if stored.PublishedAt == nil {
		t.Error("an accepted doléance has no publication time")
	}
}

// TestReturnForRetryCountsAttempts: the count is what stops a submission
// cycling for ever against a model that cannot answer it.
func TestReturnForRetryCountsAttempts(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	message := pendingMessage(t, s, "à réessayer")

	for want := 1; want <= 3; want++ {
		if _, err := s.ClaimForAssessment(ctx); err != nil {
			t.Fatalf("ClaimForAssessment: %v", err)
		}
		attempts, err := s.ReturnForRetry(ctx, message.ID)
		if err != nil {
			t.Fatalf("ReturnForRetry: %v", err)
		}
		if attempts != want {
			t.Errorf("attempts = %d, want %d", attempts, want)
		}
	}

	// And it is back in the queue each time, not stranded.
	stored, err := s.GetMessage(ctx, message.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if stored.Status != models.StatusPending {
		t.Errorf("status = %q, want pending", stored.Status)
	}
}

// TestStaleClaimsComeBack covers the one way a doléance could vanish without
// anybody deciding anything: a backend killed between claiming and recording
// leaves a row in `assessing`, which no sweep revisits and no curator sees.
func TestStaleClaimsComeBack(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	message := pendingMessage(t, s, "abandonnée en cours de route")

	if _, err := s.ClaimForAssessment(ctx); err != nil {
		t.Fatalf("ClaimForAssessment: %v", err)
	}

	// Not stale yet: a claim being worked on right now must not be stolen.
	released, err := s.ReleaseStaleAssessments(ctx, time.Hour)
	if err != nil {
		t.Fatalf("ReleaseStaleAssessments: %v", err)
	}
	if released != 0 {
		t.Errorf("released %d fresh claims, want 0", released)
	}

	// Now old enough to be assumed abandoned.
	released, err = s.ReleaseStaleAssessments(ctx, 0)
	if err != nil {
		t.Fatalf("ReleaseStaleAssessments: %v", err)
	}
	if released != 1 {
		t.Errorf("released %d, want 1", released)
	}

	stored, err := s.GetMessage(ctx, message.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if stored.Status != models.StatusPending {
		t.Errorf("status = %q, want pending — the doléance was stranded", stored.Status)
	}
}

// TestAssessingIsInvisibleToNobody: a claimed submission is briefly in neither
// the register nor the curation queue, which is tolerable only because the
// stale sweep brings it back. This pins that it really is absent from both, so
// the sweep is understood to be load-bearing rather than tidy-up.
func TestAssessingIsInvisibleToNobody(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	pendingMessage(t, s, "en cours d'évaluation")

	if _, err := s.ClaimForAssessment(ctx); err != nil {
		t.Fatalf("ClaimForAssessment: %v", err)
	}

	published, _, err := s.FindMessages(ctx, MessageQuery{Limit: 10})
	if err != nil {
		t.Fatalf("FindMessages: %v", err)
	}
	if len(published) != 0 {
		t.Error("a submission being assessed appeared in the public register")
	}

	queue, err := s.ListCurationQueue(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListCurationQueue: %v", err)
	}
	if len(queue) != 0 {
		t.Error("a submission being assessed appeared in the curation queue")
	}

	// Which is why this must work.
	if _, err := s.ReleaseStaleAssessments(ctx, 0); err != nil {
		t.Fatalf("ReleaseStaleAssessments: %v", err)
	}
	queue, err = s.ListCurationQueue(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListCurationQueue: %v", err)
	}
	if len(queue) != 1 {
		t.Error("the released submission did not come back to the queue")
	}
}

// TestUnclassifiedIsFoundByAbsence is the point of the classification time: a
// published doléance with no subjects is unfindable, and nothing else in the
// row says whether that is a failure or a correct answer.
func TestUnclassifiedIsFoundByAbsence(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	published := func(text string) models.Message {
		m := models.Message{Text: text, Status: models.StatusAccepted, TokenHash: "h"}
		if err := s.CreateMessage(ctx, &m); err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		return m
	}

	failed := published("classification failed for this one")
	succeeded := published("this one was classified")
	subjectless := published("about nothing in particular")
	pending := models.Message{Text: "not published", TokenHash: "h"}
	if err := s.CreateMessage(ctx, &pending); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	// Two successes: one that produced subjects and one that correctly
	// produced none. Both are marked, because "about nothing in particular" is
	// a real answer and asking again for ever would get the same one.
	for _, id := range []string{succeeded.ID, subjectless.ID} {
		if err := s.MarkClassified(ctx, id); err != nil {
			t.Fatalf("MarkClassified: %v", err)
		}
	}

	unclassified, err := s.ListUnclassified(ctx, 6, 50)
	if err != nil {
		t.Fatalf("ListUnclassified: %v", err)
	}
	if len(unclassified) != 1 {
		t.Fatalf("found %d unclassified, want only the failed one", len(unclassified))
	}
	if unclassified[0].ID != failed.ID {
		t.Errorf("found %q, want the one whose classification failed", unclassified[0].Text)
	}
}

// TestClassificationAttemptsAreCapped: a text the model cannot handle must
// stop being retried rather than be retried every quarter of an hour for the
// life of the register.
func TestClassificationAttemptsAreCapped(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	message := models.Message{Text: "unclassifiable", Status: models.StatusAccepted, TokenHash: "h"}
	if err := s.CreateMessage(ctx, &message); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	const cap = 3
	for attempt := 1; attempt <= cap; attempt++ {
		found, err := s.ListUnclassified(ctx, cap, 50)
		if err != nil {
			t.Fatalf("ListUnclassified: %v", err)
		}
		if len(found) != 1 {
			t.Fatalf("attempt %d: the message was not offered for retry", attempt)
		}

		count, err := s.CountClassificationAttempt(ctx, message.ID)
		if err != nil {
			t.Fatalf("CountClassificationAttempt: %v", err)
		}
		if count != attempt {
			t.Errorf("attempts = %d, want %d", count, attempt)
		}
	}

	// Past the cap it is left alone.
	found, err := s.ListUnclassified(ctx, cap, 50)
	if err != nil {
		t.Fatalf("ListUnclassified: %v", err)
	}
	if len(found) != 0 {
		t.Error("a message past the attempt cap is still being retried")
	}
}
