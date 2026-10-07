package store

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/CoderSyndicate/doleances/internal/geo"
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/subjects"
)

// ErrGroupNotFound is returned when no group matches.
var ErrGroupNotFound = errors.New("group not found")

// ErrNameTaken means another group already answers to that name.
var ErrNameTaken = errors.New("that name is already taken")

// GroupNameAvailable reports whether a name can still be claimed.
//
// Checked against live groups and against the edits waiting on a decision: a
// name a published group has asked to take is not free either, and telling
// somebody it was — then refusing them at the write — is the race this live
// check exists to avoid.
//
// Tolerant: "Collectif Citoyen" does not walk past "collectif citoyen".
func (s *Store) GroupNameAvailable(ctx context.Context, name string) (bool, error) {
	key := subjects.MatchKey(name)
	if key == "" {
		return false, nil
	}

	var groups int64
	err := s.db.WithContext(ctx).Model(&models.Group{}).
		Where("name_key = ?", key).Count(&groups).Error
	if err != nil {
		return false, err
	}
	if groups > 0 {
		return false, nil
	}

	var revisions int64
	err = s.db.WithContext(ctx).Model(&models.GroupRevision{}).
		Where("name_key = ?", key).Count(&revisions).Error
	return revisions == 0, err
}

// CreateGroup records a proposed group and makes its author the first admin.
//
// # One transaction, and why it matters
//
// A group with no admin is a door nobody can open: nobody can post an action,
// nobody can accept a change, and nothing can bring it back from hiding. The
// succession rules in *Group roles* go as far as deleting such a group
// outright rather than leaving it on the map. So the membership is not a
// follow-up write that might fail — the group and its first admin are one fact
// or neither exists.
//
// # There is no creator role
//
// Whoever proposes a group is simply its first **admin**, with nothing
// permanent about them. That is what lets a group outlive the person who
// started it, which is the whole point of the roles table.
//
// The name is re-checked inside the write rather than trusted from the form's
// live check: between the keystroke and the submission somebody else may have
// taken it, and the unique index would otherwise turn that race into a bare
// constraint error.
func (s *Store) CreateGroup(ctx context.Context, group *models.Group, adminAccountID string) error {
	group.NameKey = subjects.MatchKey(group.Name)
	if group.NameKey == "" {
		return ErrNameTaken
	}

	// Pending and invisible. A group that appeared on the map the moment
	// somebody pressed the button would make the assessment decorative.
	group.Status = models.StatusPending
	group.Visible = false
	group.LastActivityAt = time.Now()

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var taken int64
		err := tx.Model(&models.Group{}).
			Where("name_key = ?", group.NameKey).Count(&taken).Error
		if err != nil {
			return err
		}
		if taken > 0 {
			return ErrNameTaken
		}

		if err := tx.Create(group).Error; err != nil {
			return err
		}

		return tx.Create(&models.GroupMembership{
			GroupID:   group.ID,
			AccountID: adminAccountID,
			Role:      models.RoleAdmin,
			JoinedAt:  time.Now(),
		}).Error
	})
}

// ---------------------------------------------------------------------------
// The assessment sweep, for groups
// ---------------------------------------------------------------------------

// ClaimGroupForAssessment takes one pending group for a worker to assess.
//
// Claimed the same way a doléance is: the row leaves `pending` in the same
// statement that selects it, so two backends cannot score the same group twice
// and have the second verdict quietly overwrite the first. Anything left in
// `assessing` by a crash is returned by ReleaseStaleGroupAssessments.
func (s *Store) ClaimGroupForAssessment(ctx context.Context) (models.Group, error) {
	var claimed models.Group

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidate models.Group
		err := tx.Where("status = ?", models.StatusPending).
			Order("created_at asc"). // oldest first: nobody waits indefinitely
			First(&candidate).Error
		if err != nil {
			return err
		}

		result := tx.Model(&models.Group{}).
			Where("id = ? AND status = ?", candidate.ID, models.StatusPending).
			Updates(map[string]any{
				"status":      models.StatusAssessing,
				"assessed_at": time.Now(), // the stale-claim clock
				"updated_at":  time.Now(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound // somebody else took it
		}

		claimed = candidate
		claimed.Status = models.StatusAssessing
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.Group{}, ErrGroupNotFound
	}
	return claimed, err
}

// RecordGroupAssessment stores a model's verdict and the status it implies.
//
// Acceptance is what puts a group on the map, so Visible is set here and
// nowhere else in this path: a group is on the map because something decided
// it should be, never as a side effect of being created.
func (s *Store) RecordGroupAssessment(ctx context.Context, id string, verdict Verdict) error {
	now := time.Now()
	fields := map[string]any{
		"status":            verdict.Status,
		"confidence":        verdict.Confidence,
		"assessed_at":       now,
		"assessed_by":       verdict.Model,
		"assessment_reason": verdict.Reason,
		"updated_at":        now,
	}
	if verdict.Status == models.StatusAccepted {
		fields["visible"] = true
		fields["last_activity_at"] = now
	}

	return s.db.WithContext(ctx).Model(&models.Group{}).
		Where("id = ?", id).Updates(fields).Error
}

// ReturnGroupForRetry puts a claimed group back after a failure, counting the
// attempt so a model that cannot answer does not cycle it for ever.
func (s *Store) ReturnGroupForRetry(ctx context.Context, id string) (attempts int, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var group models.Group
		if err := tx.First(&group, "id = ?", id).Error; err != nil {
			return err
		}
		attempts = group.AssessmentAttempts + 1

		return tx.Model(&models.Group{}).Where("id = ?", id).
			Updates(map[string]any{
				"status":              models.StatusPending,
				"assessment_attempts": attempts,
				"updated_at":          time.Now(),
			}).Error
	})
	return attempts, err
}

// SendGroupToCuration hands a group to a human, whatever the model did or
// failed to do.
func (s *Store) SendGroupToCuration(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Model(&models.Group{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":     models.StatusCurating,
			"updated_at": time.Now(),
		}).Error
}

// ReleaseStaleGroupAssessments returns groups stuck mid-assessment.
//
// A backend killed between claiming and recording leaves a row in `assessing`,
// which no sweep revisits and no curator sees — the group would be invisible
// to everybody, which for a group means a meeting nobody can find.
func (s *Store) ReleaseStaleGroupAssessments(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)

	result := s.db.WithContext(ctx).Model(&models.Group{}).
		Where("status = ? AND assessed_at < ?", models.StatusAssessing, cutoff).
		Updates(map[string]any{
			"status":     models.StatusPending,
			"updated_at": time.Now(),
		})
	return result.RowsAffected, result.Error
}

// ListGroupCurationQueue returns the groups awaiting a human decision.
//
// Like the message queue, this includes what no classifier has ruled on. If no
// model is configured or reachable, every group arrives here rather than
// nowhere: humans are the last line, and a map that stays empty because a
// machine is down has failed the people waiting to be found.
func (s *Store) ListGroupCurationQueue(ctx context.Context, limit int) ([]models.Group, error) {
	if limit <= 0 {
		limit = 100
	}

	var groups []models.Group
	err := s.db.WithContext(ctx).
		Where("status IN ?", []models.ReviewStatus{models.StatusCurating, models.StatusPending}).
		Order("created_at asc").
		Limit(limit).
		Find(&groups).Error
	return groups, err
}

// ---------------------------------------------------------------------------
// Reading the map, and the curator's decisions
// ---------------------------------------------------------------------------

// GroupQuery narrows the public listing.
type GroupQuery struct {
	// Bounds limits results to a map viewport. Nil means everywhere.
	Bounds *geo.Box

	Limit int
}

// FindGroups returns the groups on the map, newest first, along with how many
// there are in the world.
//
// # Why the total comes back even when a viewport does not show them
//
// The number of groups and their spread across the map is itself the argument
// — the same one the register rests on. One grievance is a misfortune; ten
// thousand written down side by side are proof that the trouble is shared and
// that the people carrying it are the majority. A reader who sees four pins in
// their département and does not learn there are nine hundred elsewhere has
// been told the opposite of the truth.
//
// So the viewport narrows what is drawn and never what is counted, and no
// filter of any kind narrows the count. Groups are not filtered by language
// either, here or anywhere: somebody reading in German has every reason to
// want to know that people are meeting in Guéret, and plenty of people read
// more than one language. The Group model carries no language field at all,
// which is what makes that structural rather than a promise.
//
// Only visible groups, which means only what a curator or the classifier
// accepted: a group reaches this listing because something decided it should,
// never because it exists.
//
// Unlike the register, a group with no coordinates is simply absent. A
// doléance that named only a country is still somebody's words and the page
// says how many it is not showing; a meeting place with no place is not a
// meeting place.
func (s *Store) FindGroups(ctx context.Context, query GroupQuery) ([]models.Group, int64, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 200
	}

	// Counted before anything narrows it, and never narrowed afterwards.
	var total int64
	err := s.db.WithContext(ctx).Model(&models.Group{}).
		Where("status = ? AND visible = ? AND location_geohash <> ?",
			models.StatusAccepted, true, "").
		Count(&total).Error
	if err != nil {
		return nil, 0, err
	}

	db := s.db.WithContext(ctx).Model(&models.Group{}).
		Where("status = ? AND visible = ?", models.StatusAccepted, true)

	bounds := query.Bounds
	if bounds != nil {
		cells := geo.Cover(*bounds)
		// An empty cover is the whole world, and narrowing to everywhere is
		// not narrowing. The same reasoning as the register's viewport.
		if len(cells) == 0 {
			bounds = nil
		} else {
			prefixes := s.db.Session(&gorm.Session{NewDB: true})
			for i, cell := range cells {
				if i == 0 {
					prefixes = prefixes.Where("location_geohash LIKE ?", cell+"%")
					continue
				}
				prefixes = prefixes.Or("location_geohash LIKE ?", cell+"%")
			}
			db = db.Where(prefixes)
		}
	}

	// A group with no pin cannot be on a map, whatever the viewport.
	db = db.Where("location_geohash <> ?", "")

	// Cells always cover at least the viewport, so they overhang its edges.
	// Read past the limit and trim in Go — without this a group in Guéret
	// answers a viewport over Germany, because both sit under the same coarse
	// geohash prefix. The register does the same for the same reason.
	var candidates []models.Group
	err = db.Order("created_at desc").
		Limit(limit * 2).
		Find(&candidates).Error
	if err != nil {
		return nil, 0, err
	}

	groups := make([]models.Group, 0, len(candidates))
	for _, group := range candidates {
		if bounds != nil && !bounds.Contains(group.Location.Latitude, group.Location.Longitude) {
			log.Trace().Str("group", group.ID).
				Float64("lat", group.Location.Latitude).
				Float64("lng", group.Location.Longitude).
				Str("geohash", group.Location.Geohash).
				Msg("groups: outside the viewport, trimmed")
			continue
		}
		groups = append(groups, group)
		if len(groups) == limit {
			break
		}
	}
	return groups, total, nil
}

// AllGroupsPage is one page of the console's group listing.
type AllGroupsPage struct {
	Groups []models.Group

	// Total is every group there is, not the length of this page: the console
	// has to be able to say "12 of 340" rather than leave somebody counting
	// pages to find out how many groups the register has.
	Total int64
}

// ListAllGroups returns every group, page by page, newest first.
//
// Every group at every status — accepted, waiting, refused — because this is
// the administrative listing rather than the map. A console that could only
// show what is already public would be useless for the one question it is
// most often asked: what happened to the group somebody proposed.
//
// Newest first, because a list ordered by arrival is scanned from the end by
// anybody looking for something recent, and that is nearly everybody.
func (s *Store) ListAllGroups(ctx context.Context, offset, limit int) (AllGroupsPage, error) {
	if limit <= 0 || limit > 200 {
		limit = 25
	}
	if offset < 0 {
		offset = 0
	}

	var page AllGroupsPage
	if err := s.db.WithContext(ctx).Model(&models.Group{}).Count(&page.Total).Error; err != nil {
		return page, err
	}

	err := s.db.WithContext(ctx).
		Order("created_at desc").
		Offset(offset).
		Limit(limit).
		Find(&page.Groups).Error
	return page, err
}

// GetGroup returns one group by its identifier.
//
// Any status: a group page resolves even when the group is not on the map,
// because leaving the map is not disappearing — somebody holding the link
// reaches a page that says plainly where the group stands, and that page is
// how a hidden group comes back.
func (s *Store) GetGroup(ctx context.Context, id string) (models.Group, error) {
	var group models.Group
	err := s.db.WithContext(ctx).
		First(&group, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return group, ErrGroupNotFound
	}
	return group, err
}

// AcceptGroup publishes a group, by a curator's decision.
func (s *Store) AcceptGroup(ctx context.Context, id string) error {
	now := time.Now()
	result := s.db.WithContext(ctx).Model(&models.Group{}).
		Where("id = ? AND status <> ?", id, models.StatusAccepted).
		Updates(map[string]any{
			"status":           models.StatusAccepted,
			"visible":          true,
			"last_activity_at": now,
			"updated_at":       now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrGroupNotFound
	}
	return nil
}

// RejectGroup refuses a group and deletes it.
//
// Deletion rather than a hidden row, for the same reason a rejected doléance
// is deleted: there is no case for spending storage, backup and attention on
// something a curator judged did not belong. The audit log keeps the decision
// and the curator's name; it never keeps the content.
//
// It matters more here than for a message. A group row carries a contact
// address — somebody's name and email — so a refused group kept "just in case"
// is personal data retained for a group that does not exist. Deleting also
// frees the name, which somebody else may legitimately want.
func (s *Store) RejectGroup(ctx context.Context, id string) error {
	result := s.db.WithContext(ctx).Delete(&models.Group{}, "id = ?", id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrGroupNotFound
	}
	return nil
}

// DeleteGroup removes a group and everything hanging off it.
//
// The one place inactivity *does* delete rather than hide, and only in one
// shape: a group with no admin left. A leaderless group cannot be reactivated
// by anybody, so leaving it dormant on the map would be advertising a door
// nobody can open — which is worse than an empty map, because it tells people
// there is something to turn up to.
//
// Everything else about a group is hidden and kept. See models.Group.Visible.
func (s *Store) DeleteGroup(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The messages people wrote to it go with it. They were private text
		// addressed to admins who no longer exist, and there is nobody left
		// who may read them.
		for _, model := range []any{
			&models.GroupMessage{}, &models.GroupMembership{}, &models.GroupRevision{},
		} {
			if err := tx.Delete(model, "group_id = ?", id).Error; err != nil {
				return err
			}
		}

		// And the notifications about it. Each one is a line offering a door
		// into a group that no longer exists; leaving them would give every
		// former member a list of links that answer 404.
		err := tx.Delete(&models.Notification{},
			"subject_type = ? AND subject_id = ?", "group", id).Error
		if err != nil {
			return err
		}

		result := tx.Delete(&models.Group{}, "id = ?", id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrGroupNotFound
		}
		return nil
	})
}

// ListLeaderlessGroups finds the groups nobody can run.
//
// A group whose last admin left is offered to its hosts, then to its members,
// and deleted if nobody takes it on within the window. The offer is made when
// somebody leaves; this is what enforces the deadline afterwards, because a
// deadline nothing sweeps is a deadline that never arrives.
//
// `NOT EXISTS` over a join, so a group with no memberships at all is found too:
// that is the case a join would silently skip, and it is the one that most
// needs deleting.
func (s *Store) ListLeaderlessGroups(ctx context.Context, since time.Duration) ([]models.Group, error) {
	admins := s.db.Session(&gorm.Session{NewDB: true}).
		Model(&models.GroupMembership{}).
		Select("1").
		Where("group_memberships.group_id = groups.id AND group_memberships.role = ?", models.RoleAdmin)

	var groups []models.Group
	err := s.db.WithContext(ctx).
		Where("NOT EXISTS (?)", admins).
		Where("updated_at < ?", time.Now().Add(-since)).
		Find(&groups).Error
	return groups, err
}
