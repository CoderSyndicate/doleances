package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/subjects"
)

// ErrRevisionNotFound means no edit is waiting for that group.
var ErrRevisionNotFound = errors.New("no pending revision")

// GroupEdit is the published face of a group: what a reader sees, and
// therefore what a curator accepted.
//
// Who may change it is deliberately not in here. A group's admins are held in
// the membership table and changed there, so handing over a group is never an
// edit that a curator has to approve — a group that could not change who runs
// it would be exactly the group that depends on one irreplaceable person.
type GroupEdit struct {
	Name        string
	Description string

	// Location is the meeting place. A zero value means "leave it alone" —
	// an edit form that posted no map should not silently unpin a group.
	Location *models.Location
}

// EditGroup records a change to a group, by whichever of the two routes its
// status calls for.
//
// A **pending** group is edited in place and re-enters assessment: there is no
// published version to protect, and the alternative would be a revision queued
// behind an original nobody has ruled on yet.
//
// An **accepted** group gets a revision row instead, and stays on the map
// untouched until somebody accepts the change. Applied is false in that case,
// and the caller is expected to say so rather than implying the edit is live.
func (s *Store) EditGroup(ctx context.Context, id string, edit GroupEdit) (group models.Group, applied bool, err error) {
	key := subjects.MatchKey(edit.Name)
	if key == "" {
		return group, false, ErrNameTaken
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&group, "id = ?", id).Error; err != nil {
			return err
		}

		// A renamed group clears the same bar a new one does, against every
		// other group and every other pending revision — but never against
		// itself, or nobody could ever fix a description without renaming.
		if err := nameIsFree(tx, key, id); err != nil {
			return err
		}

		if group.Status == models.StatusAccepted {
			applied = false
			return upsertRevision(tx, group, edit, key)
		}

		applied = true
		return editInPlace(tx, &group, edit, key)
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return group, false, ErrGroupNotFound
	}
	return group, applied, err
}

// nameIsFree checks a proposed name against the live groups and the pending
// revisions at once, ignoring the group doing the asking.
func nameIsFree(tx *gorm.DB, key, exceptGroupID string) error {
	var groups int64
	err := tx.Model(&models.Group{}).
		Where("name_key = ? AND id <> ?", key, exceptGroupID).Count(&groups).Error
	if err != nil {
		return err
	}
	if groups > 0 {
		return ErrNameTaken
	}

	var revisions int64
	err = tx.Model(&models.GroupRevision{}).
		Where("name_key = ? AND group_id <> ?", key, exceptGroupID).Count(&revisions).Error
	if err != nil {
		return err
	}
	if revisions > 0 {
		return ErrNameTaken
	}
	return nil
}

func editInPlace(tx *gorm.DB, group *models.Group, edit GroupEdit, key string) error {
	now := time.Now()
	fields := map[string]any{
		"name":        edit.Name,
		"name_key":    key,
		"description": edit.Description,
		"updated_at":  now,

		// Back to the door it came in by, with everything the last assessment
		// concluded cleared. Leaving a stale score beside new text is how a
		// console shows a verdict that was never reached.
		"status":            models.StatusPending,
		"confidence":        0,
		"assessed_at":       nil,
		"assessed_by":       "",
		"assessment_reason": "",
	}
	if edit.Location != nil {
		location := *edit.Location
		location.Refresh()
		fields["location_latitude"] = location.Latitude
		fields["location_longitude"] = location.Longitude
		fields["location_geohash"] = location.Geohash
		fields["location_label"] = location.Label
		fields["location_country_code"] = location.CountryCode
		group.Location = location
	}

	if err := tx.Model(&models.Group{}).Where("id = ?", group.ID).Updates(fields).Error; err != nil {
		return err
	}

	group.Name, group.NameKey, group.Description = edit.Name, key, edit.Description
	group.Status, group.Confidence = models.StatusPending, 0
	group.AssessedAt, group.AssessedBy, group.AssessmentReason = nil, "", ""
	return nil
}

// upsertRevision writes the pending edit, overwriting any earlier one.
//
// One revision per group: a second edit while the first still waits replaces
// it, because the author's latest intent is the one worth a curator's time.
func upsertRevision(tx *gorm.DB, group models.Group, edit GroupEdit, key string) error {
	location := group.Location
	if edit.Location != nil {
		location = *edit.Location
	}
	location.Refresh()

	revision := models.GroupRevision{
		GroupID:     group.ID,
		Name:        edit.Name,
		NameKey:     key,
		Description: edit.Description,
		Location:    location,
		Status:      models.StatusPending,
	}

	var existing models.GroupRevision
	err := tx.Where("group_id = ?", group.ID).First(&existing).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return tx.Create(&revision).Error
	case err != nil:
		return err
	}

	return tx.Model(&models.GroupRevision{}).Where("id = ?", existing.ID).
		Updates(map[string]any{
			"name":        revision.Name,
			"name_key":    revision.NameKey,
			"description": revision.Description,

			"location_latitude":     location.Latitude,
			"location_longitude":    location.Longitude,
			"location_geohash":      location.Geohash,
			"location_label":        location.Label,
			"location_country_code": location.CountryCode,

			// A replaced revision is a new proposal, so it is assessed again
			// rather than inheriting a verdict reached about different words.
			"status":              models.StatusPending,
			"confidence":          0,
			"assessed_at":         nil,
			"assessed_by":         "",
			"assessment_attempts": 0,
			"assessment_reason":   "",
			"updated_at":          time.Now(),
		}).Error
}

// FindGroupRevision returns the edit waiting on a group, if there is one.
func (s *Store) FindGroupRevision(ctx context.Context, groupID string) (models.GroupRevision, error) {
	var revision models.GroupRevision
	err := s.db.WithContext(ctx).Where("group_id = ?", groupID).First(&revision).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return revision, ErrRevisionNotFound
	}
	return revision, err
}

// ---------------------------------------------------------------------------
// The assessment sweep, for revisions
// ---------------------------------------------------------------------------

// ClaimGroupRevisionForAssessment takes one pending revision to assess, on the
// same exclusive terms as a group or a doléance.
func (s *Store) ClaimGroupRevisionForAssessment(ctx context.Context) (models.GroupRevision, error) {
	var claimed models.GroupRevision

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidate models.GroupRevision
		err := tx.Where("status = ?", models.StatusPending).
			Order("created_at asc").
			First(&candidate).Error
		if err != nil {
			return err
		}

		result := tx.Model(&models.GroupRevision{}).
			Where("id = ? AND status = ?", candidate.ID, models.StatusPending).
			Updates(map[string]any{
				"status":      models.StatusAssessing,
				"assessed_at": time.Now(),
				"updated_at":  time.Now(),
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
		return models.GroupRevision{}, ErrRevisionNotFound
	}
	return claimed, err
}

// RecordGroupRevisionAssessment stores a verdict about a pending edit.
//
// An accepted revision is **not applied here**. Applying is a separate act
// with its own failure — the name may have gone in the meantime — and folding
// it into the sweep would mean a model's verdict silently failing to take
// effect with nothing to show for it.
func (s *Store) RecordGroupRevisionAssessment(ctx context.Context, id string, verdict Verdict) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&models.GroupRevision{}).
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

// ReturnGroupRevisionForRetry puts a claimed revision back, counting the try.
func (s *Store) ReturnGroupRevisionForRetry(ctx context.Context, id string) (attempts int, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var revision models.GroupRevision
		if err := tx.Where("id = ?", id).First(&revision).Error; err != nil {
			return err
		}
		attempts = revision.AssessmentAttempts + 1

		return tx.Model(&models.GroupRevision{}).Where("id = ?", id).
			Updates(map[string]any{
				"status":              models.StatusPending,
				"assessment_attempts": attempts,
				"updated_at":          time.Now(),
			}).Error
	})
	return attempts, err
}

// SendGroupRevisionToCuration hands an edit to a human.
func (s *Store) SendGroupRevisionToCuration(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Model(&models.GroupRevision{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":     models.StatusCurating,
			"updated_at": time.Now(),
		}).Error
}

// ReleaseStaleGroupRevisionAssessments rescues edits stranded mid-claim.
func (s *Store) ReleaseStaleGroupRevisionAssessments(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)

	result := s.db.WithContext(ctx).Model(&models.GroupRevision{}).
		Where("status = ? AND updated_at < ?", models.StatusAssessing, cutoff).
		Updates(map[string]any{
			"status":     models.StatusPending,
			"updated_at": time.Now(),
		})
	return result.RowsAffected, result.Error
}

// ListGroupRevisionQueue returns the edits awaiting a decision, each with the
// group it proposes to change.
//
// The published group travels with the revision because that is the whole
// question a curator is answering: not "is this a good group?" but "is this a
// good change to that group?", and the second is unanswerable without the
// first on the same screen.
func (s *Store) ListGroupRevisionQueue(ctx context.Context, limit int) ([]models.GroupRevision, map[string]models.Group, error) {
	if limit <= 0 {
		limit = 100
	}

	var revisions []models.GroupRevision
	err := s.db.WithContext(ctx).
		Where("status IN ?", []models.ReviewStatus{models.StatusCurating, models.StatusPending}).
		Order("created_at asc").
		Limit(limit).
		Find(&revisions).Error
	if err != nil {
		return nil, nil, err
	}
	if len(revisions) == 0 {
		return revisions, map[string]models.Group{}, nil
	}

	ids := make([]string, 0, len(revisions))
	for _, revision := range revisions {
		ids = append(ids, revision.GroupID)
	}

	var groups []models.Group
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&groups).Error; err != nil {
		return nil, nil, err
	}

	before := make(map[string]models.Group, len(groups))
	for _, group := range groups {
		before[group.ID] = group
	}
	return revisions, before, nil
}

// AcceptGroupRevision applies a pending edit to its group and clears the row.
//
// The name is re-checked here and not only when the edit was proposed: a name
// free in the morning may belong to somebody else by the time a curator says
// yes, and the unique index would otherwise turn that into a failed write
// after the decision had already been recorded.
func (s *Store) AcceptGroupRevision(ctx context.Context, groupID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var revision models.GroupRevision
		if err := tx.Where("group_id = ?", groupID).First(&revision).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRevisionNotFound
			}
			return err
		}

		if err := nameIsFree(tx, revision.NameKey, groupID); err != nil {
			return err
		}

		location := revision.Location
		location.Refresh()

		now := time.Now()
		err := tx.Model(&models.Group{}).Where("id = ?", groupID).
			Updates(map[string]any{
				"name":        revision.Name,
				"name_key":    revision.NameKey,
				"description": revision.Description,

				"location_latitude":     location.Latitude,
				"location_longitude":    location.Longitude,
				"location_geohash":      location.Geohash,
				"location_label":        location.Label,
				"location_country_code": location.CountryCode,

				// The group keeps its accepted status — it never left the map
				// — but takes on what was concluded about the new words, or a
				// console would show yesterday's verdict beside today's text.
				"confidence":        revision.Confidence,
				"assessed_at":       revision.AssessedAt,
				"assessed_by":       revision.AssessedBy,
				"assessment_reason": revision.AssessmentReason,
				"updated_at":        now,
			}).Error
		if err != nil {
			return err
		}

		return tx.Delete(&models.GroupRevision{}, "id = ?", revision.ID).Error
	})
}

// RejectGroupRevision discards an edit. The published group is untouched,
// which is the point of the whole arrangement.
func (s *Store) RejectGroupRevision(ctx context.Context, groupID string) error {
	result := s.db.WithContext(ctx).Delete(&models.GroupRevision{}, "group_id = ?", groupID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrRevisionNotFound
	}
	return nil
}
