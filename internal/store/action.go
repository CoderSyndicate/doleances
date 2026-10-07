package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/recur"
)

// ErrActionNotFound means no such action, or not in that group.
var ErrActionNotFound = errors.New("action not found")

// ActionDraft is what a group announces.
//
// The location is optional and absent means the group's own: most actions
// happen where the group meets, and making somebody re-pin the same room for
// every monthly meeting is how a form stops being used.
type ActionDraft struct {
	Title       string
	Description string
	Type        models.ActionType

	// StartsAt is when a one-time action happens, and DTSTART for a recurrent
	// one: the first occurrence, which also carries the time of day and
	// anchors any interval.
	StartsAt *time.Time

	// RecurrenceRule is the rhythm as an RFC 5545 rule, assembled by the form.
	RecurrenceRule string

	// RecurrenceNote is the nuance no rule carries. Nothing reads it but a
	// person.
	RecurrenceNote string

	Location *models.Location

	// InheritLocation asks for the group's meeting place, whatever the action
	// had before.
	//
	// It is not the same as a nil Location, which means "leave the place
	// alone" — the difference is the whole of how an action that was
	// announced somewhere else gets moved back.
	InheritLocation bool
}

// CreateAction records an announcement and sends it for assessment.
//
// Pending, never published: creating an action does not put it on a page any
// more than creating a group does. The group behind it was reviewed, which is
// why the bar is lower — not why there is no bar.
func (s *Store) CreateAction(ctx context.Context, groupID string, draft ActionDraft) (models.Action, error) {
	action := models.Action{
		GroupID:        groupID,
		Title:          draft.Title,
		Description:    draft.Description,
		Type:           draft.Type,
		StartsAt:       draft.StartsAt,
		RecurrenceRule: draft.RecurrenceRule,
		RecurrenceNote: draft.RecurrenceNote,
		Status:         models.StatusPending,
		ConfirmedAt:    time.Now(),
	}
	action.NextOccurrenceAt = NextOccurrence(action, time.Now())

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var group models.Group
		if err := tx.First(&group, "id = ?", groupID).Error; err != nil {
			return err
		}

		action.Location = group.Location
		if draft.Location != nil {
			action.Location = *draft.Location
		}
		return tx.Create(&action).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return action, ErrGroupNotFound
	}
	return action, err
}

// UpdateAction changes an announcement, which sends it back for assessment.
//
// In place rather than through a revision, and that is a deliberate difference
// from a group. A group is an identity people navigate to and hold a link
// for; an action is a date. Keeping a stale time on the map while a correction
// waits for a curator would send people to a meeting that is not happening,
// which is worse than the action briefly not being listed.
func (s *Store) UpdateAction(ctx context.Context, groupID, id string, draft ActionDraft) (models.Action, error) {
	var action models.Action

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("id = ? AND group_id = ?", id, groupID).First(&action).Error
		if err != nil {
			return err
		}

		fields := map[string]any{
			"title":           draft.Title,
			"description":     draft.Description,
			"type":            draft.Type,
			"starts_at":       draft.StartsAt,
			"recurrence_rule": draft.RecurrenceRule,
			"recurrence_note": draft.RecurrenceNote,
			"updated_at":      time.Now(),

			// Derived here rather than by the caller, so no write path can
			// leave a rhythm and a next date disagreeing — the same rule the
			// geohash follows, for the same reason.
			"next_occurrence_at": NextOccurrence(models.Action{
				Type: draft.Type, StartsAt: draft.StartsAt,
				RecurrenceRule: draft.RecurrenceRule,
			}, time.Now()),

			// Back to the queue with the old verdict cleared, so nothing shows
			// a score that was reached about different words.
			"status":              models.StatusPending,
			"confidence":          0,
			"assessed_at":         nil,
			"assessed_by":         "",
			"assessment_attempts": 0,
			"assessment_reason":   "",

			// An edited action is being looked after, so the clock that
			// retires neglected ones starts again.
			"confirmed_at": time.Now(),
			"retired":      false,
		}
		place := draft.Location
		if draft.InheritLocation {
			// Back to where the group meets. Read now rather than remembered
			// from creation: the group may have moved since, and an action
			// that says "where we meet" should mean where they meet.
			var group models.Group
			if err := tx.First(&group, "id = ?", action.GroupID).Error; err != nil {
				return err
			}
			place = &group.Location
		}
		if place != nil {
			location := *place
			location.Refresh()
			fields["location_latitude"] = location.Latitude
			fields["location_longitude"] = location.Longitude
			fields["location_geohash"] = location.Geohash
			fields["location_label"] = location.Label
			fields["location_country_code"] = location.CountryCode
			action.Location = location
		}

		if err := tx.Model(&models.Action{}).Where("id = ?", id).Updates(fields).Error; err != nil {
			return err
		}

		action.Title, action.Description = draft.Title, draft.Description
		action.Type, action.StartsAt = draft.Type, draft.StartsAt
		action.RecurrenceRule, action.RecurrenceNote = draft.RecurrenceRule, draft.RecurrenceNote
		action.NextOccurrenceAt = NextOccurrence(action, time.Now())
		action.Status, action.Confidence, action.Retired = models.StatusPending, 0, false
		action.AssessedAt, action.AssessedBy, action.AssessmentReason = nil, "", ""
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return action, ErrActionNotFound
	}
	return action, err
}

// DeleteAction removes an announcement outright.
//
// Deletion, not retirement, and the two are different acts by different
// people. Retirement is what time does to an action nobody confirmed; deleting
// is a group saying this is not happening. A retired action stays because the
// record of what a group did is worth keeping; a cancelled one that was never
// real is not.
func (s *Store) DeleteAction(ctx context.Context, groupID, id string) error {
	result := s.db.WithContext(ctx).
		Delete(&models.Action{}, "id = ? AND group_id = ?", id, groupID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrActionNotFound
	}
	return nil
}

// ConfirmAction says a recurrent action still happens, restarting its year.
func (s *Store) ConfirmAction(ctx context.Context, groupID, id string) error {
	result := s.db.WithContext(ctx).Model(&models.Action{}).
		Where("id = ? AND group_id = ?", id, groupID).
		Updates(map[string]any{
			"confirmed_at": time.Now(),
			"retired":      false,
			"updated_at":   time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrActionNotFound
	}
	return nil
}

// ListGroupActions returns everything a group has announced, at any status.
//
// Everything, because this is the list its own organisers manage: an action
// waiting on a curator or refused by one has to be visible to the people who
// wrote it, or they will write it again.
func (s *Store) ListGroupActions(ctx context.Context, groupID string) ([]models.Action, error) {
	var actions []models.Action
	err := s.db.WithContext(ctx).
		Where("group_id = ?", groupID).
		// Soonest first, so the next thing this group does is the first row —
		// which is what its organisers came to the page to see. Whatever has
		// no next date, because it is over or because its rhythm says nothing,
		// falls to the bottom where it belongs.
		Order(soonestFirst).
		Order("created_at desc").
		Find(&actions).Error
	return actions, err
}

// ListPublicActions returns what a group is actually offering, soonest first.
//
// Accepted and not retired: the two ways an action leaves a page. One-time
// actions whose date has passed are excluded here as well as by the sweep, so
// a page is right even in the minutes before the sweep runs.
func (s *Store) ListPublicActions(ctx context.Context, groupID string, limit int) ([]models.Action, error) {
	if limit <= 0 {
		limit = 5
	}

	var actions []models.Action
	err := s.db.WithContext(ctx).
		Where("group_id = ? AND status = ? AND retired = ?",
			groupID, models.StatusAccepted, false).
		Where("next_occurrence_at IS NULL OR next_occurrence_at >= ?", time.Now()).
		Order(soonestFirst).
		Limit(limit).
		Find(&actions).Error
	return actions, err
}

// soonestFirst orders by when a thing next happens, with the ones that have no
// next date at the end.
//
// The two-part expression is what makes that true on both engines. `ORDER BY
// column ASC` sorts NULL first in SQLite and last in PostgreSQL, so an
// undated action would lead the list on one and trail it on the other — and
// `NULLS LAST` is PostgreSQL-only, which the engine-neutral rule forbids.
// Comparing the column to NULL yields 0 or 1 in SQLite and false or true in
// PostgreSQL, and both sort the same way.
//
// This was a real defect before actions had a computed date: recurrent ones
// had no `starts_at`, so they pushed genuinely imminent meetings down the
// "next five".
const soonestFirst = "next_occurrence_at IS NULL, next_occurrence_at asc"

// NextOccurrence is when an action next happens, or nil when nothing is.
//
// A one-time action is its own next occurrence until its date passes. A
// recurrent one is whatever its rule yields after now, counted from the start
// of the series — which is why DTSTART is the action's own StartsAt rather
// than something separate.
//
// A rule nothing can read yields nil rather than an error. It is not a
// caller's problem: an action with no next date is simply listed last, and a
// curator or its own group can see that its rhythm says nothing.
func NextOccurrence(action models.Action, after time.Time) *time.Time {
	if action.Type == models.ActionOneTime {
		if action.StartsAt == nil || action.StartsAt.Before(after) {
			return nil
		}
		return action.StartsAt
	}

	if action.RecurrenceRule == "" || action.StartsAt == nil {
		return nil
	}
	rule, err := recur.Parse(action.RecurrenceRule)
	if err != nil {
		return nil
	}

	// Strictly after `after`, so an occurrence happening right now has already
	// been and the next one is the one worth showing.
	next, ok := recur.Next(rule, *action.StartsAt, after)
	if !ok {
		return nil
	}
	return &next
}

// RefreshNextOccurrences moves every rhythm on whose next date has passed.
//
// The whole point of storing the date is that nothing computes it per request,
// which means something has to compute it when it goes stale. This is that,
// and it runs on the same tick as the retirement sweep — a meeting that
// happened last night should be showing next month's date by the morning.
func (s *Store) RefreshNextOccurrences(ctx context.Context, now time.Time) (int64, error) {
	// Both kinds, not only the recurrent ones.
	//
	// A one-time action needs this too, and for a reason that only shows up
	// once: rows written before this column existed have no next date at all,
	// so a perfectly good meeting three weeks away sorts to the bottom with
	// the things that are over. Recomputing both backfills them on the first
	// sweep and costs nothing afterwards — a dated action whose date has not
	// moved is written once and then skipped.
	var actions []models.Action
	err := s.db.WithContext(ctx).
		Where("retired = ?", false).
		Where("next_occurrence_at IS NULL OR next_occurrence_at < ?", now).
		Find(&actions).Error
	if err != nil {
		return 0, err
	}

	var moved int64
	for _, action := range actions {
		next := NextOccurrence(action, now)
		if next == nil && action.NextOccurrenceAt == nil {
			// Nothing to say and nothing to write. Without this an action
			// that is simply over would be rewritten on every tick for ever.
			continue
		}

		err := s.db.WithContext(ctx).Model(&models.Action{}).
			Where("id = ?", action.ID).
			Updates(map[string]any{"next_occurrence_at": next}).Error
		if err != nil {
			return moved, err
		}
		moved++
	}
	return moved, nil
}

// RetirePastActions takes finished and unconfirmed actions off the map.
//
// A map full of meetings that stopped happening two years ago is worse than an
// empty one: it tells people the movement is dead. The two kinds expire in
// opposite ways, which is why they are two statements.
//
// It is not deletion. The row and its history stay; only the listing changes.
func (s *Store) RetirePastActions(ctx context.Context, window time.Duration) (int64, error) {
	now := time.Now()
	var retired int64

	// A one-time action is over when its date passes. Nobody has to decide,
	// and a recurrent one is never caught by this: it has a next date, which
	// RefreshNextOccurrences has already moved on.
	result := s.db.WithContext(ctx).Model(&models.Action{}).
		Where("retired = ? AND type = ? AND starts_at IS NOT NULL AND starts_at < ?",
			false, models.ActionOneTime, now).
		Updates(map[string]any{"retired": true, "updated_at": now})
	if result.Error != nil {
		return retired, result.Error
	}
	retired += result.RowsAffected

	// A recurrent one has no date to expire on, so it needs the opposite
	// guard: somebody has to say it still happens.
	result = s.db.WithContext(ctx).Model(&models.Action{}).
		Where("retired = ? AND type = ? AND confirmed_at < ?",
			false, models.ActionRecurrent, now.Add(-window)).
		Updates(map[string]any{"retired": true, "updated_at": now})
	if result.Error != nil {
		return retired, result.Error
	}
	return retired + result.RowsAffected, nil
}

// ---------------------------------------------------------------------------
// The assessment sweep, for actions
// ---------------------------------------------------------------------------

// ClaimActionForAssessment takes one pending announcement to assess.
func (s *Store) ClaimActionForAssessment(ctx context.Context) (models.Action, error) {
	var claimed models.Action

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidate models.Action
		err := tx.Where("status = ?", models.StatusPending).
			Order("created_at asc").
			First(&candidate).Error
		if err != nil {
			return err
		}

		result := tx.Model(&models.Action{}).
			Where("id = ? AND status = ?", candidate.ID, models.StatusPending).
			Updates(map[string]any{
				"status":     models.StatusAssessing,
				"updated_at": time.Now(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}

		claimed = candidate
		claimed.Status = models.StatusAssessing
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.Action{}, ErrActionNotFound
	}
	return claimed, err
}

// RecordActionAssessment stores a verdict.
func (s *Store) RecordActionAssessment(ctx context.Context, id string, verdict Verdict) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&models.Action{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":            verdict.Status,
			"confidence":        verdict.Confidence,
			"assessed_at":       now,
			"assessed_by":       verdict.Model,
			"assessment_reason": verdict.Reason,
			"updated_at":        now,
		}).Error
}

// ReturnActionForRetry puts a claimed announcement back, counting the attempt.
func (s *Store) ReturnActionForRetry(ctx context.Context, id string) (attempts int, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var action models.Action
		if err := tx.Where("id = ?", id).First(&action).Error; err != nil {
			return err
		}
		attempts = action.AssessmentAttempts + 1

		return tx.Model(&models.Action{}).Where("id = ?", id).
			Updates(map[string]any{
				"status":              models.StatusPending,
				"assessment_attempts": attempts,
				"updated_at":          time.Now(),
			}).Error
	})
	return attempts, err
}

// SendActionToCuration hands an announcement to a human.
func (s *Store) SendActionToCuration(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Model(&models.Action{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":     models.StatusCurating,
			"updated_at": time.Now(),
		}).Error
}

// ReleaseStaleActionAssessments rescues announcements stranded mid-claim.
func (s *Store) ReleaseStaleActionAssessments(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)

	result := s.db.WithContext(ctx).Model(&models.Action{}).
		Where("status = ? AND updated_at < ?", models.StatusAssessing, cutoff).
		Updates(map[string]any{
			"status":     models.StatusPending,
			"updated_at": time.Now(),
		})
	return result.RowsAffected, result.Error
}

// ActionQueueItem is one announcement awaiting a decision, with the group that
// made it — a curator judging "can people come to this?" needs to know whose
// meeting it is.
type ActionQueueItem struct {
	Action models.Action
	Group  models.Group
}

// ListActionCurationQueue returns the announcements waiting.
func (s *Store) ListActionCurationQueue(ctx context.Context, limit int) ([]ActionQueueItem, error) {
	if limit <= 0 {
		limit = 100
	}

	var actions []models.Action
	err := s.db.WithContext(ctx).
		Where("status IN ?", []models.ReviewStatus{models.StatusCurating, models.StatusPending}).
		Order("created_at asc").
		Limit(limit).
		Find(&actions).Error
	if err != nil || len(actions) == 0 {
		return nil, err
	}

	ids := make([]string, 0, len(actions))
	for _, action := range actions {
		ids = append(ids, action.GroupID)
	}

	var groups []models.Group
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&groups).Error; err != nil {
		return nil, err
	}
	byID := make(map[string]models.Group, len(groups))
	for _, group := range groups {
		byID[group.ID] = group
	}

	items := make([]ActionQueueItem, 0, len(actions))
	for _, action := range actions {
		items = append(items, ActionQueueItem{Action: action, Group: byID[action.GroupID]})
	}
	return items, nil
}

// AcceptAction publishes an announcement and brings its group back onto the
// map.
//
// # Accepting one that is already accepted is not an error
//
// It used to be, through a `status <> accepted` guard meant to stop a curator
// deciding twice, and that made the whole second half of this function
// unreachable from the path that uses it most. The assessment sweep records
// the verdict first — which sets the status to accepted — and then calls this
// to publish, so every automatically accepted action came back
// `ErrActionNotFound` and no group was ever reactivated by one. A whole run's
// actions failed here with nothing visibly wrong on any page, because the
// groups in that run happened to be published moments later for a different
// reason.
//
// So the guard is on the row existing rather than on the state it is in: the
// row is looked up, a missing one is still ErrActionNotFound, and reaching a
// state the row is already in is success. That is the same answer this project
// gives for joining a group twice, for the same reason — somebody wanted the
// action published and it is published.
func (s *Store) AcceptAction(ctx context.Context, id string) error {
	now := time.Now()

	var action models.Action
	if err := s.db.WithContext(ctx).First(&action, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrActionNotFound
		}
		return err
	}

	err := s.db.WithContext(ctx).Model(&models.Action{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status": models.StatusAccepted,
			// An action accepted now is not one time retired. This matters for
			// an edit re-entering assessment: without it a corrected meeting
			// would be published and stay off the map.
			"retired":    false,
			"updated_at": now,
		}).Error
	if err != nil {
		return err
	}

	// An accepted action is a sign of life, so the group it belongs to comes
	// back onto the map if inactivity had taken it off. This is the
	// reactivation rule: a group returns by posting an action.
	return s.db.WithContext(ctx).Model(&models.Group{}).
		Where("id = ? AND status = ?", action.GroupID, models.StatusAccepted).
		Updates(map[string]any{
			"visible":          true,
			"last_activity_at": now,
			"updated_at":       now,
		}).Error
}

// RejectAction refuses an announcement and deletes it.
//
// The same rule as a refused doléance and a refused group: content a curator
// judged did not belong is discarded rather than archived. The audit log keeps
// the decision.
func (s *Store) RejectAction(ctx context.Context, id string) error {
	result := s.db.WithContext(ctx).Delete(&models.Action{}, "id = ?", id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrActionNotFound
	}
	return nil
}
