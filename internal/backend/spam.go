package backend

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/CoderSyndicate/doleances/internal/cache"
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/store"
	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"
)

// purgeInterval is how often expired spam is swept. The window is measured in
// hours, so sweeping every few minutes is punctual enough and costs nothing.
const purgeInterval = 5 * time.Minute

func (a *API) registerSpamRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-spam",
		Method:      http.MethodGet,
		Path:        "/v1/spam",
		Summary:     "Sample the dropped submissions",
		Description: "What the classifier refused without a human, still inside the retention window. " +
			"A sample to check the filter by, not a work queue.",
		Tags: []string{"Curation"},
	}, a.listSpam)

	huma.Register(api, invalidates(cache.Messages)(huma.Operation{
		OperationID: "rescue-spam",
		Method:      http.MethodPost,
		Path:        "/v1/spam/{id}/rescue",
		Summary:     "Send a wrongly dropped submission to the curation queue",
		Description: "It does not publish: being wrongly called spam is not the same as being ready for the register.",
		Tags:        []string{"Curation"},
	}), a.rescueSpam)

	huma.Register(api, invalidates(cache.Messages)(huma.Operation{
		OperationID: "delete-spam",
		Method:      http.MethodDelete,
		Path:        "/v1/spam/{id}",
		Summary:     "Delete a dropped submission now",
		Tags:        []string{"Curation"},
	}), a.deleteSpam)

	huma.Register(api, huma.Operation{
		OperationID: "get-curation-settings",
		Method:      http.MethodGet,
		Path:        "/v1/curation",
		Summary:     "Read the curation policies",
		Tags:        []string{"Curation"},
	}, a.getCurationSettings)

	huma.Register(api, huma.Operation{
		OperationID: "save-curation-settings",
		Method:      http.MethodPut,
		Path:        "/v1/curation",
		Summary:     "Update the curation policies",
		Tags:        []string{"Curation"},
	}, a.saveCurationSettings)
}

// SpamItem is one dropped submission as the console shows it.
type SpamItem struct {
	ID         string    `json:"id"`
	Text       string    `json:"text"`
	Language   string    `json:"language,omitempty"`
	Confidence int       `json:"confidence"`
	DroppedAt  time.Time `json:"dropped_at"`
	ExpiresAt  time.Time `json:"expires_at"`

	// Refusal names the ground this was refused on — a guard that ran before
	// any classifier ("duplicate", "payload") or one the model reported
	// ("threat", "contact", "identifies"). Empty when the score decided alone.
	Refusal string `json:"refusal,omitempty"`

	// Reason is the model's own sentence, and it is the point of this page.
	// A score of 30 tells a curator nothing about whether the filter is eating
	// real doléances; "reads as advertising for a named company" does.
	Reason string `json:"reason,omitempty"`

	// DuplicateOf names the earlier doléance this one repeats, when that is
	// why it is here rather than a score.
	//
	// The distinction matters to whoever reads this page: a low score is the
	// classifier's opinion about the text, and a duplicate is an arithmetic
	// fact about it. They are read differently and rescued for different
	// reasons — and a page filling up with duplicates of one paragraph is a
	// flood in progress, which is worth noticing while it is happening.
	DuplicateOf string `json:"duplicate_of,omitempty"`

	// Pleas is how many readers said this refusal was wrong, from the public
	// page. Anything above zero sorts to the top of this listing, which is
	// the whole of what a plea does: somebody asked for a person to look, so
	// a person sees it before the day's drops bury it.
	//
	// It decides nothing else. A refusal overturned by a count would be a
	// register where the largest group rules on what may be written.
	Pleas int `json:"pleas"`
}

// SpamOutput is the sample plus the context needed to read it.
type SpamOutput struct {
	Body struct {
		Items []SpamItem `json:"items"`
		// Held is how many are being kept, which may exceed what is listed.
		Held           int64 `json:"held"`
		RetentionHours int   `json:"retention_hours"`

		// Page and PerPage, so the console can walk a sample bigger than one
		// screen rather than showing the first page and calling it the whole.
		Page    int `json:"page"`
		PerPage int `json:"per_page"`
	}
}

// SpamInput selects a page of the sample.
type SpamInput struct {
	Page    int `query:"page" minimum:"1" default:"1"`
	PerPage int `query:"per_page" minimum:"1" maximum:"200" default:"25"`
}

func (a *API) listSpam(ctx context.Context, in *SpamInput) (*SpamOutput, error) {
	page, perPage := in.Page, in.PerPage
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 25
	}

	settings, err := a.store.CurationSettings(ctx)
	if err != nil {
		log.Error().Err(err).Msg("cannot read curation settings")
		return nil, huma.Error500InternalServerError("cannot read the curation settings")
	}
	retention := time.Duration(settings.SpamRetentionHours) * time.Hour

	messages, err := a.store.ListSpam(ctx, retention, perPage, (page-1)*perPage)
	if err != nil {
		log.Error().Err(err).Msg("cannot list dropped messages")
		return nil, huma.Error500InternalServerError("cannot read the dropped submissions")
	}
	held, err := a.store.CountSpam(ctx, retention)
	if err != nil {
		log.Error().Err(err).Msg("cannot count dropped messages")
		return nil, huma.Error500InternalServerError("cannot read the dropped submissions")
	}

	out := &SpamOutput{}
	out.Body.Held = held
	out.Body.RetentionHours = settings.SpamRetentionHours
	out.Body.Page, out.Body.PerPage = page, perPage
	for _, message := range messages {
		out.Body.Items = append(out.Body.Items, SpamItem{
			ID:          message.ID,
			Refusal:     message.DropReason,
			Reason:      message.AssessmentReason,
			DuplicateOf: message.DuplicateOf,
			Pleas:       message.Pleas,
			Text:        message.Text,
			Language:    message.Language,
			Confidence:  message.Confidence,
			DroppedAt:   message.UpdatedAt,
			ExpiresAt:   message.UpdatedAt.Add(retention),
		})
	}
	return out, nil
}

// SpamIDInput addresses one dropped submission.
type SpamIDInput struct {
	Actor string `header:"X-Actor"`
	ID    string `path:"id"`
}

func (a *API) rescueSpam(ctx context.Context, in *SpamIDInput) (*EmptyOutput, error) {
	err := a.store.RescueSpam(ctx, in.ID)
	if errors.Is(err, store.ErrMessageNotFound) {
		return nil, huma.Error404NotFound("no dropped submission with that id — it may have been purged")
	}
	if err != nil {
		log.Error().Err(err).Str("message", in.ID).Msg("cannot rescue message")
		return nil, huma.Error500InternalServerError("cannot rescue the submission")
	}

	// Overruling the classifier is a curation decision like any other.
	a.recordDecision(ctx, in.Actor, models.AuditAccept, "message", in.ID,
		"rescued from the dropped submissions, sent to the curation queue")
	log.Info().Str("message", in.ID).Msg("dropped message rescued")
	return &EmptyOutput{}, nil
}

func (a *API) deleteSpam(ctx context.Context, in *SpamIDInput) (*EmptyOutput, error) {
	err := a.store.DeleteSpam(ctx, in.ID)
	if errors.Is(err, store.ErrMessageNotFound) {
		return nil, huma.Error404NotFound("no dropped submission with that id")
	}
	if err != nil {
		log.Error().Err(err).Str("message", in.ID).Msg("cannot delete message")
		return nil, huma.Error500InternalServerError("cannot delete the submission")
	}

	a.recordDecision(ctx, in.Actor, models.AuditReject, "message", in.ID, "confirmed as spam and deleted")
	return &EmptyOutput{}, nil
}

// CurationSettingsOutput returns the policies.
type CurationSettingsOutput struct {
	Body struct {
		SpamRetentionHours int `json:"spam_retention_hours"`
	}
}

func (a *API) getCurationSettings(ctx context.Context, _ *struct{}) (*CurationSettingsOutput, error) {
	settings, err := a.store.CurationSettings(ctx)
	if err != nil {
		log.Error().Err(err).Msg("cannot read curation settings")
		return nil, huma.Error500InternalServerError("cannot read the curation settings")
	}

	out := &CurationSettingsOutput{}
	out.Body.SpamRetentionHours = settings.SpamRetentionHours
	return out, nil
}

// SaveCurationSettingsInput carries the edited policies.
type SaveCurationSettingsInput struct {
	Actor string `header:"X-Actor"`
	Body  struct {
		SpamRetentionHours int `json:"spam_retention_hours"`
	}
}

func (a *API) saveCurationSettings(ctx context.Context, in *SaveCurationSettingsInput) (*EmptyOutput, error) {
	hours := in.Body.SpamRetentionHours
	// A week is already generous for content judged not to belong here, and an
	// unbounded window would make the spam bin the largest thing in the
	// database.
	if hours < 1 || hours > 168 {
		return nil, huma.Error422UnprocessableEntity("retention must be between 1 and 168 hours")
	}

	err := a.store.SaveCurationSettings(ctx, models.CurationSettings{SpamRetentionHours: hours})
	if err != nil {
		log.Error().Err(err).Msg("cannot save curation settings")
		return nil, huma.Error500InternalServerError("cannot save the curation settings")
	}

	a.recordConfigChange(ctx, in.Actor, models.CurationSettingsID, "spam retention changed")
	log.Info().Int("hours", hours).Msg("spam retention changed")
	return &EmptyOutput{}, nil
}

// recordDecision appends a curation decision to the audit log.
func (a *API) recordDecision(ctx context.Context, actor string, action models.AuditAction, subjectType, subjectID, reason string) {
	if actor == "" {
		actor = "unknown"
	}
	entry := models.AuditEntry{
		ID:          models.NewID(),
		CreatedAt:   time.Now(),
		Actor:       actor,
		Action:      action,
		SubjectType: subjectType,
		SubjectID:   subjectID,
		Reason:      reason,
	}
	if err := a.store.DB().WithContext(ctx).Create(&entry).Error; err != nil {
		log.Error().Err(err).Msg("cannot write audit entry")
	}
}

// purgeSpamPeriodically deletes dropped submissions past their window until
// the context is cancelled.
//
// Retention that nothing enforces is not retention, it is an archive nobody
// admitted to keeping.
func (a *API) purgeSpamPeriodically(ctx context.Context) {
	ticker := time.NewTicker(purgeInterval)
	defer ticker.Stop()

	// Once before the first tick, not only after it.
	//
	// Everything on this loop is housekeeping that should already be true:
	// dropped messages past their window, actions whose date has gone, codes
	// nobody entered. Waiting five minutes after every start would mean a
	// restart is five minutes of a page showing last night's meeting — and,
	// the first time a new column appears, five minutes of a register that
	// has not derived it yet.
	a.sweepOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Debug().Msg("spam purge stopped")
			return
		case <-ticker.C:
			a.sweepOnce(ctx)
		}
	}
}

func (a *API) purgeSpamOnce(ctx context.Context) {
	settings, err := a.store.CurationSettings(ctx)
	if err != nil {
		log.Error().Err(err).Msg("cannot read curation settings, skipping purge")
		return
	}

	purged, err := a.store.PurgeSpam(ctx, time.Duration(settings.SpamRetentionHours)*time.Hour)
	if err != nil {
		log.Error().Err(err).Msg("cannot purge dropped messages")
		return
	}
	if purged > 0 {
		log.Info().Int64("purged", purged).Int("retention_hours", settings.SpamRetentionHours).
			Msg("dropped submissions purged")
	}
}

// sweepOnce is one pass of the housekeeping.
//
// Named rather than inlined so that the startup pass and the ticking one are
// provably the same work: two lists that drifted apart would mean a restart
// doing something subtly different from a tick.
func (a *API) sweepOnce(ctx context.Context) {
	a.purgeSpamOnce(ctx)
	a.retireActionsOnce(ctx)
	a.deleteLeaderlessGroupsOnce(ctx)
	a.purgeCredentialLeftoversOnce(ctx)
}

// successionWindow is how long a group stands with nobody able to run it.
//
// A week, which is a starting value and therefore configuration-shaped rather
// than a law: long enough for somebody who was offered the group to come back
// from a holiday, short enough that the map is not carrying a door nobody can
// open for a month.
const successionWindow = 7 * 24 * time.Hour

// deleteLeaderlessGroupsOnce enforces the succession deadline.
//
// When the last admin leaves, the hosts are offered the group, then the
// ordinary members. This is what happens when nobody takes it: the group is
// deleted. It is the one place in this project where inactivity deletes rather
// than hides, and the reason is specific — a leaderless group cannot be
// reactivated by anybody, so hiding it would simply be forgetting about it
// while keeping it.
func (a *API) deleteLeaderlessGroupsOnce(ctx context.Context) {
	if !a.groups {
		return
	}

	groups, err := a.store.ListLeaderlessGroups(ctx, successionWindow)
	if err != nil {
		log.Error().Err(err).Msg("cannot look for leaderless groups")
		return
	}

	deleted := 0
	for _, group := range groups {
		if err := a.store.DeleteGroup(ctx, group.ID); err != nil {
			log.Error().Err(err).Str("group", group.ID).
				Msg("cannot delete a group nobody took on")
			continue
		}
		deleted++
		// WARN, not DEBUG. A group disappearing is the most consequential
		// thing this sweep does, and it is not recoverable.
		log.Warn().Str("group", group.Name).Str("id", group.ID).
			Msg("group deleted: a week passed with nobody able to run it")
	}

	// Its actions went with it.
	if deleted > 0 {
		a.cache.Drop(cache.Groups, cache.Actions)
	}
}

// actionConfirmationWindow is how long a recurrent action stands without
// anybody vouching that it still happens.
//
// A starting value, like the classifier thresholds, and a year because that is
// long enough that a monthly meeting is never asked twice, short enough that a
// map is not advertising a room that closed.
const actionConfirmationWindow = 365 * 24 * time.Hour

// retireActionsOnce takes finished and unconfirmed actions off the map.
//
// It runs on the same tick as the spam purge and it runs whether or not
// anybody is looking, for the same reason: a map full of meetings that stopped
// happening two years ago is worse than an empty one, because it tells people
// the movement is dead.
//
// Retirement is not deletion. The rows stay; only the listing changes.
func (a *API) retireActionsOnce(ctx context.Context) {
	if !a.groups {
		return
	}

	// Rhythms move on first. A recurrent action whose meeting was last night
	// has a next date, and retiring it because the stored one has passed
	// would take a live monthly meeting off the map every month.
	moved, err := a.store.RefreshNextOccurrences(ctx, time.Now())
	if err != nil {
		log.Error().Err(err).Msg("cannot move the recurring actions on")
		return
	}
	if moved > 0 {
		log.Debug().Int64("actions", moved).Msg("recurring actions moved to their next date")
	}

	retired, err := a.store.RetirePastActions(ctx, actionConfirmationWindow)
	if err != nil {
		log.Error().Err(err).Msg("cannot retire past actions")
		return
	}
	if retired > 0 {
		log.Info().Int64("actions", retired).
			Msg("actions retired: their date passed, or nobody confirmed they still happen")
	}

	// Retiring takes an action off the map, and takes a group with no live
	// actions left off it too.
	if moved > 0 || retired > 0 {
		a.cache.Drop(cache.Actions, cache.Groups)
	}
}

// purgeCredentialLeftoversOnce deletes the half-finished exchanges.
//
// A WebAuthn challenge, a device-linking token and an expired session are all
// short-lived rows that authorise something, and each is worthless the moment
// it lapses. Keeping them would be keeping a record of which device somebody
// used and when, for no purpose at all — and retention that nothing enforces
// is not retention.
func (a *API) purgeCredentialLeftoversOnce(ctx context.Context) {
	if purged, err := a.store.PurgeCeremonies(ctx); err != nil {
		log.Error().Err(err).Msg("cannot purge expired WebAuthn ceremonies")
	} else if purged > 0 {
		log.Debug().Int64("ceremonies", purged).Msg("expired WebAuthn ceremonies purged")
	}

	if purged, err := a.store.PurgeLinkTokens(ctx); err != nil {
		log.Error().Err(err).Msg("cannot purge expired device links")
	} else if purged > 0 {
		log.Debug().Int64("links", purged).Msg("expired device links purged")
	}

	if purged, err := a.store.PurgeSessions(ctx); err != nil {
		log.Error().Err(err).Msg("cannot purge expired sessions")
	} else if purged > 0 {
		log.Debug().Int64("sessions", purged).Msg("expired sessions purged")
	}
}
