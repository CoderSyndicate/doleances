package store

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// account makes somebody who has signed in, the way a passkey registration
// leaves them: a random handle, a chosen name, and nothing else at all.
func account(t *testing.T, s *Store, name string) models.Account {
	t.Helper()

	handle := make([]byte, 32)
	if _, err := rand.Read(handle); err != nil {
		t.Fatalf("read randomness: %v", err)
	}

	created := &models.Account{Handle: handle, Name: name}
	first := &models.Credential{
		CredentialID: append([]byte("cred-"), handle...),
		PublicKey:    []byte("not a real key"),
		Name:         "a device",
	}
	if err := s.CreateAccount(context.Background(), created, first); err != nil {
		t.Fatalf("CreateAccount(%q): %v", name, err)
	}
	return *created
}

// published makes a group that is on the map, the way a curator leaves one.
func published(t *testing.T, s *Store, name string) models.Group {
	t.Helper()
	ctx := context.Background()

	group := &models.Group{Name: name}
	if err := s.CreateGroup(ctx, group, account(t, s, "Camille").ID); err != nil {
		t.Fatalf("CreateGroup(%q): %v", name, err)
	}
	if err := s.AcceptGroup(ctx, group.ID); err != nil {
		t.Fatalf("AcceptGroup(%q): %v", name, err)
	}
	group.Status, group.Visible = models.StatusAccepted, true
	return *group
}

// TestASecondEditOverwritesThePendingOne: the author's latest intent is the
// one worth a curator's time, and two revisions of one group would be two
// answers to the same question.
func TestASecondEditOverwritesThePendingOne(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	for _, description := range []string{"première tentative", "ce que je voulais dire"} {
		if _, _, err := s.EditGroup(ctx, group.ID, GroupEdit{
			Name: "Collectif Citoyen", Description: description,
		}); err != nil {
			t.Fatalf("EditGroup(%q): %v", description, err)
		}
	}

	var count int64
	if err := s.DB().Model(&models.GroupRevision{}).
		Where("group_id = ?", group.ID).Count(&count).Error; err != nil {
		t.Fatalf("count revisions: %v", err)
	}
	if count != 1 {
		t.Errorf("%d revisions are waiting on one group, want 1", count)
	}

	revision, err := s.FindGroupRevision(ctx, group.ID)
	if err != nil {
		t.Fatalf("FindGroupRevision: %v", err)
	}
	if revision.Description != "ce que je voulais dire" {
		t.Errorf("description = %q, want the later edit", revision.Description)
	}
}

// TestAcceptingARevisionMovesTheGroup is the whole point of the mechanism:
// what a curator said yes to is what the map shows afterwards.
func TestAcceptingARevisionMovesTheGroup(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	moved := models.Location{Latitude: 51.2203212, Longitude: 6.7729157, Label: "Düsseldorf"}
	_, _, err := s.EditGroup(ctx, group.ID, GroupEdit{
		Name:        "Collectif de Guéret",
		Description: "On se retrouve le premier mardi.",
		Location:    &moved,
	})
	if err != nil {
		t.Fatalf("EditGroup: %v", err)
	}

	if err := s.AcceptGroupRevision(ctx, group.ID); err != nil {
		t.Fatalf("AcceptGroupRevision: %v", err)
	}

	live, err := s.GetGroup(ctx, group.ID)
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	if live.Name != "Collectif de Guéret" || live.Description != "On se retrouve le premier mardi." {
		t.Errorf("the accepted words did not reach the group: %+v", live)
	}
	// The pin travels with the revision, or moving a group would be the one
	// change that skipped review.
	if live.Location.Latitude != moved.Latitude || live.Location.Longitude != moved.Longitude {
		t.Errorf("location = %+v, want the accepted pin", live.Location)
	}
	// And the geohash is derived rather than carried, so it cannot disagree
	// with the coordinates it was written beside.
	if live.Location.Geohash == "" || live.Location.Geohash == group.Location.Geohash {
		t.Errorf("geohash = %q, want one recomputed from the new pin", live.Location.Geohash)
	}
	// It never left the map.
	if !live.Visible || live.Status != models.StatusAccepted {
		t.Errorf("the group left the map while its edit was reviewed: %+v", live)
	}

	if _, err := s.FindGroupRevision(ctx, group.ID); !errors.Is(err, ErrRevisionNotFound) {
		t.Errorf("err = %v, want the applied revision to be gone", err)
	}
}

// TestRejectingARevisionLeavesTheGroupAlone.
func TestRejectingARevisionLeavesTheGroupAlone(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	if _, _, err := s.EditGroup(ctx, group.ID, GroupEdit{Name: "Autre nom"}); err != nil {
		t.Fatalf("EditGroup: %v", err)
	}
	if err := s.RejectGroupRevision(ctx, group.ID); err != nil {
		t.Fatalf("RejectGroupRevision: %v", err)
	}

	live, err := s.GetGroup(ctx, group.ID)
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	if live.Name != "Collectif Citoyen" || !live.Visible {
		t.Errorf("a refused edit touched the published group: %+v", live)
	}
	// The name it asked for is free again, rather than held by a row nobody
	// can see.
	free, err := s.GroupNameAvailable(ctx, "Autre nom")
	if err != nil {
		t.Fatalf("GroupNameAvailable: %v", err)
	}
	if !free {
		t.Error("a rejected revision is still holding the name it proposed")
	}
}

// TestAPendingRevisionHoldsItsName. Telling somebody a name is free and then
// refusing them at the write is the race the live check exists to prevent.
func TestAPendingRevisionHoldsItsName(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	if _, _, err := s.EditGroup(ctx, group.ID, GroupEdit{Name: "Assemblée de Guéret"}); err != nil {
		t.Fatalf("EditGroup: %v", err)
	}

	free, err := s.GroupNameAvailable(ctx, "assemblée de guéret")
	if err != nil {
		t.Fatalf("GroupNameAvailable: %v", err)
	}
	if free {
		t.Error("a name a pending revision has claimed was reported free")
	}

	// And another group cannot rename onto it either.
	other := published(t, s, "Gilets Jaunes Guéret")
	if _, _, err := s.EditGroup(ctx, other.ID, GroupEdit{Name: "Assemblée de Guéret"}); !errors.Is(err, ErrNameTaken) {
		t.Errorf("err = %v, want ErrNameTaken", err)
	}
}

// TestARevisionClaimIsExclusive, on the same terms as a group or a doléance:
// two workers scoring one edit is a charge paid twice and a verdict silently
// overwritten.
func TestARevisionClaimIsExclusive(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	if _, _, err := s.EditGroup(ctx, group.ID, GroupEdit{Name: "Autre nom"}); err != nil {
		t.Fatalf("EditGroup: %v", err)
	}

	claimed := 0
	for range 4 {
		if _, err := s.ClaimGroupRevisionForAssessment(ctx); err == nil {
			claimed++
		}
	}
	if claimed != 1 {
		t.Errorf("%d workers claimed the same revision, want exactly 1", claimed)
	}
}

// TestAStrandedRevisionComesBack: a process killed between claiming and
// recording must not leave an edit that no sweep revisits and no curator sees.
func TestAStrandedRevisionComesBack(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	if _, _, err := s.EditGroup(ctx, group.ID, GroupEdit{Name: "Autre nom"}); err != nil {
		t.Fatalf("EditGroup: %v", err)
	}
	if _, err := s.ClaimGroupRevisionForAssessment(ctx); err != nil {
		t.Fatalf("ClaimGroupRevisionForAssessment: %v", err)
	}

	// While it is being worked on, it is in neither the queue nor the sweep.
	waiting, _, err := s.ListGroupRevisionQueue(ctx, 10)
	if err != nil {
		t.Fatalf("ListGroupRevisionQueue: %v", err)
	}
	if len(waiting) != 0 {
		t.Error("a revision being assessed appeared in the curation queue")
	}

	released, err := s.ReleaseStaleGroupRevisionAssessments(ctx, 0)
	if err != nil {
		t.Fatalf("ReleaseStaleGroupRevisionAssessments: %v", err)
	}
	if released != 1 {
		t.Fatalf("released %d, want 1 — the edit was stranded", released)
	}

	waiting, before, err := s.ListGroupRevisionQueue(ctx, 10)
	if err != nil {
		t.Fatalf("ListGroupRevisionQueue: %v", err)
	}
	if len(waiting) != 1 {
		t.Fatal("the released revision did not come back")
	}
	// The queue carries the group as it stands, because "is this a good
	// change?" is unanswerable without "to what?".
	if before[group.ID].Name != "Collectif Citoyen" {
		t.Errorf("the queue did not carry the published group: %+v", before)
	}
}
