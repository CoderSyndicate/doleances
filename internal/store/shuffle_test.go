package store

import (
	"context"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// TestTheRegisterOffersTwoOrders.
//
// Shuffled and unpaged, or newest-first and paged. The two cannot be one
// thing: `random()` draws a fresh order per query, so a second shuffled page
// would repeat some rows and skip others, and seeding it is not portable.
func TestTheRegisterOffersTwoOrders(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for i := 0; i < 60; i++ {
		m := models.Message{Text: "doléance", Status: models.StatusAccepted, TokenHash: "h"}
		if err := s.CreateMessage(ctx, &m); err != nil {
			t.Fatal(err)
		}
	}

	shuffledOnce, _, err := s.FindMessages(ctx, MessageQuery{Shuffled: true, Limit: 40})
	if err != nil {
		t.Fatalf("shuffled: %v", err)
	}
	if len(shuffledOnce) != 40 {
		t.Fatalf("got %d, want the 40 asked for", len(shuffledOnce))
	}
	shuffledAgain, _, _ := s.FindMessages(ctx, MessageQuery{Shuffled: true, Limit: 40})
	identical := true
	for i := range shuffledOnce {
		if shuffledOnce[i].ID != shuffledAgain[i].ID {
			identical = false
			break
		}
	}
	if identical {
		t.Error("two shuffled reads were identical, so a reload shows nothing new")
	}

	// Paged reads walk without repeating — the property the shuffled one
	// cannot have, and the whole reason for the switch.
	page1, _, _ := s.FindMessages(ctx, MessageQuery{Limit: 40})
	page2, _, _ := s.FindMessages(ctx, MessageQuery{Limit: 40, Offset: 40})
	seen := map[string]bool{}
	for _, m := range page1 {
		seen[m.ID] = true
	}
	for _, m := range page2 {
		if seen[m.ID] {
			t.Errorf("page 2 repeated %s", m.ID)
		}
	}
	if len(page2) != 20 {
		t.Errorf("page 2 has %d of the remaining 20", len(page2))
	}

	// An offset asked for alongside a shuffle is ignored rather than obeyed:
	// obeying it would hand back a page overlapping one the reader has seen,
	// with nothing to show it had.
	withBoth, _, _ := s.FindMessages(ctx, MessageQuery{Shuffled: true, Offset: 40, Limit: 40})
	if len(withBoth) != 40 {
		t.Errorf("a shuffled read with an offset returned %d, want a full page", len(withBoth))
	}
}
