package backend

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/CoderSyndicate/doleances/internal/models"
)

func reader(t *testing.T, a *API, name string) *caller {
	t.Helper()

	handle := make([]byte, 32)
	if _, err := rand.Read(handle); err != nil {
		t.Fatalf("read randomness: %v", err)
	}
	account := &models.Account{Handle: handle, Name: name}
	credential := &models.Credential{
		CredentialID: append([]byte("cred-"), handle...),
		PublicKey:    []byte("not a real key"),
		Name:         "a device",
	}
	if err := a.store.CreateAccount(context.Background(), account, credential); err != nil {
		t.Fatalf("CreateAccount(%q): %v", name, err)
	}
	return &caller{Account: *account}
}

func as(who *caller) context.Context {
	return context.WithValue(context.Background(), callerKey, who)
}

func registered(t *testing.T, a *API, text string) models.Message {
	t.Helper()

	now := time.Now()
	message := models.Message{
		Text: text, Status: models.StatusAccepted, TokenHash: "h", PublishedAt: &now,
	}
	if err := a.store.CreateMessage(context.Background(), &message); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	return message
}

// TestTheCachedRegisterDoesNotLendOneReadersListToAnother is the failure the
// cache was written to be incapable of, asserted rather than asserted about.
//
// The register's listing is cached and shared by everybody, which is only safe
// because nothing in it is per-reader. The "did I keep this" mark is the first
// thing on a public page that *is* per-reader, so it is computed on top of the
// cached rows rather than stored with them. If that ever moved inside the
// cache, this is what notices — and what it would be noticing is one person's
// reading history being shown to a stranger.
func TestTheCachedRegisterDoesNotLendOneReadersListToAnother(t *testing.T) {
	a := newAPI(t)

	dominique := reader(t, a, "Dominique")
	camille := reader(t, a, "Camille")
	message := registered(t, a, "La maternité a fermé et il faut une heure de route.")

	if err := a.store.AddBookmark(as(dominique), dominique.Account.ID,
		models.BookmarkMessage, message.ID); err != nil {
		t.Fatalf("AddBookmark: %v", err)
	}

	marks := func(ctx context.Context) bool {
		out, err := a.listMessages(ctx, &MessageListInput{Limit: 10})
		if err != nil {
			t.Fatalf("listMessages: %v", err)
		}
		if len(out.Body.Messages) != 1 {
			t.Fatalf("%d messages, want 1", len(out.Body.Messages))
		}
		return out.Body.Messages[0].Kept
	}

	// Dominique first, which is what warms the cache with their view of it.
	if !marks(as(dominique)) {
		t.Error("the reader who kept the doléance is not shown as having kept it")
	}
	if marks(as(camille)) {
		t.Error("a second reader was shown somebody else's bookmark")
	}
	if marks(context.Background()) {
		t.Error("an anonymous reader was shown somebody else's bookmark")
	}
	// And back again, because a cache that fixed itself on the second read
	// would pass the three above and still be wrong.
	if !marks(as(dominique)) {
		t.Error("the first reader lost their own mark once somebody else read the page")
	}
}

// TestThePermalinkAnswersTheSameWay. The doléance's own page is the one read
// that is deliberately uncached, so it is worth pinning that the mark arrives
// there too rather than only on the listing.
func TestThePermalinkAnswersTheSameWay(t *testing.T) {
	a := newAPI(t)

	dominique := reader(t, a, "Dominique")
	camille := reader(t, a, "Camille")
	message := registered(t, a, "Le bus ne passe plus le dimanche.")

	if err := a.store.AddBookmark(as(dominique), dominique.Account.ID,
		models.BookmarkMessage, message.ID); err != nil {
		t.Fatalf("AddBookmark: %v", err)
	}

	for _, who := range []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{"the reader who kept it", as(dominique), true},
		{"another reader", as(camille), false},
		{"nobody", context.Background(), false},
	} {
		out, err := a.getMessage(who.ctx, &MessageIDInput{ID: message.ID})
		if err != nil {
			t.Fatalf("getMessage for %s: %v", who.name, err)
		}
		if out.Body.Kept != who.want {
			t.Errorf("%s: kept = %v, want %v", who.name, out.Body.Kept, who.want)
		}
	}
}

// TestAnIdentifierNamingNothingCannotBeKept.
//
// Not a privacy guard, and it is worth being clear about which it is: the
// permalink deliberately resolves for a submission no curator has looked at,
// because the contributor holding that link needs to see what they wrote. So
// an identifier somebody holds already opens the text, and keeping it reveals
// nothing the permalink does not. What this refuses is an identifier that
// names nothing, which would put an entry in somebody's own list that resolves
// to a gap.
func TestAnIdentifierNamingNothingCannotBeKept(t *testing.T) {
	a := newAPI(t)
	dominique := reader(t, a, "Dominique")

	if _, err := a.keepText(as(dominique), bookmarkOf("message", "no-such-identifier")); err == nil {
		t.Error("an identifier that names nothing could be kept")
	}
	if _, err := a.keepText(as(dominique), bookmarkOf("historical", "no-such-passage")); err == nil {
		t.Error("a passage that does not exist could be kept")
	}

	// And a submission waiting on a curator can be kept, which is the same
	// answer its permalink gives: the contributor who wrote it is the likeliest
	// person to want it in their own list.
	pending := models.Message{Text: "en attente d'un curateur", TokenHash: "h"}
	if err := a.store.CreateMessage(context.Background(), &pending); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if _, err := a.keepText(as(dominique), bookmarkOf("message", pending.ID)); err != nil {
		t.Errorf("a submission readable at its permalink could not be kept: %v", err)
	}
}

// TestAKindFromOutsideIsRefused: it arrives in a request, so it is arbitrary
// text until proven otherwise, and a row with a kind nothing can read is a row
// nothing can ever show.
func TestAKindFromOutsideIsRefused(t *testing.T) {
	a := newAPI(t)
	dominique := reader(t, a, "Dominique")
	message := registered(t, a, "Plus de médecin dans le village.")

	if _, err := a.keepText(as(dominique), bookmarkOf("messages", message.ID)); err == nil {
		t.Error("a kind this register does not have was accepted")
	}
	if _, err := a.keepText(as(dominique), bookmarkOf("message", "")); err == nil {
		t.Error("a bookmark naming no text was accepted")
	}
	if _, err := a.releaseText(as(dominique),
		&BookmarkTargetInput{Kind: "whatever", Target: message.ID}); err == nil {
		t.Error("releasing a kind this register does not have was accepted")
	}
}

func bookmarkOf(kind, id string) *BookmarkInput {
	in := &BookmarkInput{}
	in.Body.Kind = kind
	in.Body.ID = id
	return in
}

// TestTheListIsTheOneTheReaderAskedFor — the whole feature, end to end through
// the handlers, including that taking something back is visible immediately.
func TestTheListIsTheOneTheReaderAskedFor(t *testing.T) {
	a := newAPI(t)
	dominique := reader(t, a, "Dominique")
	ctx := as(dominique)

	first := registered(t, a, "On ferme la poste et la supérette.")
	second := registered(t, a, "Je travaille et je n'y arrive plus.")

	for _, message := range []models.Message{first, second} {
		if _, err := a.keepText(ctx, bookmarkOf("message", message.ID)); err != nil {
			t.Fatalf("keepText: %v", err)
		}
	}

	out, err := a.listBookmarks(ctx, nil)
	if err != nil {
		t.Fatalf("listBookmarks: %v", err)
	}
	if len(out.Body.Kept) != 2 {
		t.Fatalf("%d kept, want 2", len(out.Body.Kept))
	}
	for _, entry := range out.Body.Kept {
		if entry.Message == nil {
			t.Fatalf("a kept entry of kind %q carries no doléance", entry.Kind)
		}
		// Every entry of this list is kept by definition, so the page can
		// offer the same control it offers everywhere else.
		if !entry.Message.Kept {
			t.Error("an entry of the reader's own list is not marked as kept")
		}
	}

	if _, err := a.releaseText(ctx,
		&BookmarkTargetInput{Kind: "message", Target: first.ID}); err != nil {
		t.Fatalf("releaseText: %v", err)
	}
	out, err = a.listBookmarks(ctx, nil)
	if err != nil {
		t.Fatalf("listBookmarks: %v", err)
	}
	if len(out.Body.Kept) != 1 || out.Body.Kept[0].Message.ID != second.ID {
		t.Error("taking a text back did not change the list")
	}
}
