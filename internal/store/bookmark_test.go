package store

import (
	"context"
	"testing"
	"time"

	"github.com/CoderSyndicate/doleances/internal/models"
)

func publishedMessage(t *testing.T, s *Store, text string) models.Message {
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

func TestKeepingAText(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	dominique := account(t, s, "Dominique")
	message := publishedMessage(t, s, "Le bus ne passe plus le dimanche, et je n'ai pas de voiture.")

	if err := s.AddBookmark(ctx, dominique.ID, models.BookmarkMessage, message.ID); err != nil {
		t.Fatalf("AddBookmark: %v", err)
	}

	kept, err := s.ListBookmarks(ctx, dominique.ID)
	if err != nil {
		t.Fatalf("ListBookmarks: %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("%d kept, want 1", len(kept))
	}
	// The text comes back with the bookmark, because every caller wants it and
	// resolving it is also what notices a bookmark pointing at nothing.
	if kept[0].Message == nil || kept[0].Message.ID != message.ID {
		t.Errorf("the kept text is not the doléance that was kept")
	}
}

// TestKeepingSomethingTwiceIsNotAnError, and the first press is the one the
// timestamp remembers: they kept it when they kept it.
func TestKeepingSomethingTwiceIsNotAnError(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	dominique := account(t, s, "Dominique")
	message := publishedMessage(t, s, "La maternité a fermé.")

	if err := s.AddBookmark(ctx, dominique.ID, models.BookmarkMessage, message.ID); err != nil {
		t.Fatalf("first AddBookmark: %v", err)
	}
	first, err := s.ListBookmarks(ctx, dominique.ID)
	if err != nil {
		t.Fatalf("ListBookmarks: %v", err)
	}

	if err := s.AddBookmark(ctx, dominique.ID, models.BookmarkMessage, message.ID); err != nil {
		t.Errorf("second AddBookmark: %v", err)
	}
	again, err := s.ListBookmarks(ctx, dominique.ID)
	if err != nil {
		t.Fatalf("ListBookmarks: %v", err)
	}
	if len(again) != 1 {
		t.Errorf("%d kept after pressing twice, want 1", len(again))
	}
	if !again[0].Bookmark.KeptAt.Equal(first[0].Bookmark.KeptAt) {
		t.Error("pressing again moved when they kept it")
	}
}

// TestTakingSomethingBack is the half that makes this a filing cabinet
// somebody chose rather than a record kept about them.
func TestTakingSomethingBack(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	dominique := account(t, s, "Dominique")
	message := publishedMessage(t, s, "On ferme la poste.")

	if err := s.AddBookmark(ctx, dominique.ID, models.BookmarkMessage, message.ID); err != nil {
		t.Fatalf("AddBookmark: %v", err)
	}
	if err := s.RemoveBookmark(ctx, dominique.ID, models.BookmarkMessage, message.ID); err != nil {
		t.Fatalf("RemoveBookmark: %v", err)
	}

	kept, err := s.ListBookmarks(ctx, dominique.ID)
	if err != nil {
		t.Fatalf("ListBookmarks: %v", err)
	}
	if len(kept) != 0 {
		t.Errorf("%d kept after taking it back, want 0", len(kept))
	}

	// And taking back something that is already gone is not a failure to show
	// somebody: their page may simply have been stale.
	if err := s.RemoveBookmark(ctx, dominique.ID, models.BookmarkMessage, message.ID); err != nil {
		t.Errorf("removing an absent bookmark: %v", err)
	}
}

// TestOneListPerReader: a bookmark names a person, which is exactly why it has
// to be the only person it answers to.
func TestOneListPerReader(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	dominique := account(t, s, "Dominique")
	camille := account(t, s, "Camille")
	message := publishedMessage(t, s, "Plus de médecin dans le village.")

	if err := s.AddBookmark(ctx, dominique.ID, models.BookmarkMessage, message.ID); err != nil {
		t.Fatalf("AddBookmark: %v", err)
	}

	kept, err := s.ListBookmarks(ctx, camille.ID)
	if err != nil {
		t.Fatalf("ListBookmarks: %v", err)
	}
	if len(kept) != 0 {
		t.Error("one reader's list showed another reader's bookmark")
	}

	// And both may keep the same text without one overwriting the other: the
	// uniqueness is per pair, not per text.
	if err := s.AddBookmark(ctx, camille.ID, models.BookmarkMessage, message.ID); err != nil {
		t.Fatalf("AddBookmark for a second reader: %v", err)
	}
	for _, who := range []models.Account{dominique, camille} {
		kept, err := s.ListBookmarks(ctx, who.ID)
		if err != nil {
			t.Fatalf("ListBookmarks(%s): %v", who.Name, err)
		}
		if len(kept) != 1 {
			t.Errorf("%s has %d kept, want 1", who.Name, len(kept))
		}
	}
}

// TestDeletedIsDeleted: a contributor who removes their doléance must not
// leave a row behind saying somebody kept it. "Deleted is deleted" is a
// promise this project makes in as many words, and a trace nobody happens to
// read is still a trace.
func TestDeletedIsDeleted(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	dominique := account(t, s, "Dominique")
	message := publishedMessage(t, s, "Je travaille et je n'y arrive plus.")

	if err := s.AddBookmark(ctx, dominique.ID, models.BookmarkMessage, message.ID); err != nil {
		t.Fatalf("AddBookmark: %v", err)
	}
	if err := s.DeleteMessage(ctx, message.ID); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}

	var remaining int64
	if err := s.DB().Model(&models.Bookmark{}).Count(&remaining).Error; err != nil {
		t.Fatalf("count bookmarks: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d bookmarks survived the doléance they pointed at", remaining)
	}
}

// TestAListHealsItself covers the deletion path nobody remembered to clean up
// after — a snapshot restore replacing the corpus, a table emptied by hand,
// the next way a text will be removed. The reader must not be shown gaps they
// cannot clear.
func TestAListHealsItself(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	dominique := account(t, s, "Dominique")
	message := publishedMessage(t, s, "Le centre de santé a fermé ses portes.")

	if err := s.AddBookmark(ctx, dominique.ID, models.BookmarkMessage, message.ID); err != nil {
		t.Fatalf("AddBookmark: %v", err)
	}
	// Removed behind the bookmark's back, which is what every path this
	// package does not know about looks like from here.
	if err := s.DB().Exec("DELETE FROM messages WHERE id = ?", message.ID).Error; err != nil {
		t.Fatalf("delete the message directly: %v", err)
	}

	kept, err := s.ListBookmarks(ctx, dominique.ID)
	if err != nil {
		t.Fatalf("ListBookmarks: %v", err)
	}
	if len(kept) != 0 {
		t.Errorf("%d kept, want none — the text is gone", len(kept))
	}

	// And the row is gone rather than merely hidden, so the list does not do
	// the same work again on every visit.
	var remaining int64
	if err := s.DB().Model(&models.Bookmark{}).Count(&remaining).Error; err != nil {
		t.Fatalf("count bookmarks: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d dangling bookmarks were shown and then kept", remaining)
	}
}

// TestErasureTakesTheListWithIt. Everything *about* an account goes, and a
// bookmark is about nobody else: unlike a membership, there is no count for it
// to keep true, so anonymising it would leave a record of what somebody read
// after they asked to stop existing, useful to no one.
func TestErasureTakesTheListWithIt(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	dominique := account(t, s, "Dominique")
	message := publishedMessage(t, s, "Il n'y a plus rien ici.")

	if err := s.AddBookmark(ctx, dominique.ID, models.BookmarkMessage, message.ID); err != nil {
		t.Fatalf("AddBookmark: %v", err)
	}
	if err := s.DeleteAccount(ctx, dominique.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}

	var remaining int64
	if err := s.DB().Model(&models.Bookmark{}).Count(&remaining).Error; err != nil {
		t.Fatalf("count bookmarks: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d bookmarks survived the account that made them", remaining)
	}
	// The doléance is untouched: it was somebody else's, and keeping it was
	// never a fact about it.
	if _, err := s.GetMessage(ctx, message.ID); err != nil {
		t.Errorf("the doléance went with the reader's list: %v", err)
	}
}

// TestWhichOfTheseDidIKeep is what a page of fifty cards asks, once, instead
// of asking per card.
func TestWhichOfTheseDidIKeep(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	dominique := account(t, s, "Dominique")

	kept := publishedMessage(t, s, "Celle-ci, je la garde.")
	other := publishedMessage(t, s, "Celle-là, non.")
	if err := s.AddBookmark(ctx, dominique.ID, models.BookmarkMessage, kept.ID); err != nil {
		t.Fatalf("AddBookmark: %v", err)
	}

	marks, err := s.BookmarkedBy(ctx, dominique.ID, models.BookmarkMessage,
		[]string{kept.ID, other.ID})
	if err != nil {
		t.Fatalf("BookmarkedBy: %v", err)
	}
	if !marks[kept.ID] {
		t.Error("the kept doléance is not marked")
	}
	if marks[other.ID] {
		t.Error("a doléance nobody kept is marked")
	}

	// A reader who is not signed in has kept nothing, which is a different
	// statement from "the rows belonging to nobody".
	none, err := s.BookmarkedBy(ctx, "", models.BookmarkMessage, []string{kept.ID})
	if err != nil {
		t.Fatalf("BookmarkedBy for nobody: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("an anonymous reader was told they kept %d things", len(none))
	}
}

// TestAPassageIsKeptLikeADoleance. The two registers are the same act
// performed twice, and the pages already say so: a button on one and not the
// other, side by side in the same list, would quietly take a position the rest
// of the design refuses.
func TestAPassageIsKeptLikeADoleance(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	dominique := account(t, s, "Dominique")

	passage := models.HistoricalText{
		Model:  models.Model{ID: "h-1789-1"},
		Title:  "Doléances du sexe",
		Text:   "Nous demandons à être instruites…",
		Source: "placeholder",
	}
	if _, err := s.SeedHistoricalText(ctx, passage); err != nil {
		t.Fatalf("SeedHistoricalText: %v", err)
	}
	message := publishedMessage(t, s, "Rien n'a changé depuis deux siècles.")

	if err := s.AddBookmark(ctx, dominique.ID, models.BookmarkHistorical, passage.ID); err != nil {
		t.Fatalf("AddBookmark(historical): %v", err)
	}
	if err := s.AddBookmark(ctx, dominique.ID, models.BookmarkMessage, message.ID); err != nil {
		t.Fatalf("AddBookmark(message): %v", err)
	}

	kept, err := s.ListBookmarks(ctx, dominique.ID)
	if err != nil {
		t.Fatalf("ListBookmarks: %v", err)
	}
	if len(kept) != 2 {
		t.Fatalf("%d kept, want both registers", len(kept))
	}

	var passages, doleances int
	for _, entry := range kept {
		switch {
		case entry.Historical != nil:
			passages++
		case entry.Message != nil:
			doleances++
		}
	}
	if passages != 1 || doleances != 1 {
		t.Errorf("kept %d passages and %d doléances, want one of each", passages, doleances)
	}

	// And the same identifier in the other register is a different bookmark,
	// which is what the kind is in the unique index for.
	if err := s.AddBookmark(ctx, dominique.ID, models.BookmarkMessage, passage.ID); err != nil {
		t.Fatalf("AddBookmark across kinds: %v", err)
	}
}
