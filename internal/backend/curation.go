package backend

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/cache"
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/store"
)

func (a *API) registerCurationRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-curation-queue",
		Method:      http.MethodGet,
		// Not /v1/curation: that is the settings resource. The queue is its
		// own thing and says so.
		Path:    "/v1/curation/queue",
		Summary: "List submissions awaiting a human decision",
		Description: "Includes submissions the classifier has not ruled on, not only those it " +
			"referred. If no model is configured or reachable, everything arrives here: " +
			"humans are the last line, so the register is never blocked by a machine.",
		Tags: []string{"Curation"},
	}, a.listCurationQueue)

	huma.Register(api, huma.Operation{
		OperationID: "list-group-queue",
		Method:      http.MethodGet,
		Path:        "/v1/curation/groups",
		Summary:     "List groups awaiting a human decision",
		Description: "Includes groups the classifier has not ruled on. If no model is " +
			"configured or reachable everything arrives here: humans are the last line, and " +
			"a map that stays empty because a machine is down has failed the people waiting " +
			"to be found.",
		Tags: []string{"Curation"},
	}, a.listGroupQueue)

	huma.Register(api, invalidates(cache.Groups)(huma.Operation{
		OperationID: "accept-group",
		Method:      http.MethodPost,
		Path:        "/v1/curation/groups/{id}/accept",
		Summary:     "Put a group on the map",
		Tags:        []string{"Curation"},
	}), a.acceptGroup)

	huma.Register(api, invalidates(cache.Groups)(huma.Operation{
		OperationID: "reject-group",
		Method:      http.MethodPost,
		Path:        "/v1/curation/groups/{id}/reject",
		Summary:     "Refuse a group",
		Description: "Rejection deletes it, together with the contact address that created it. " +
			"The audit log keeps the decision, the curator and the reason — never the " +
			"content, and never the address.",
		Tags: []string{"Curation"},
	}), a.rejectGroup)

	huma.Register(api, huma.Operation{
		OperationID: "list-all-groups",
		Method:      http.MethodGet,
		Path:        "/v1/curation/groups/all",
		Summary:     "Every group, page by page",
		Description: "At every status, newest first — the administrative listing rather than " +
			"the map. A console that could only show what is already public could not " +
			"answer the question it is most often asked: what happened to the group " +
			"somebody proposed.",
		Tags: []string{"Curation"},
	}, a.listAllGroups)

	huma.Register(api, huma.Operation{
		OperationID: "list-group-revisions",
		Method:      http.MethodGet,
		Path:        "/v1/curation/group-revisions",
		Summary:     "List edits to published groups awaiting a decision",
		Description: "Each carries the group as it currently stands, because the question is " +
			"not \"is this a good group?\" but \"is this a good change to that group?\", and " +
			"the second cannot be answered without the first.",
		Tags: []string{"Curation"},
	}, a.listGroupRevisionQueue)

	huma.Register(api, invalidates(cache.Groups)(huma.Operation{
		OperationID: "accept-group-revision",
		Method:      http.MethodPost,
		Path:        "/v1/curation/group-revisions/{id}/accept",
		Summary:     "Apply an edit to its group",
		Tags:        []string{"Curation"},
	}), a.acceptGroupRevision)

	huma.Register(api, invalidates(cache.Groups)(huma.Operation{
		OperationID: "reject-group-revision",
		Method:      http.MethodPost,
		Path:        "/v1/curation/group-revisions/{id}/reject",
		Summary:     "Discard an edit",
		Description: "The group is untouched and stays on the map saying what it said before. " +
			"Rejecting an edit is not rejecting a group.",
		Tags: []string{"Curation"},
	}), a.rejectGroupRevision)

	huma.Register(api, huma.Operation{
		OperationID: "list-action-queue",
		Method:      http.MethodGet,
		Path:        "/v1/curation/actions",
		Summary:     "List actions awaiting a human decision",
		Description: "Each carries the group that announced it: \"can people come to this?\" " +
			"is not answerable without knowing whose meeting it is.",
		Tags: []string{"Curation"},
	}, a.listActionQueue)

	huma.Register(api, invalidates(cache.Actions, cache.Groups)(huma.Operation{
		OperationID: "accept-action",
		Method:      http.MethodPost,
		Path:        "/v1/curation/actions/{id}/accept",
		Summary:     "Publish an action",
		Description: "It reaches the group's page, and brings the group back onto the map if " +
			"inactivity had taken it off — a group returns by posting an action.",
		Tags: []string{"Curation"},
	}), a.acceptAction)

	huma.Register(api, invalidates(cache.Actions)(huma.Operation{
		OperationID: "reject-action",
		Method:      http.MethodPost,
		Path:        "/v1/curation/actions/{id}/reject",
		Summary:     "Refuse an action",
		Description: "Deletes it. The group is untouched: refusing what a group announced is " +
			"not refusing the group.",
		Tags: []string{"Curation"},
	}), a.rejectAction)

	huma.Register(api, huma.Operation{
		OperationID: "get-curation-item",
		Method:      http.MethodGet,
		Path:        "/v1/curation/{id}",
		Summary:     "Read one submission as a curator sees it",
		Description: "The submission plus what the classifier made of it — the score, the model " +
			"that produced it and when. Distinct from GET /v1/messages/{id}, which is the " +
			"public view and deliberately carries none of that: a reader of the register has " +
			"no business knowing how confident a machine was about somebody's grievance.",
		Tags: []string{"Curation"},
	}, a.getCurationItem)

	huma.Register(api, invalidates(cache.Messages)(huma.Operation{
		OperationID: "accept-message",
		Method:      http.MethodPost,
		Path:        "/v1/curation/{id}/accept",
		Summary:     "Publish a doléance",
		Tags:        []string{"Curation"},
	}), a.acceptMessage)

	huma.Register(api, invalidates(cache.Messages)(huma.Operation{
		OperationID: "reject-message",
		Method:      http.MethodPost,
		Path:        "/v1/curation/{id}/reject",
		Summary:     "Refuse a submission",
		Description: "Rejection deletes the submission. The audit log keeps the decision, the " +
			"curator and the reason — never the text.",
		Tags: []string{"Curation"},
	}), a.rejectMessage)
}

// CurationItem is one submission in front of a curator.
type CurationItem struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Nickname  string    `json:"nickname,omitempty"`
	BirthYear int       `json:"birth_year,omitempty"`
	Activity  string    `json:"activity,omitempty"`
	Language  string    `json:"language,omitempty"`
	Place     string    `json:"place,omitempty"`
	CreatedAt time.Time `json:"created_at"`

	// Status distinguishes a submission the classifier referred to a human
	// from one it never scored.
	Status string `json:"status"`

	// Assessed is false when no classifier has ruled on this submission. The
	// console says so plainly: a curator deciding without a score should know
	// that is what they are doing.
	Assessed bool `json:"assessed"`

	// Confidence is the classifier's score, meaningless when Assessed is false.
	Confidence int `json:"confidence"`

	// AssessedBy is the model that produced the score, so a decision can be
	// traced to a model version when thresholds are tuned or a model swapped.
	AssessedBy string `json:"assessed_by,omitempty"`

	// DropReason names the guard that refused this before any classifier saw
	// it — "duplicate" or "payload" — and is empty when a score decided.
	DropReason string `json:"drop_reason,omitempty"`

	// Reason is the model's own sentence about its verdict. A curator deciding
	// in seconds is deciding about a text they have not read twice, and this
	// is the fastest way in — including when it is wrong, which is itself the
	// most useful thing to see.
	Reason string `json:"reason,omitempty"`

	// DuplicateOf names the earlier doléance this one repeats, when it was
	// dropped before any classifier saw it.
	DuplicateOf string `json:"duplicate_of,omitempty"`
}

// CurationQueueOutput is the queue.
type CurationQueueOutput struct {
	Body struct {
		Items []CurationItem `json:"items"`

		// Unassessed counts the submissions no classifier has ruled on. When
		// this equals the queue length, the machine is doing nothing and the
		// console should say so rather than let it look normal.
		Unassessed int `json:"unassessed"`

		// Page and PerPage are what the console needs to say where in the
		// queue this is and whether there is another page after it.
		Page    int `json:"page"`
		PerPage int `json:"per_page"`
	}
}

// CurationQueueInput selects a page of the listing.
type CurationQueueInput struct {
	Limit int `query:"limit" default:"100"`

	// Page walks the queue. It is paged rather than capped because the cap
	// was the whole answer before: past the limit the queue simply stopped,
	// and a submission that fell off the end waited for ever with nothing to
	// show it was there.
	Page    int `query:"page" minimum:"1" default:"1"`
	PerPage int `query:"per_page" minimum:"1" maximum:"200" default:"25"`
}

func (a *API) listCurationQueue(ctx context.Context, in *CurationQueueInput) (*CurationQueueOutput, error) {
	page, perPage := in.Page, in.PerPage
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 25
	}

	messages, err := a.store.ListCurationQueue(ctx, perPage, (page-1)*perPage)
	if err != nil {
		log.Error().Err(err).Msg("cannot read the curation queue")
		return nil, huma.Error500InternalServerError("cannot read the queue")
	}

	out := &CurationQueueOutput{}
	out.Body.Page, out.Body.PerPage = page, perPage
	out.Body.Items = make([]CurationItem, 0, len(messages))
	for _, message := range messages {
		item := toCurationItem(message)

		// In the queue, "assessed" means the classifier referred this here
		// rather than that it ever ran: a submission that reached a human
		// because no model answered is the case the console must not let look
		// normal.
		item.Assessed = message.Status == models.StatusCurating
		if !item.Assessed {
			out.Body.Unassessed++
		}
		out.Body.Items = append(out.Body.Items, item)
	}
	return out, nil
}

// CurationItemOutput is one submission with its assessment.
type CurationItemOutput struct {
	Body CurationItem
}

func (a *API) getCurationItem(ctx context.Context, in *MessageIDInput) (*CurationItemOutput, error) {
	message, err := a.store.GetMessage(ctx, in.ID)
	if errors.Is(err, store.ErrMessageNotFound) {
		return nil, huma.Error404NotFound("no such submission")
	}
	if err != nil {
		log.Error().Err(err).Str("message", in.ID).Msg("cannot read a submission")
		return nil, huma.Error500InternalServerError("cannot read the submission")
	}
	return &CurationItemOutput{Body: toCurationItem(message)}, nil
}

// toCurationItem is the curator's view of a submission, shared by the queue
// and the single-item read so the two can never drift into showing different
// things about the same row.
func toCurationItem(message models.Message) CurationItem {
	item := CurationItem{
		ID:          message.ID,
		Text:        message.Text,
		Nickname:    message.Nickname,
		BirthYear:   message.BirthYear,
		Activity:    message.Activity,
		Language:    message.Language,
		CreatedAt:   message.CreatedAt,
		Status:      string(message.Status),
		Assessed:    message.AssessedAt != nil,
		AssessedBy:  message.AssessedBy,
		Confidence:  message.Confidence,
		Reason:      message.AssessmentReason,
		DropReason:  message.DropReason,
		DuplicateOf: message.DuplicateOf,
	}
	if message.Location != nil {
		item.Place = message.Location.Label
	}
	return item
}

// CurationDecisionInput is a curator's ruling.
type CurationDecisionInput struct {
	ID   string `path:"id"`
	Body struct {
		Actor  string `json:"actor,omitempty"`
		Reason string `json:"reason,omitempty"`
	}
}

// CurationDecisionOutput reports what happened.
type CurationDecisionOutput struct {
	Body struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
}

func (a *API) acceptMessage(ctx context.Context, in *CurationDecisionInput) (*CurationDecisionOutput, error) {
	err := a.store.AcceptMessage(ctx, in.ID)
	if errors.Is(err, store.ErrMessageNotFound) {
		return nil, huma.Error404NotFound("no such submission, or it has already been decided")
	}
	if err != nil {
		log.Error().Err(err).Str("message", in.ID).Msg("cannot accept a submission")
		return nil, huma.Error500InternalServerError("cannot accept the submission")
	}

	a.recordDecision(ctx, in.Body.Actor, models.AuditAccept, "message", in.ID, in.Body.Reason)
	log.Info().Str("message", in.ID).Str("actor", in.Body.Actor).Msg("doléance accepted")

	out := &CurationDecisionOutput{}
	out.Body.ID = in.ID
	out.Body.Status = string(models.StatusAccepted)
	return out, nil
}

func (a *API) rejectMessage(ctx context.Context, in *CurationDecisionInput) (*CurationDecisionOutput, error) {
	// The audit entry is written before the deletion, so a failure leaves a
	// record of an attempted decision rather than a message that vanished
	// with nothing to say who decided.
	a.recordDecision(ctx, in.Body.Actor, models.AuditReject, "message", in.ID, in.Body.Reason)

	err := a.store.RejectMessage(ctx, in.ID)
	if errors.Is(err, store.ErrMessageNotFound) {
		return nil, huma.Error404NotFound("no such submission, or it has already been decided")
	}
	if err != nil {
		log.Error().Err(err).Str("message", in.ID).Msg("cannot reject a submission")
		return nil, huma.Error500InternalServerError("cannot reject the submission")
	}

	log.Info().Str("message", in.ID).Str("actor", in.Body.Actor).Msg("doléance rejected and deleted")

	out := &CurationDecisionOutput{}
	out.Body.ID = in.ID
	out.Body.Status = string(models.StatusRejected)
	return out, nil
}

// ---------------------------------------------------------------------------
// Groups in front of a curator
// ---------------------------------------------------------------------------

// GroupCurationItem is one group awaiting a decision.
//
// It used to carry the contact addresses on the argument that a curator
// deciding about a group is deciding about people who can be reached. There are
// no addresses any more, and the argument turns out to have been about the old
// shape rather than about curation: what a curator judges is whether people in
// a place really mean to meet, and the evidence for that is a name, a place and
// a description.
type GroupCurationItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Place       string `json:"place,omitempty"`

	Status string `json:"status"`

	// Assessed is false when no classifier has ruled. The console says so
	// plainly: deciding without a score is a different act from confirming one.
	Assessed   bool   `json:"assessed"`
	Confidence int    `json:"confidence"`
	Reason     string `json:"reason,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// GroupQueueOutput is the queue.
type GroupQueueOutput struct {
	Body struct {
		Items []GroupCurationItem `json:"items"`

		// Unassessed counts what no classifier has ruled on. When it equals
		// the queue length the machine is doing nothing, and the console
		// should say so rather than let it look normal.
		Unassessed int `json:"unassessed"`
	}
}

func (a *API) listGroupQueue(ctx context.Context, in *CurationQueueInput) (*GroupQueueOutput, error) {
	groups, err := a.store.ListGroupCurationQueue(ctx, in.Limit)
	if err != nil {
		log.Error().Err(err).Msg("cannot read the group queue")
		return nil, huma.Error500InternalServerError("cannot read the queue")
	}

	out := &GroupQueueOutput{}
	out.Body.Items = make([]GroupCurationItem, 0, len(groups))
	for _, group := range groups {
		item := GroupCurationItem{
			ID:          group.ID,
			Name:        group.Name,
			Description: group.Description,
			Place:       group.Location.Label,
			Status:      string(group.Status),
			Assessed:    group.AssessedAt != nil,
			Confidence:  group.Confidence,
			Reason:      group.AssessmentReason,
			CreatedAt:   group.CreatedAt,
		}
		if !item.Assessed {
			out.Body.Unassessed++
		}
		out.Body.Items = append(out.Body.Items, item)
	}
	return out, nil
}

// AllGroupsInput selects a page.
type AllGroupsInput struct {
	Page    int `query:"page" minimum:"1" default:"1"`
	PerPage int `query:"per_page" minimum:"1" maximum:"200" default:"25"`
}

// AllGroupsOutput is one page of the listing.
type AllGroupsOutput struct {
	Body struct {
		Groups []GroupCurationItem `json:"groups"`

		// Page, PerPage and Total are what the console needs to say "12 of
		// 340" and to know whether there is a page after this one.
		Page    int   `json:"page"`
		PerPage int   `json:"per_page"`
		Total   int64 `json:"total"`
	}
}

func (a *API) listAllGroups(ctx context.Context, in *AllGroupsInput) (*AllGroupsOutput, error) {
	page, perPage := in.Page, in.PerPage
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 25
	}

	listing, err := a.store.ListAllGroups(ctx, (page-1)*perPage, perPage)
	if err != nil {
		log.Error().Err(err).Msg("cannot list the groups")
		return nil, huma.Error500InternalServerError("cannot read the groups")
	}

	out := &AllGroupsOutput{}
	out.Body.Page, out.Body.PerPage, out.Body.Total = page, perPage, listing.Total
	out.Body.Groups = make([]GroupCurationItem, 0, len(listing.Groups))
	for _, group := range listing.Groups {
		item := GroupCurationItem{
			ID:          group.ID,
			Name:        group.Name,
			Description: group.Description,
			Place:       group.Location.Label,
			Status:      string(group.Status),
			Assessed:    group.AssessedAt != nil,
			Confidence:  group.Confidence,
			Reason:      group.AssessmentReason,
			CreatedAt:   group.CreatedAt,
		}
		out.Body.Groups = append(out.Body.Groups, item)
	}
	return out, nil
}

// GroupRevisionItem is a proposed change, beside what it would replace.
type GroupRevisionItem struct {
	// ID is the group's, not the revision's: there is only ever one edit per
	// group, and a curator acting on it is acting on that group.
	ID string `json:"id"`

	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Place       string `json:"place,omitempty"`

	// The group as it stands, so the console can show what changed rather
	// than asking a curator to remember.
	CurrentName        string `json:"current_name"`
	CurrentDescription string `json:"current_description,omitempty"`
	CurrentPlace       string `json:"current_place,omitempty"`

	// Moved says the pin changed, which a label alone can hide: two points
	//200 metres apart are both "Düsseldorf".
	Moved bool `json:"moved"`

	Status     string `json:"status"`
	Assessed   bool   `json:"assessed"`
	Confidence int    `json:"confidence"`
	Reason     string `json:"reason,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// GroupRevisionQueueOutput is the edits waiting.
type GroupRevisionQueueOutput struct {
	Body struct {
		Items      []GroupRevisionItem `json:"items"`
		Unassessed int                 `json:"unassessed"`
	}
}

func (a *API) listGroupRevisionQueue(ctx context.Context, in *CurationQueueInput) (*GroupRevisionQueueOutput, error) {
	revisions, before, err := a.store.ListGroupRevisionQueue(ctx, in.Limit)
	if err != nil {
		log.Error().Err(err).Msg("cannot read the group revision queue")
		return nil, huma.Error500InternalServerError("cannot read the queue")
	}

	out := &GroupRevisionQueueOutput{}
	out.Body.Items = make([]GroupRevisionItem, 0, len(revisions))
	for _, revision := range revisions {
		group := before[revision.GroupID]
		item := GroupRevisionItem{
			ID:          revision.GroupID,
			Name:        revision.Name,
			Description: revision.Description,
			Place:       revision.Location.Label,

			CurrentName:        group.Name,
			CurrentDescription: group.Description,
			CurrentPlace:       group.Location.Label,

			Moved: revision.Location.Geohash != group.Location.Geohash,

			Status:     string(revision.Status),
			Assessed:   revision.AssessedBy != "",
			Confidence: revision.Confidence,
			Reason:     revision.AssessmentReason,
			CreatedAt:  revision.CreatedAt,
		}
		if !item.Assessed {
			out.Body.Unassessed++
		}
		out.Body.Items = append(out.Body.Items, item)
	}
	return out, nil
}

func (a *API) acceptGroupRevision(ctx context.Context, in *CurationDecisionInput) (*CurationDecisionOutput, error) {
	err := a.store.AcceptGroupRevision(ctx, in.ID)
	switch {
	case errors.Is(err, store.ErrRevisionNotFound):
		return nil, huma.Error404NotFound("no edit is waiting on that group")
	case errors.Is(err, store.ErrNameTaken):
		// Somebody took the name between the proposal and the decision. Said
		// as an answer rather than a server error: it is exactly what the
		// curator asked about.
		return nil, huma.Error409Conflict("that name has been taken since the edit was written")
	case err != nil:
		log.Error().Err(err).Str("group", in.ID).Msg("cannot accept a group edit")
		return nil, huma.Error500InternalServerError("cannot accept the edit")
	}

	a.recordDecision(ctx, in.Body.Actor, models.AuditAccept, "group_revision", in.ID, in.Body.Reason)
	log.Info().Str("group", in.ID).Str("actor", in.Body.Actor).Msg("group edit accepted")

	out := &CurationDecisionOutput{}
	out.Body.ID = in.ID
	out.Body.Status = string(models.StatusAccepted)
	return out, nil
}

func (a *API) rejectGroupRevision(ctx context.Context, in *CurationDecisionInput) (*CurationDecisionOutput, error) {
	a.recordDecision(ctx, in.Body.Actor, models.AuditReject, "group_revision", in.ID, in.Body.Reason)

	err := a.store.RejectGroupRevision(ctx, in.ID)
	if errors.Is(err, store.ErrRevisionNotFound) {
		return nil, huma.Error404NotFound("no edit is waiting on that group")
	}
	if err != nil {
		log.Error().Err(err).Str("group", in.ID).Msg("cannot reject a group edit")
		return nil, huma.Error500InternalServerError("cannot reject the edit")
	}

	log.Info().Str("group", in.ID).Str("actor", in.Body.Actor).Msg("group edit refused")

	out := &CurationDecisionOutput{}
	out.Body.ID = in.ID
	out.Body.Status = string(models.StatusRejected)
	return out, nil
}

// ActionQueueEntry is one announcement waiting, with whose it is.
type ActionQueueEntry struct {
	ActionItem

	Group      string `json:"group"`
	GroupID    string `json:"group_id"`
	GroupPlace string `json:"group_place,omitempty"`
}

// ActionQueueOutput is what is waiting.
type ActionQueueOutput struct {
	Body struct {
		Items      []ActionQueueEntry `json:"items"`
		Unassessed int                `json:"unassessed"`
	}
}

func (a *API) listActionQueue(ctx context.Context, in *CurationQueueInput) (*ActionQueueOutput, error) {
	items, err := a.store.ListActionCurationQueue(ctx, in.Limit)
	if err != nil {
		log.Error().Err(err).Msg("cannot read the action queue")
		return nil, huma.Error500InternalServerError("cannot read the queue")
	}

	out := &ActionQueueOutput{}
	out.Body.Items = make([]ActionQueueEntry, 0, len(items))
	for _, item := range items {
		entry := ActionQueueEntry{
			ActionItem: toActionItem(item.Action, true),
			Group:      item.Group.Name,
			GroupID:    item.Group.ID,
			GroupPlace: item.Group.Location.Label,
		}
		if !entry.Assessed {
			out.Body.Unassessed++
		}
		out.Body.Items = append(out.Body.Items, entry)
	}
	return out, nil
}

func (a *API) acceptAction(ctx context.Context, in *CurationDecisionInput) (*CurationDecisionOutput, error) {
	err := a.store.AcceptAction(ctx, in.ID)
	if errors.Is(err, store.ErrActionNotFound) {
		// No such action, and nothing else: accepting one that is already
		// accepted succeeds. See store.AcceptAction for why it has to.
		return nil, huma.Error404NotFound("no such action")
	}
	if err != nil {
		log.Error().Err(err).Str("action", in.ID).Msg("cannot accept an action")
		return nil, huma.Error500InternalServerError("cannot accept the action")
	}

	a.recordDecision(ctx, in.Body.Actor, models.AuditAccept, "action", in.ID, in.Body.Reason)
	log.Info().Str("action", in.ID).Str("actor", in.Body.Actor).Msg("action accepted")

	out := &CurationDecisionOutput{}
	out.Body.ID = in.ID
	out.Body.Status = string(models.StatusAccepted)
	return out, nil
}

func (a *API) rejectAction(ctx context.Context, in *CurationDecisionInput) (*CurationDecisionOutput, error) {
	// Written before the deletion, so a failure leaves a record of an
	// attempted decision rather than an action that vanished with nothing to
	// say who decided.
	a.recordDecision(ctx, in.Body.Actor, models.AuditReject, "action", in.ID, in.Body.Reason)

	err := a.store.RejectAction(ctx, in.ID)
	if errors.Is(err, store.ErrActionNotFound) {
		return nil, huma.Error404NotFound("no such action")
	}
	if err != nil {
		log.Error().Err(err).Str("action", in.ID).Msg("cannot reject an action")
		return nil, huma.Error500InternalServerError("cannot reject the action")
	}

	log.Info().Str("action", in.ID).Str("actor", in.Body.Actor).Msg("action refused")

	out := &CurationDecisionOutput{}
	out.Body.ID = in.ID
	out.Body.Status = string(models.StatusRejected)
	return out, nil
}

func (a *API) acceptGroup(ctx context.Context, in *CurationDecisionInput) (*CurationDecisionOutput, error) {
	err := a.store.AcceptGroup(ctx, in.ID)
	if errors.Is(err, store.ErrGroupNotFound) {
		return nil, huma.Error404NotFound("no such group, or it has already been decided")
	}
	if err != nil {
		log.Error().Err(err).Str("group", in.ID).Msg("cannot accept a group")
		return nil, huma.Error500InternalServerError("cannot accept the group")
	}

	a.recordDecision(ctx, in.Body.Actor, models.AuditAccept, "group", in.ID, in.Body.Reason)
	log.Info().Str("group", in.ID).Str("actor", in.Body.Actor).Msg("group accepted")

	out := &CurationDecisionOutput{}
	out.Body.ID = in.ID
	out.Body.Status = string(models.StatusAccepted)
	return out, nil
}

func (a *API) rejectGroup(ctx context.Context, in *CurationDecisionInput) (*CurationDecisionOutput, error) {
	// Written before the deletion, so a failure leaves a record of an
	// attempted decision rather than a group that vanished with nothing to
	// say who decided.
	a.recordDecision(ctx, in.Body.Actor, models.AuditReject, "group", in.ID, in.Body.Reason)

	err := a.store.RejectGroup(ctx, in.ID)
	if errors.Is(err, store.ErrGroupNotFound) {
		return nil, huma.Error404NotFound("no such group, or it has already been decided")
	}
	if err != nil {
		log.Error().Err(err).Str("group", in.ID).Msg("cannot reject a group")
		return nil, huma.Error500InternalServerError("cannot reject the group")
	}

	log.Info().Str("group", in.ID).Str("actor", in.Body.Actor).
		Msg("group rejected and deleted, with the address that created it")

	out := &CurationDecisionOutput{}
	out.Body.ID = in.ID
	out.Body.Status = string(models.StatusRejected)
	return out, nil
}
