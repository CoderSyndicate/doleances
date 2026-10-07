package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CoderSyndicate/doleances/internal/geo"
	"github.com/CoderSyndicate/doleances/internal/models"
)

// TestTwoGroupsCannotShareAName is what replaces duplicate detection for
// groups. The content guard that serves messages would refuse the second
// village to write the same reasonable sentence; a name is a different kind of
// claim, and one a person can be asked to change while filling the form.
func TestTwoGroupsCannotShareAName(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	camille := account(t, s, "Camille")

	first := &models.Group{Name: "Collectif Citoyen"}
	if err := s.CreateGroup(ctx, first, camille.ID); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	free, err := s.GroupNameAvailable(ctx, "Collectif Citoyen")
	if err != nil {
		t.Fatalf("GroupNameAvailable: %v", err)
	}
	if free {
		t.Error("a name a group already holds was offered as free")
	}

	// Tolerant, or the check is trivially walked past.
	for _, variant := range []string{"collectif citoyen", "  Collectif  Citoyen  ", "COLLECTIF CITOYEN"} {
		if free, _ := s.GroupNameAvailable(ctx, variant); free {
			t.Errorf("%q was offered as free", variant)
		}
		err := s.CreateGroup(ctx, &models.Group{Name: variant}, camille.ID)
		if !errors.Is(err, ErrNameTaken) {
			t.Errorf("CreateGroup(%q) = %v, want ErrNameTaken", variant, err)
		}
	}

	// A different name is fine.
	if err := s.CreateGroup(ctx, &models.Group{Name: "Gilets Jaunes Guéret"}, camille.ID); err != nil {
		t.Errorf("a distinct name was refused: %v", err)
	}
}

// TestCreatingEntersThePipelineRatherThanTheMap.
//
// Proposing a group says somebody real means to meet; it does not say the
// group belongs on a public map. If creation published, the assessment would
// be decorative — and an unmoderated public map of where people physically
// gather is the abuse surface the whole review exists for.
func TestCreatingEntersThePipelineRatherThanTheMap(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	group := &models.Group{Name: "Collectif Citoyen"}
	if err := s.CreateGroup(ctx, group, account(t, s, "Camille").ID); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	if group.Status != models.StatusPending {
		t.Errorf("status = %q, want pending", group.Status)
	}
	if group.Visible {
		t.Error("the group reached the map without anything deciding it should")
	}
	if group.NameKey == "" {
		t.Error("the name key was not derived, so the unique index guards nothing")
	}
	if free, _ := s.GroupNameAvailable(ctx, "Collectif Citoyen"); free {
		t.Error("the new group is not holding its own name")
	}
}

// TestTheAuthorIsTheFirstAdmin, which is the whole of what creating a group
// grants. There is no creator role and nothing permanent: a group must never
// depend on one irreplaceable person, and the succession rules can only work
// if the first admin is an ordinary admin.
func TestTheAuthorIsTheFirstAdmin(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	camille := account(t, s, "Camille")

	group := &models.Group{Name: "Collectif Citoyen"}
	if err := s.CreateGroup(ctx, group, camille.ID); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	membership, err := s.MembershipOf(ctx, group.ID, camille.ID)
	if err != nil {
		t.Fatalf("MembershipOf: %v", err)
	}
	if membership.Role != models.RoleAdmin {
		t.Errorf("role = %q, want admin", membership.Role)
	}

	admins, err := s.CountAdmins(ctx, group.ID)
	if err != nil {
		t.Fatalf("CountAdmins: %v", err)
	}
	if admins != 1 {
		t.Errorf("admins = %d, want exactly the author", admins)
	}
}

// TestARefusedNameLeavesNothingBehind: the group and its first admin are one
// fact or neither exists. A group with no admin is a door nobody can open —
// nobody can post an action, accept a change, or bring it back from hiding —
// and the succession rules go as far as deleting one rather than leaving it on
// the map.
func TestARefusedNameLeavesNothingBehind(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	camille := account(t, s, "Camille")

	if err := s.CreateGroup(ctx, &models.Group{Name: "Collectif Citoyen"}, camille.ID); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	dominique := account(t, s, "Dominique")
	err := s.CreateGroup(ctx, &models.Group{Name: "collectif citoyen"}, dominique.ID)
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("CreateGroup = %v, want ErrNameTaken", err)
	}

	joined, err := s.GroupsOf(ctx, dominique.ID)
	if err != nil {
		t.Fatalf("GroupsOf: %v", err)
	}
	if len(joined) != 0 {
		t.Errorf("a refused creation left %d memberships behind", len(joined))
	}
}

// TestEditingTheTextSendsItBackThroughAssessment.
//
// Without this, "submit something harmless, collect a 95, then edit it into
// something else" walks straight past the classifier, and the register
// publishes words nothing ever read.
func TestEditingTheTextSendsItBackThroughAssessment(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	group := &models.Group{Name: "Collectif Citoyen"}
	if err := s.CreateGroup(ctx, group, account(t, s, "Camille").ID); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	// As if a model had already ruled on the original words.
	scored := time.Now()
	err := s.DB().Model(&models.Group{}).Where("id = ?", group.ID).Updates(map[string]any{
		"status": models.StatusCurating, "confidence": 95,
		"assessed_at": scored, "assessed_by": "a-model",
		"assessment_reason": "reads like a real local group",
	}).Error
	if err != nil {
		t.Fatalf("seed an assessment: %v", err)
	}

	edited, applied, err := s.EditGroup(ctx, group.ID, GroupEdit{
		Name:        "Collectif Citoyen",
		Description: "Nous nous réunissons le premier mardi à la salle des fêtes.",
	})
	if err != nil {
		t.Fatalf("EditGroup: %v", err)
	}
	// Nothing was published, so there is nothing to protect: the edit lands on
	// the group itself rather than waiting behind a revision.
	if !applied {
		t.Error("an edit to a pending group was queued instead of applied")
	}

	if edited.Status != models.StatusPending {
		t.Errorf("status = %q, want pending — the scored text is not the text any more", edited.Status)
	}
	// A stale score beside new words is how a console shows a verdict that was
	// never reached.
	if edited.Confidence != 0 || edited.AssessedAt != nil || edited.AssessmentReason != "" {
		t.Errorf("the previous assessment survived the edit: %+v", edited)
	}
}

// TestRenamingClearsTheSameBarAsCreating, against everything except itself.
func TestRenamingClearsTheSameBarAsCreating(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	propose := func(name string) models.Group {
		group := &models.Group{Name: name}
		if err := s.CreateGroup(ctx, group, account(t, s, "Camille").ID); err != nil {
			t.Fatalf("CreateGroup(%q): %v", name, err)
		}
		return *group
	}

	first := propose("Collectif Citoyen")
	second := propose("Gilets Jaunes Guéret")

	_, _, err := s.EditGroup(ctx, second.ID, GroupEdit{Name: "collectif citoyen"})
	if !errors.Is(err, ErrNameTaken) {
		t.Errorf("err = %v, want ErrNameTaken — renaming onto a taken name", err)
	}
	// Keeping its own name is not a collision with itself.
	_, _, err = s.EditGroup(ctx, first.ID, GroupEdit{
		Name: "Collectif Citoyen", Description: "une description",
	})
	if err != nil {
		t.Errorf("a group could not keep its own name: %v", err)
	}
}

// TestAPublishedGroupIsNotEditedInPlace. Rewriting a live group would take it
// off the map while it was re-assessed, which is the one thing an edit must
// never do — so the change is written beside it instead, and the group carries
// on saying what a curator accepted until somebody accepts the new words.
func TestAPublishedGroupIsNotEditedInPlace(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	group := &models.Group{Name: "Collectif Citoyen"}
	if err := s.CreateGroup(ctx, group, account(t, s, "Camille").ID); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	err := s.DB().Model(&models.Group{}).Where("id = ?", group.ID).
		Updates(map[string]any{"status": models.StatusAccepted, "visible": true}).Error
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	_, applied, err := s.EditGroup(ctx, group.ID, GroupEdit{
		Name: "Autre nom", Description: "autre texte",
	})
	if err != nil {
		t.Fatalf("EditGroup: %v", err)
	}
	if applied {
		t.Fatal("an edit to a published group was applied directly")
	}

	// It is still on the map, with the words a curator accepted.
	var live models.Group
	if err := s.DB().First(&live, "id = ?", group.ID).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !live.Visible || live.Name != "Collectif Citoyen" {
		t.Errorf("an edit changed the published group: %+v", live)
	}

	// And the change is waiting rather than lost.
	revision, err := s.FindGroupRevision(ctx, group.ID)
	if err != nil {
		t.Fatalf("FindGroupRevision: %v", err)
	}
	if revision.Name != "Autre nom" || revision.Status != models.StatusPending {
		t.Errorf("revision = %+v, want the new words awaiting assessment", revision)
	}
}

// TestGroupClaimIsExclusive: two workers must never score the same group, or
// it is charged twice and the second verdict silently overwrites the first.
func TestGroupClaimIsExclusive(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	group := &models.Group{Name: "Collectif Citoyen"}
	if err := s.CreateGroup(ctx, group, account(t, s, "Camille").ID); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		claimed int
	)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ClaimGroupForAssessment(ctx); err == nil {
				mu.Lock()
				claimed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if claimed != 1 {
		t.Errorf("%d workers claimed the same group, want exactly 1", claimed)
	}
}

// TestAStrandedGroupComesBack. A backend killed between claiming and recording
// leaves a row nothing revisits and no curator sees — for a group that means a
// meeting nobody can find.
func TestAStrandedGroupComesBack(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	group := &models.Group{Name: "Collectif Citoyen"}
	if err := s.CreateGroup(ctx, group, account(t, s, "Camille").ID); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := s.ClaimGroupForAssessment(ctx); err != nil {
		t.Fatalf("ClaimGroupForAssessment: %v", err)
	}

	// A claim being worked on right now must not be stolen.
	released, err := s.ReleaseStaleGroupAssessments(ctx, time.Hour)
	if err != nil {
		t.Fatalf("ReleaseStaleGroupAssessments: %v", err)
	}
	if released != 0 {
		t.Errorf("released %d fresh claims, want 0", released)
	}

	// Old enough to be assumed abandoned.
	if released, err = s.ReleaseStaleGroupAssessments(ctx, 0); err != nil {
		t.Fatalf("ReleaseStaleGroupAssessments: %v", err)
	}
	if released != 1 {
		t.Errorf("released %d, want 1", released)
	}

	// And it is in front of a human, which is where an unassessed group goes.
	queue, err := s.ListGroupCurationQueue(ctx, 10)
	if err != nil {
		t.Fatalf("ListGroupCurationQueue: %v", err)
	}
	if len(queue) != 1 {
		t.Errorf("the released group did not reach the queue")
	}
}

// TestAcceptanceIsWhatPutsAGroupOnTheMap, and nothing else does.
func TestAcceptanceIsWhatPutsAGroupOnTheMap(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	group := &models.Group{Name: "Collectif Citoyen"}
	if err := s.CreateGroup(ctx, group, account(t, s, "Camille").ID); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if group.Visible {
		t.Fatal("a promoted group was already on the map")
	}

	// Curating is not publishing.
	if err := s.SendGroupToCuration(ctx, group.ID); err != nil {
		t.Fatalf("SendGroupToCuration: %v", err)
	}
	var queued models.Group
	if err := s.DB().First(&queued, "id = ?", group.ID).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if queued.Visible {
		t.Error("a group awaiting a human is on the map")
	}

	err := s.RecordGroupAssessment(ctx, group.ID, Verdict{
		Status: models.StatusAccepted, Confidence: 95, Model: "a-model",
		Reason: "un vrai groupe local",
	})
	if err != nil {
		t.Fatalf("RecordGroupAssessment: %v", err)
	}

	var accepted models.Group
	if err := s.DB().First(&accepted, "id = ?", group.ID).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !accepted.Visible {
		t.Error("an accepted group is not on the map")
	}
	if accepted.AssessmentReason != "un vrai groupe local" {
		t.Errorf("reason = %q, want the model's sentence", accepted.AssessmentReason)
	}
}

// TestAGroupOutsideTheViewportIsTrimmed.
//
// Geohash cells always cover at least the viewport, so they overhang its
// edges: a group in central France and a box over Germany share a coarse
// prefix, and the query alone returns it. Found by asking a live instance for
// a German viewport and being handed Guéret.
func TestAGroupOutsideTheViewportIsTrimmed(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	place := func(name string, lat, lng float64) {
		group := &models.Group{
			Name:     name,
			Location: models.Location{Latitude: lat, Longitude: lng, Label: name},
		}
		if err := s.CreateGroup(ctx, group, account(t, s, name).ID); err != nil {
			t.Fatalf("CreateGroup(%q): %v", name, err)
		}
		if err := s.AcceptGroup(ctx, group.ID); err != nil {
			t.Fatalf("AcceptGroup(%q): %v", name, err)
		}
	}

	place("Gueret", 46.1699, 1.8714)
	place("Cottbus", 51.7563, 14.3329)

	germany := &geo.Box{North: 55, South: 47, East: 15, West: 5}
	found, total, err := s.FindGroups(ctx, GroupQuery{Bounds: germany})
	if err != nil {
		t.Fatalf("FindGroups: %v", err)
	}
	// The viewport narrows what is drawn and never what is counted: the
	// spread of groups is the argument this map makes.
	if total != 2 {
		t.Errorf("total = %d, want 2 — a viewport must not narrow the count", total)
	}
	if len(found) != 1 || found[0].Name != "Cottbus" {
		names := make([]string, len(found))
		for i, g := range found {
			names[i] = g.Name
		}
		t.Errorf("a German viewport returned %v, want only Cottbus", names)
	}

	// And the whole world still returns both.
	all, _, err := s.FindGroups(ctx, GroupQuery{})
	if err != nil {
		t.Fatalf("FindGroups: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("got %d groups with no viewport, want 2", len(all))
	}
}

// TestOnlyAcceptedGroupsReachTheMap.
func TestOnlyAcceptedGroupsReachTheMap(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	group := &models.Group{
		Name:     "Collectif Citoyen",
		Location: models.Location{Latitude: 46.1699, Longitude: 1.8714, Label: "Guéret"},
	}
	if err := s.CreateGroup(ctx, group, account(t, s, "Camille").ID); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	found, _, err := s.FindGroups(ctx, GroupQuery{})
	if err != nil {
		t.Fatalf("FindGroups: %v", err)
	}
	if len(found) != 0 {
		t.Error("a group awaiting a decision is already on the map")
	}

	// But its page resolves: leaving the map is not disappearing, and that
	// page is how a hidden group comes back.
	if _, err := s.GetGroup(ctx, group.ID); err != nil {
		t.Errorf("GetGroup: %v", err)
	}

	if err := s.AcceptGroup(ctx, group.ID); err != nil {
		t.Fatalf("AcceptGroup: %v", err)
	}
	if found, _, _ = s.FindGroups(ctx, GroupQuery{}); len(found) != 1 {
		t.Error("an accepted group is not on the map")
	}
}
