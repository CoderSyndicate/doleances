package store

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/snapshot"
)

// TestASnapshotSourceStripsTheAccountReference is where the privacy claim
// about the full package is actually kept.
//
// `internal/snapshot` proves the builder exports nothing it was not given;
// this proves what it is given. A snapshot's whole statement about
// participation is "this many people were in this group" — the identifier
// saying *which* person is the join that would turn a public mirror into a way
// to follow somebody from one group to another, which is precisely what the
// two-identity-domain rule exists to prevent.
//
// It is the same operation account deletion performs, applied to a copy rather
// than to the original.
func TestASnapshotSourceStripsTheAccountReference(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")
	dominique := account(t, s, "Dominique")

	if err := s.JoinGroup(ctx, group.ID, dominique.ID); err != nil {
		t.Fatalf("JoinGroup: %v", err)
	}

	source := s.SnapshotSource()

	groups, err := source.Groups(ctx)
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("%d groups, want 1", len(groups))
	}
	// The memberships are still there — the count is the point, and losing it
	// would be a silent data-loss bug rather than a privacy improvement.
	if len(groups[0].Memberships) != 2 {
		t.Fatalf("%d memberships, want the author and the joiner", len(groups[0].Memberships))
	}
	for _, membership := range groups[0].Memberships {
		if membership.AccountID != "" {
			t.Errorf("a snapshot membership names an account: %q", membership.AccountID)
		}
	}

	// And the same for an intent, which is the finer-grained version of the
	// same fact: somebody said they were coming to one particular meeting.
	action, err := s.CreateAction(ctx, group.ID, ActionDraft{
		Title:    "Réunion mensuelle",
		Type:     models.ActionOneTime,
		StartsAt: ptr(time.Now().Add(72 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	occurrence := models.ActionOccurrence{ActionID: action.ID, StartsAt: time.Now().Add(72 * time.Hour)}
	if err := s.DB().Create(&occurrence).Error; err != nil {
		t.Fatalf("create occurrence: %v", err)
	}
	intent := models.Intent{
		OccurrenceID: occurrence.ID,
		AccountID:    dominique.ID,
		DeclaredAt:   time.Now(),
	}
	if err := s.DB().Create(&intent).Error; err != nil {
		t.Fatalf("create intent: %v", err)
	}

	actions, err := source.Actions(ctx)
	if err != nil {
		t.Fatalf("Actions: %v", err)
	}
	var seen int
	for _, action := range actions {
		for _, occurrence := range action.Occurrences {
			for _, intent := range occurrence.Intents {
				seen++
				if intent.AccountID != "" {
					t.Errorf("a snapshot intent names an account: %q", intent.AccountID)
				}
			}
		}
	}
	if seen != 1 {
		t.Errorf("%d intents in the snapshot, want the one that was declared", seen)
	}
}

func ptr[T any](value T) *T { return &value }

// TestNoPackageCarriesWhatSomebodyKept is the bookmark half of the same claim.
//
// A snapshot is a copy of the register, and a bookmark is not in the register:
// it is one reader's own list of what they wanted to keep. Exporting it would
// put "this person reads about hospital closures" into a file this project
// actively encourages strangers to download and rehost — the one failure that
// cannot be fixed after the fact.
//
// It is asserted on the bytes of the **full** package, which is the one that
// carries everything else: a guard that only checked the public package would
// pass a change that started exporting bookmarks to admins.
func TestNoPackageCarriesWhatSomebodyKept(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	dominique := account(t, s, "Dominique")
	message := models.Message{
		Text:        "La maternité a fermé et il faut une heure de route.",
		Status:      models.StatusAccepted,
		TokenHash:   "h",
		PublishedAt: ptr(time.Now()),
	}
	if err := s.CreateMessage(ctx, &message); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if err := s.AddBookmark(ctx, dominique.ID, models.BookmarkMessage, message.ID); err != nil {
		t.Fatalf("AddBookmark: %v", err)
	}

	var bookmark models.Bookmark
	if err := s.DB().First(&bookmark).Error; err != nil {
		t.Fatalf("read the bookmark back: %v", err)
	}

	var buf bytes.Buffer
	if _, err := snapshot.Build(ctx, &buf, s.SnapshotSource(), snapshot.Options{
		Kind:      snapshot.KindFull,
		Now:       time.Now(),
		Generator: "doleances-test",
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Read back decompressed as well as raw: a value could survive deflate in
	// a form a scan of the archive's own bytes would miss.
	whole := buf.String() + unpacked(t, buf.Bytes())

	// The doléance itself is supposed to be in there; what must not be is the
	// account, or the row that joins the two.
	if !strings.Contains(whole, "La maternité a fermé") {
		t.Fatal("the fixture proves nothing: the doléance is not in the package either")
	}
	for value, why := range map[string]string{
		dominique.ID: "an account identifier would say which reader kept it",
		bookmark.ID:  "a bookmark row is one reader's own list, not the register",
	} {
		if strings.Contains(whole, value) {
			t.Errorf("the full package contains %q: %s", value, why)
		}
	}
}

// unpacked is every entry of the archive, decompressed and concatenated.
func unpacked(t *testing.T, data []byte) string {
	t.Helper()

	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open the package: %v", err)
	}

	var whole strings.Builder
	for _, file := range archive.File {
		whole.WriteString(file.Name)
		opened, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		if _, err := io.Copy(&whole, opened); err != nil {
			t.Fatalf("read %s: %v", file.Name, err)
		}
		opened.Close() //nolint:errcheck,gosec // a reader over a buffer
	}
	return whole.String()
}
