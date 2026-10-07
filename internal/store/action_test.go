package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CoderSyndicate/doleances/internal/models"
)

func oneTime(title string, when time.Time) ActionDraft {
	return ActionDraft{Title: title, Type: models.ActionOneTime, StartsAt: &when}
}

// recurrent builds a rhythm the way the API does: a rule plus the DTSTART
// that anchors it.
func recurrent(title, rule string) ActionDraft {
	start := time.Now().Add(24 * time.Hour)
	return ActionDraft{
		Title: title, Type: models.ActionRecurrent,
		RecurrenceRule: rule, StartsAt: &start,
	}
}

// TestAnActionStartsPending. A group being accepted is not a licence to put
// anything on the map under its name.
func TestAnActionStartsPending(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	action, err := s.CreateAction(ctx, group.ID, oneTime("Réunion publique", time.Now().Add(48*time.Hour)))
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	if action.Status != models.StatusPending {
		t.Errorf("status = %q, want pending", action.Status)
	}

	public, err := s.ListPublicActions(ctx, group.ID, 5)
	if err != nil {
		t.Fatalf("ListPublicActions: %v", err)
	}
	if len(public) != 0 {
		t.Error("an unassessed action is already on the group's page")
	}
}

// TestAnActionInheritsItsGroupsPlace: most actions happen where the group
// meets, and making somebody re-pin the same room every month is how a form
// stops being used.
func TestAnActionInheritsItsGroupsPlace(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	err := s.DB().Model(&models.Group{}).Where("id = ?", group.ID).
		Updates(map[string]any{
			"location_latitude": 46.1667, "location_longitude": 1.8667,
			"location_label": "Guéret",
		}).Error
	if err != nil {
		t.Fatalf("place the group: %v", err)
	}

	action, err := s.CreateAction(ctx, group.ID, recurrent("Réunion mensuelle", "FREQ=MONTHLY;BYDAY=1TU"))
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	if action.Location.Label != "Guéret" || action.Location.Latitude == 0 {
		t.Errorf("location = %+v, want the group's", action.Location)
	}

	// Somewhere else, when it is somewhere else.
	elsewhere := models.Location{Latitude: 45.7772, Longitude: 3.0870, Label: "Clermont-Ferrand"}
	action, err = s.UpdateAction(ctx, group.ID, action.ID, ActionDraft{
		Title: "Réunion mensuelle", Type: models.ActionRecurrent,
		RecurrenceRule: "FREQ=MONTHLY;BYDAY=1TU", StartsAt: action.StartsAt,
		Location: &elsewhere,
	})
	if err != nil {
		t.Fatalf("UpdateAction: %v", err)
	}
	if action.Location.Label != "Clermont-Ferrand" || action.Location.Geohash == "" {
		t.Errorf("location = %+v, want the new one with a derived geohash", action.Location)
	}
}

// TestEditingAnActionSendsItBackThroughAssessment, and clears the verdict
// reached about the words it no longer has.
func TestEditingAnActionSendsItBackThroughAssessment(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	action, err := s.CreateAction(ctx, group.ID, recurrent("Réunion", "FREQ=MONTHLY;BYDAY=1TU"))
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	if err := s.RecordActionAssessment(ctx, action.ID, Verdict{
		Status: models.StatusAccepted, Confidence: 95, Model: "a-model", Reason: "une réunion",
	}); err != nil {
		t.Fatalf("RecordActionAssessment: %v", err)
	}

	edited, err := s.UpdateAction(ctx, group.ID, action.ID, recurrent("Réunion", "FREQ=MONTHLY;BYDAY=1TH"))
	if err != nil {
		t.Fatalf("UpdateAction: %v", err)
	}
	if edited.Status != models.StatusPending {
		t.Errorf("status = %q, want pending", edited.Status)
	}
	if edited.Confidence != 0 || edited.AssessedAt != nil || edited.AssessmentReason != "" {
		t.Errorf("the previous assessment survived the edit: %+v", edited)
	}
}

// TestThePastRetiresItself, and an unconfirmed rhythm does too. A map full of
// meetings that stopped happening two years ago tells people the movement is
// dead.
func TestThePastRetiresItself(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	past, _ := s.CreateAction(ctx, group.ID, oneTime("Manifestation", time.Now().Add(-time.Hour)))
	future, _ := s.CreateAction(ctx, group.ID, oneTime("Réunion publique", time.Now().Add(48*time.Hour)))
	stale, _ := s.CreateAction(ctx, group.ID, recurrent("Ancienne réunion", "FREQ=MONTHLY;BYDAY=1TU"))
	live, _ := s.CreateAction(ctx, group.ID, recurrent("Réunion mensuelle", "FREQ=MONTHLY;BYDAY=1TU"))

	for _, id := range []string{past.ID, future.ID, stale.ID, live.ID} {
		if err := s.AcceptAction(ctx, id); err != nil {
			t.Fatalf("AcceptAction: %v", err)
		}
	}
	// One rhythm nobody has vouched for in over a year.
	err := s.DB().Model(&models.Action{}).Where("id = ?", stale.ID).
		Update("confirmed_at", time.Now().Add(-400*24*time.Hour)).Error
	if err != nil {
		t.Fatalf("age the action: %v", err)
	}

	retired, err := s.RetirePastActions(ctx, 365*24*time.Hour)
	if err != nil {
		t.Fatalf("RetirePastActions: %v", err)
	}
	if retired != 2 {
		t.Errorf("retired %d, want the past one and the unconfirmed one", retired)
	}

	public, err := s.ListPublicActions(ctx, group.ID, 10)
	if err != nil {
		t.Fatalf("ListPublicActions: %v", err)
	}
	if len(public) != 2 {
		t.Fatalf("%d actions are public, want 2: %+v", len(public), public)
	}

	// Retirement is not deletion: the rows are still there.
	var total int64
	s.DB().Model(&models.Action{}).Count(&total) //nolint:errcheck
	if total != 4 {
		t.Errorf("%d actions in the database, want all 4 kept", total)
	}
	// And the group's own list still shows them, because the people who wrote
	// them need to see what happened to them.
	own, err := s.ListGroupActions(ctx, group.ID)
	if err != nil {
		t.Fatalf("ListGroupActions: %v", err)
	}
	if len(own) != 4 {
		t.Errorf("%d actions in the group's own list, want 4", len(own))
	}
}

// TestConfirmingRestartsTheYear.
func TestConfirmingRestartsTheYear(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	action, _ := s.CreateAction(ctx, group.ID, recurrent("Réunion mensuelle", "FREQ=MONTHLY;BYDAY=1TU"))
	if err := s.AcceptAction(ctx, action.ID); err != nil {
		t.Fatalf("AcceptAction: %v", err)
	}
	err := s.DB().Model(&models.Action{}).Where("id = ?", action.ID).
		Update("confirmed_at", time.Now().Add(-400*24*time.Hour)).Error
	if err != nil {
		t.Fatalf("age the action: %v", err)
	}
	if _, err := s.RetirePastActions(ctx, 365*24*time.Hour); err != nil {
		t.Fatalf("RetirePastActions: %v", err)
	}

	if err := s.ConfirmAction(ctx, group.ID, action.ID); err != nil {
		t.Fatalf("ConfirmAction: %v", err)
	}
	public, _ := s.ListPublicActions(ctx, group.ID, 5)
	if len(public) != 1 {
		t.Error("a confirmed action did not come back")
	}
}

// TestAnAcceptedActionBringsItsGroupBack. A group that dropped off the map for
// inactivity returns by posting an action, and this is that.
func TestAnAcceptedActionBringsItsGroupBack(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	err := s.DB().Model(&models.Group{}).Where("id = ?", group.ID).
		Update("visible", false).Error
	if err != nil {
		t.Fatalf("hide the group: %v", err)
	}

	action, _ := s.CreateAction(ctx, group.ID, recurrent("Réunion mensuelle", "FREQ=MONTHLY;BYDAY=1TU"))
	if err := s.AcceptAction(ctx, action.ID); err != nil {
		t.Fatalf("AcceptAction: %v", err)
	}

	live, err := s.GetGroup(ctx, group.ID)
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	if !live.Visible {
		t.Error("an accepted action did not bring its group back onto the map")
	}
}

// TestAnActionBelongsToItsGroup: a valid identifier from another group must
// not reach this one.
func TestAnActionBelongsToItsGroup(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mine := published(t, s, "Collectif Citoyen")
	theirs := published(t, s, "Gilets Jaunes Guéret")

	action, _ := s.CreateAction(ctx, theirs.ID, recurrent("Réunion", "FREQ=MONTHLY;BYDAY=1TU"))

	if _, err := s.UpdateAction(ctx, mine.ID, action.ID, recurrent("Détourné", "FREQ=MONTHLY;BYDAY=1TU")); !errors.Is(err, ErrActionNotFound) {
		t.Errorf("err = %v, want ErrActionNotFound", err)
	}
	if err := s.DeleteAction(ctx, mine.ID, action.ID); !errors.Is(err, ErrActionNotFound) {
		t.Errorf("err = %v, want ErrActionNotFound", err)
	}
}

// TestTheActionClaimIsExclusive.
func TestTheActionClaimIsExclusive(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	if _, err := s.CreateAction(ctx, group.ID, recurrent("Réunion", "FREQ=MONTHLY;BYDAY=1TU")); err != nil {
		t.Fatalf("CreateAction: %v", err)
	}

	claimed := 0
	for range 4 {
		if _, err := s.ClaimActionForAssessment(ctx); err == nil {
			claimed++
		}
	}
	if claimed != 1 {
		t.Errorf("%d workers claimed the same action, want exactly 1", claimed)
	}
}

// TestAStrandedActionComesBack.
func TestAStrandedActionComesBack(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	if _, err := s.CreateAction(ctx, group.ID, recurrent("Réunion", "FREQ=MONTHLY;BYDAY=1TU")); err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	if _, err := s.ClaimActionForAssessment(ctx); err != nil {
		t.Fatalf("ClaimActionForAssessment: %v", err)
	}

	waiting, err := s.ListActionCurationQueue(ctx, 10)
	if err != nil {
		t.Fatalf("ListActionCurationQueue: %v", err)
	}
	if len(waiting) != 0 {
		t.Error("an action being assessed appeared in the curation queue")
	}

	released, err := s.ReleaseStaleActionAssessments(ctx, 0)
	if err != nil {
		t.Fatalf("ReleaseStaleActionAssessments: %v", err)
	}
	if released != 1 {
		t.Fatalf("released %d, want 1 — the action was stranded", released)
	}

	waiting, err = s.ListActionCurationQueue(ctx, 10)
	if err != nil {
		t.Fatalf("ListActionCurationQueue: %v", err)
	}
	if len(waiting) != 1 || waiting[0].Group.Name != "Collectif Citoyen" {
		t.Errorf("queue = %+v, want the action with the group that made it", waiting)
	}
}

// TestTheNextDateIsStoredNotComputed is the property this whole arrangement
// exists for: the rhythm is turned into a date once, when it is written, so a
// listing can sort on an ordinary column.
func TestTheNextDateIsStoredNotComputed(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	// A fourth Thursday, announced on one.
	start := time.Date(2026, 9, 24, 19, 0, 0, 0, time.UTC)
	action, err := s.CreateAction(ctx, group.ID, ActionDraft{
		Title: "Stammtisch", Type: models.ActionRecurrent,
		RecurrenceRule: "FREQ=MONTHLY;BYDAY=4TH", StartsAt: &start,
	})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	if action.NextOccurrenceAt == nil {
		t.Fatal("a rhythm was stored with no next date")
	}
	if action.NextOccurrenceAt.Weekday() != time.Thursday {
		t.Errorf("next is a %s", action.NextOccurrenceAt.Weekday())
	}

	// And it is on the row, not recomputed by the reader.
	var stored models.Action
	if err := s.DB().First(&stored, "id = ?", action.ID).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored.NextOccurrenceAt == nil || !stored.NextOccurrenceAt.Equal(*action.NextOccurrenceAt) {
		t.Errorf("stored next = %v, want %v", stored.NextOccurrenceAt, action.NextOccurrenceAt)
	}
}

// TestSoonestFirst is the ordering the page needed and did not have: before
// the next date was stored, recurrent actions had no `starts_at` and pushed
// genuinely imminent meetings down the list.
func TestSoonestFirst(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	soon := time.Now().Add(48 * time.Hour)
	later := time.Now().Add(30 * 24 * time.Hour)
	rhythmStart := time.Now().Add(7 * 24 * time.Hour)

	create := func(draft ActionDraft) models.Action {
		action, err := s.CreateAction(ctx, group.ID, draft)
		if err != nil {
			t.Fatalf("CreateAction(%s): %v", draft.Title, err)
		}
		if err := s.AcceptAction(ctx, action.ID); err != nil {
			t.Fatalf("AcceptAction: %v", err)
		}
		return action
	}

	create(ActionDraft{Title: "dans un mois", Type: models.ActionOneTime, StartsAt: &later})
	create(ActionDraft{Title: "après-demain", Type: models.ActionOneTime, StartsAt: &soon})
	create(ActionDraft{Title: "chaque semaine", Type: models.ActionRecurrent,
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=TH", StartsAt: &rhythmStart})
	// A rhythm nothing can read: it belongs last, not first.
	create(ActionDraft{Title: "illisible", Type: models.ActionRecurrent,
		RecurrenceRule: "FREQ=YEARLY", StartsAt: &rhythmStart})

	public, err := s.ListPublicActions(ctx, group.ID, 10)
	if err != nil {
		t.Fatalf("ListPublicActions: %v", err)
	}

	got := make([]string, 0, len(public))
	for _, action := range public {
		got = append(got, action.Title)
	}
	want := []string{"après-demain", "chaque semaine", "dans un mois", "illisible"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// TestARhythmMovesOnByItself. The date is stored, so something has to move it
// when it passes — otherwise a monthly meeting shows last month's date for
// ever and is then retired for having a date in the past.
func TestARhythmMovesOnByItself(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	start := time.Now().Add(-90 * 24 * time.Hour)
	action, err := s.CreateAction(ctx, group.ID, ActionDraft{
		Title: "Réunion mensuelle", Type: models.ActionRecurrent,
		RecurrenceRule: "FREQ=MONTHLY;BYDAY=1TU", StartsAt: &start,
	})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	if err := s.AcceptAction(ctx, action.ID); err != nil {
		t.Fatalf("AcceptAction: %v", err)
	}

	// Drag its next date into the past, as time would.
	stale := time.Now().Add(-time.Hour)
	err = s.DB().Model(&models.Action{}).Where("id = ?", action.ID).
		Update("next_occurrence_at", stale).Error
	if err != nil {
		t.Fatalf("age the action: %v", err)
	}

	moved, err := s.RefreshNextOccurrences(ctx, time.Now())
	if err != nil {
		t.Fatalf("RefreshNextOccurrences: %v", err)
	}
	if moved != 1 {
		t.Fatalf("moved %d, want 1", moved)
	}

	var stored models.Action
	if err := s.DB().First(&stored, "id = ?", action.ID).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored.NextOccurrenceAt == nil || stored.NextOccurrenceAt.Before(time.Now()) {
		t.Fatalf("next = %v, want a date in the future", stored.NextOccurrenceAt)
	}
	if stored.NextOccurrenceAt.Weekday() != time.Tuesday {
		t.Errorf("next is a %s, want Tuesday", stored.NextOccurrenceAt.Weekday())
	}

	// And it is still on the page: a live monthly meeting must not be retired
	// for having held one.
	if _, err := s.RetirePastActions(ctx, 365*24*time.Hour); err != nil {
		t.Fatalf("RetirePastActions: %v", err)
	}
	public, _ := s.ListPublicActions(ctx, group.ID, 5)
	if len(public) != 1 {
		t.Error("a recurring meeting was retired for having happened")
	}
}

// TestABackfillReachesDatedActionsToo.
//
// Rows written before the next-occurrence column existed have none, and a
// perfectly good meeting three weeks away would otherwise sort to the bottom
// with the things that are over. The refresh is what fills them in, so it
// cannot be limited to the recurring ones.
func TestABackfillReachesDatedActionsToo(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	soon := time.Now().Add(21 * 24 * time.Hour)
	action, err := s.CreateAction(ctx, group.ID, ActionDraft{
		Title: "Manifestation", Type: models.ActionOneTime, StartsAt: &soon,
	})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}

	// As an upgraded row arrives: a real date, and no next occurrence.
	err = s.DB().Model(&models.Action{}).Where("id = ?", action.ID).
		Update("next_occurrence_at", nil).Error
	if err != nil {
		t.Fatalf("blank the column: %v", err)
	}

	if _, err := s.RefreshNextOccurrences(ctx, time.Now()); err != nil {
		t.Fatalf("RefreshNextOccurrences: %v", err)
	}

	var stored models.Action
	if err := s.DB().First(&stored, "id = ?", action.ID).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored.NextOccurrenceAt == nil {
		t.Fatal("a dated action was not backfilled")
	}
	if !stored.NextOccurrenceAt.Equal(soon.Truncate(time.Second)) &&
		stored.NextOccurrenceAt.Sub(soon).Abs() > time.Second {
		t.Errorf("next = %v, want the action's own date %v", stored.NextOccurrenceAt, soon)
	}
}

// TestAnActionThatIsOverIsNotRewrittenForEver. Without the skip, every tick
// would write nil over nil on every finished action the register ever had.
func TestAnActionThatIsOverIsNotRewrittenForEver(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	past := time.Now().Add(-time.Hour)
	if _, err := s.CreateAction(ctx, group.ID, ActionDraft{
		Title: "Manifestation", Type: models.ActionOneTime, StartsAt: &past,
	}); err != nil {
		t.Fatalf("CreateAction: %v", err)
	}

	moved, err := s.RefreshNextOccurrences(ctx, time.Now())
	if err != nil {
		t.Fatalf("RefreshNextOccurrences: %v", err)
	}
	if moved != 0 {
		t.Errorf("moved %d, want nothing rewritten", moved)
	}
}

// TestTheSweepsOwnOrderPublishesAnAction covers the same rule as
// TestAnAcceptedActionBringsItsGroupBack, reached the way the sweep reaches
// it — which is the way it was silently broken.
//
// The sweep records its verdict first, and that already sets the status to
// accepted; only then does it publish. A guard refusing to accept an action
// that was already accepted therefore refused **every** automatically accepted
// action, and the reactivation never ran for any of them. A whole seeded run
// failed here with nothing visibly wrong on any page, because those groups
// happened to be published moments later for a different reason. The curator's
// path — which the test above walks — was fine, which is why this went
// unnoticed.
func TestTheSweepsOwnOrderPublishesAnAction(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Assemblée de Guéret")

	action, err := s.CreateAction(ctx, group.ID, ActionDraft{
		Title:    "Réunion mensuelle",
		Type:     models.ActionOneTime,
		StartsAt: ptr(time.Now().Add(72 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}

	// Inactivity took the group off the map, which is the case the
	// reactivation exists for.
	err = s.DB().Model(&models.Group{}).Where("id = ?", group.ID).
		Update("visible", false).Error
	if err != nil {
		t.Fatalf("hide the group: %v", err)
	}

	// Exactly what the sweep does: the verdict, then the publication.
	err = s.RecordActionAssessment(ctx, action.ID, Verdict{
		Status: models.StatusAccepted, Confidence: 95, Model: "a-model",
	})
	if err != nil {
		t.Fatalf("RecordActionAssessment: %v", err)
	}
	if err := s.AcceptAction(ctx, action.ID); err != nil {
		t.Fatalf("AcceptAction after the verdict was recorded: %v", err)
	}

	var reloaded models.Group
	if err := s.DB().First(&reloaded, "id = ?", group.ID).Error; err != nil {
		t.Fatalf("read the group: %v", err)
	}
	if !reloaded.Visible {
		t.Error("the group is still off the map after one of its actions was accepted")
	}
	if reloaded.LastActivityAt.IsZero() {
		t.Error("the group's activity was not recorded")
	}

	// Accepting again is success: somebody wanted it published and it is.
	if err := s.AcceptAction(ctx, action.ID); err != nil {
		t.Errorf("accepting an already-accepted action: %v", err)
	}
	// A missing one is still missing.
	if err := s.AcceptAction(ctx, "no-such-action"); !errors.Is(err, ErrActionNotFound) {
		t.Errorf("err = %v, want ErrActionNotFound", err)
	}
}
