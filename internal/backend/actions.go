package backend

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/cache"
	"github.com/CoderSyndicate/doleances/internal/content"
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/recur"
	"github.com/CoderSyndicate/doleances/internal/store"
)

// What an action's own words may be.
const (
	maxActionTitleRunes       = 160
	maxActionDescriptionRunes = 2000
	maxRecurrenceRunes        = 120
)

func (a *API) registerActionRoutes(api huma.API) {
	huma.Register(api, authenticated(huma.Operation{
		OperationID: "list-group-actions",
		Method:      http.MethodGet,
		Path:        "/v1/groups/{id}/actions/all",
		Summary:     "Everything a group has announced",
		Description: "At any status, behind being an admin or a host of the group. An action " +
			"waiting on a curator has to be visible to the people who wrote it, or they " +
			"will write it again.",
		Tags: []string{"Actions"},
	}), a.listGroupActions)

	huma.Register(api, invalidates(cache.Actions, cache.Groups)(authenticated(huma.Operation{
		OperationID: "create-action",
		Method:      http.MethodPost,
		Path:        "/v1/groups/{id}/actions",
		Summary:     "Announce an action",
		Description: "Enters the same pipeline as everything else. The group behind it was " +
			"already reviewed, which is why the bar is lower — not why there is no bar.",
		Tags: []string{"Actions"},
	})), a.createAction)

	huma.Register(api, invalidates(cache.Actions)(authenticated(huma.Operation{
		OperationID: "edit-action",
		Method:      http.MethodPatch,
		Path:        "/v1/groups/{id}/actions/{action}",
		Summary:     "Change an announcement",
		Description: "In place, unlike a group, and back through assessment. Keeping a stale " +
			"time on the map while a correction waits would send people to a meeting that " +
			"is not happening.",
		Tags: []string{"Actions"},
	})), a.editAction)

	huma.Register(api, invalidates(cache.Actions)(authenticated(huma.Operation{
		OperationID: "delete-action",
		Method:      http.MethodDelete,
		Path:        "/v1/groups/{id}/actions/{action}",
		Summary:     "Call an action off",
		Description: "Deletion, not retirement: retirement is what time does to an action " +
			"nobody confirmed, deleting is a group saying this is not happening.",
		Tags: []string{"Actions"},
	})), a.deleteAction)

	huma.Register(api, invalidates(cache.Actions)(authenticated(huma.Operation{
		OperationID: "confirm-action",
		Method:      http.MethodPost,
		Path:        "/v1/groups/{id}/actions/{action}/confirm",
		Summary:     "Say a recurrent action still happens",
		Description: "Restarts its year. A recurrent action has no date to expire on, so it " +
			"needs the opposite guard.",
		Tags: []string{"Actions"},
	})), a.confirmAction)

	huma.Register(api, huma.Operation{
		OperationID: "list-public-actions",
		Method:      http.MethodGet,
		Path:        "/v1/groups/{id}/actions",
		Summary:     "What a group is offering",
		Description: "Accepted, not retired, soonest first. This is what the group page shows.",
		Tags:        []string{"Actions"},
	}, a.listPublicActions)
}

// ActionItem is one announcement.
type ActionItem struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type"`

	// StartsOn is a date rather than a moment: an action is announced on a
	// day, and a time zone on the wire would invite somebody to render it in
	// theirs, which is a different day.
	StartsOn string `json:"starts_on,omitempty"`
	StartsAt string `json:"starts_at,omitempty"`

	// Rule is the RFC 5545 recurrence rule, and Note the nuance it cannot
	// carry. Parts is the same rule broken up, so a page can put it into a
	// sentence in the reader's language rather than showing them an RRULE.
	Rule  string          `json:"rule,omitempty"`
	Note  string          `json:"note,omitempty"`
	Parts *RecurrenceInfo `json:"parts,omitempty"`

	// NextOn is when this next happens — derived and stored, never computed
	// here. Empty when nothing is coming.
	NextOn string `json:"next_on,omitempty"`

	Place string `json:"place,omitempty"`

	// Latitude, Longitude and Elsewhere describe where it happens.
	//
	// Elsewhere says the action has a venue of its own rather than the group's
	// meeting place, which is the thing a management form has to know and
	// cannot work out for itself: an inherited place is a *copy* of the
	// group's, so by the time it reaches a page the two are indistinguishable
	// without the group beside them.
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
	Elsewhere bool    `json:"elsewhere"`

	Status     string `json:"status"`
	Retired    bool   `json:"retired"`
	Assessed   bool   `json:"assessed"`
	Confidence int    `json:"confidence"`
	Reason     string `json:"reason,omitempty"`
}

// ActionListOutput is a group's announcements.
type ActionListOutput struct {
	Body struct {
		Actions []ActionItem `json:"actions"`
	}
}

// RecurrenceInfo is a rhythm broken into the pieces a sentence needs.
//
// The sentence is assembled by whichever page shows it, from its own
// catalogue, in its reader's language — the backend has no business knowing
// how to say "the fourth Thursday of the month" in fifty languages.
type RecurrenceInfo struct {
	// Key names the shape of the sentence.
	Key string `json:"key"`

	Interval int   `json:"interval,omitempty"`
	Weekdays []int `json:"weekdays,omitempty"`
	Week     int   `json:"week,omitempty"`
	Day      int   `json:"day,omitempty"`
	Month    int   `json:"month,omitempty"`
}

func toActionItem(action models.Action, withVerdict bool) ActionItem {
	item := ActionItem{
		ID:          action.ID,
		Title:       action.Title,
		Description: action.Description,
		Type:        string(action.Type),
		Rule:        action.RecurrenceRule,
		Note:        action.RecurrenceNote,
		Place:       action.Location.Label,
		Latitude:    action.Location.Latitude,
		Longitude:   action.Location.Longitude,
		Status:      string(action.Status),
		Retired:     action.Retired,
	}
	if action.StartsAt != nil {
		item.StartsOn = action.StartsAt.Format("2006-01-02")
		item.StartsAt = action.StartsAt.Format("2006-01-02T15:04")
	}
	if action.NextOccurrenceAt != nil {
		item.NextOn = action.NextOccurrenceAt.Format("2006-01-02T15:04")
	}
	if rule, err := recur.Parse(action.RecurrenceRule); err == nil {
		described := recur.Describe(rule)
		info := &RecurrenceInfo{
			Key: described.Key, Interval: described.Interval,
			Week: described.Week, Day: described.Day, Month: described.Month,
		}
		for _, weekday := range described.Weekdays {
			info.Weekdays = append(info.Weekdays, int(weekday))
		}
		item.Parts = info
	}
	// The score and the model's sentence are for whoever manages the group or
	// curates it, never for a reader of the page: how confident a machine was
	// about somebody's meeting is nobody else's business.
	if withVerdict {
		item.Assessed = action.AssessedAt != nil
		item.Confidence = action.Confidence
		item.Reason = action.AssessmentReason
	}
	return item
}

func (a *API) listGroupActions(ctx context.Context, in *MessageIDInput) (*ActionListOutput, error) {
	if _, err := a.groupHost(ctx, in.ID); err != nil {
		return nil, err
	}
	return a.actionList(ctx, in.ID)
}

func (a *API) actionList(ctx context.Context, id string) (*ActionListOutput, error) {
	actions, err := a.store.ListGroupActions(ctx, id)
	if err != nil {
		log.Error().Err(err).Str("group", id).Msg("cannot read the actions")
		return nil, huma.Error500InternalServerError("cannot read the actions")
	}

	// The group's own place, to say which actions have one of their own.
	//
	// Compared by geohash rather than by coordinate: the geohash is derived
	// from the pair on every write, so it is exact where comparing two floats
	// for equality is a coin toss.
	//
	// An action pinned to precisely where the group meets reads as inherited,
	// which is indistinguishable and does not matter: both mean "here".
	var venue string
	if group, err := a.store.GetGroup(ctx, id); err == nil {
		venue = group.Location.Geohash
	}

	out := &ActionListOutput{}
	out.Body.Actions = make([]ActionItem, 0, len(actions))
	for _, action := range actions {
		item := toActionItem(action, true)
		item.Elsewhere = action.Location.Geohash != "" && action.Location.Geohash != venue
		out.Body.Actions = append(out.Body.Actions, item)
	}
	return out, nil
}

func (a *API) listPublicActions(ctx context.Context, in *MessageIDInput) (*ActionListOutput, error) {
	actions, err := cache.Fetch(a.cache, cache.Keyed("actions.public", in.ID),
		cache.Actions, func() ([]models.Action, error) {
			return a.store.ListPublicActions(ctx, in.ID, publicActions)
		})
	if err != nil {
		log.Error().Err(err).Str("group", in.ID).Msg("cannot read the actions")
		return nil, huma.Error500InternalServerError("cannot read the actions")
	}

	out := &ActionListOutput{}
	out.Body.Actions = make([]ActionItem, 0, len(actions))
	for _, action := range actions {
		out.Body.Actions = append(out.Body.Actions, toActionItem(action, false))
	}
	return out, nil
}

// publicActions is how many a group page shows: the next five.
const publicActions = 5

// ActionBody is an announcement, on the way in.
//
// Shared by creating and editing, and declared apart from the path parameters
// for a reason Huma enforces: a type that declares `path:"action"` may only be
// used on a route that has that segment, and POST /actions does not. Two
// inputs, one body.
type ActionBody struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`

	// Type is "onetime" or "recurrent".
	Type string `json:"type"`

	// StartsOn is the date: when a one-time action happens, and when a
	// recurrent series begins. StartsTime is the clock, "19:00".
	StartsOn   string `json:"starts_on,omitempty"`
	StartsTime string `json:"starts_time,omitempty"`

	// The rhythm, as the form's own choices. The RRULE is assembled from
	// these rather than typed: nobody writes FREQ=MONTHLY;BYDAY=4TH by hand,
	// and a field that accepted one would be a field somebody could put
	// anything into.
	Repeat   string `json:"repeat,omitempty"`   // "weekly" or "monthly"
	Interval int    `json:"interval,omitempty"` // every N weeks
	Weekdays []int  `json:"weekdays,omitempty"` // 0 Sunday .. 6 Saturday
	Monthly  string `json:"monthly,omitempty"`  // "weekday" or "day"
	Week     int    `json:"week,omitempty"`     // 1..4, or -1 for the last
	Weekday  int    `json:"weekday,omitempty"`
	Day      int    `json:"day,omitempty"`
	Month    int    `json:"month,omitempty"` // 1..12, for a yearly rhythm

	// Note is the nuance no rule carries — "except in August".
	Note string `json:"note,omitempty"`

	// The venue, optional. Absent means the group's own place.
	// Elsewhere says this action happens somewhere other than where the group
	// meets, and the pin below is that place.
	//
	// It is sent explicitly rather than inferred from a pin being present,
	// because the two answers differ in the case that matters: an action that
	// *had* its own venue and is being moved back to the group's. Inferring
	// from an empty pin would make that indistinguishable from "do not change
	// the place", and one of those has to be possible.
	Elsewhere bool `json:"elsewhere,omitempty"`

	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
	Place     string  `json:"place,omitempty"`
	Country   string  `json:"country,omitempty"`
	Zoom      int     `json:"zoom,omitempty"`
}

// NewActionInput announces one. No action in the path: there is not one yet.
type NewActionInput struct {
	ID   string `path:"id"`
	Body ActionBody
}

// EditActionInput changes one that already exists.
type EditActionInput struct {
	ID     string `path:"id"`
	Action string `path:"action"`
	Body   ActionBody
}

// draft validates an announcement and turns it into what the store wants.
func (a *API) draft(ctx context.Context, body ActionBody) (store.ActionDraft, error) {
	title, _ := content.Sanitise(body.Title)
	title = strings.TrimSpace(title)
	description, _ := content.Sanitise(body.Description)
	description = strings.TrimSpace(description)
	note, _ := content.Sanitise(body.Note)
	note = strings.TrimSpace(note)

	switch {
	case title == "":
		return store.ActionDraft{}, huma.Error422UnprocessableEntity("an action needs a title")
	case utf8.RuneCountInString(title) > maxActionTitleRunes:
		return store.ActionDraft{}, huma.Error422UnprocessableEntity("that title is too long")
	case utf8.RuneCountInString(description) > maxActionDescriptionRunes:
		return store.ActionDraft{}, huma.Error422UnprocessableEntity("that description is too long")
	case utf8.RuneCountInString(note) > maxRecurrenceRunes:
		return store.ActionDraft{}, huma.Error422UnprocessableEntity("that note is too long")
	}

	// Every string here is rendered on a public page.
	for _, value := range []string{title, description, note} {
		if content.ContainsExecutablePayload(value) {
			return store.ActionDraft{}, huma.Error422UnprocessableEntity("that cannot be saved")
		}
	}

	draft := store.ActionDraft{
		Title:       title,
		Description: description,
		Type:        models.ActionType(body.Type),
	}
	if !draft.Type.Valid() {
		return store.ActionDraft{}, huma.Error422UnprocessableEntity("an action is one-time or recurrent")
	}

	// Both kinds need a start. For a one-time action it is when it happens;
	// for a recurrent one it is DTSTART — the first occurrence, the time of
	// day, and what an interval is counted from.
	when, err := time.Parse("2006-01-02", strings.TrimSpace(body.StartsOn))
	if err != nil {
		if draft.Type == models.ActionOneTime {
			return store.ActionDraft{}, huma.Error422UnprocessableEntity("a one-time action needs a date")
		}
		return store.ActionDraft{}, huma.Error422UnprocessableEntity("a recurrent action needs a date to start from")
	}
	if clock := strings.TrimSpace(body.StartsTime); clock != "" {
		hour, minute, err := parseClock(clock)
		if err != nil {
			return store.ActionDraft{}, huma.Error422UnprocessableEntity("that is not a time of day")
		}
		when = when.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
	}
	draft.StartsAt = &when
	draft.RecurrenceNote = note

	if draft.Type == models.ActionRecurrent {
		rule, err := ruleFromForm(body)
		if err != nil {
			return store.ActionDraft{}, huma.Error422UnprocessableEntity(err.Error())
		}
		// Written, then read straight back. The rule is stored as text and
		// evaluated by whatever reads it later, so the moment to find out it
		// is not readable is now — not on the page that was going to tell
		// somebody when to turn up.
		draft.RecurrenceRule = rule.String()
		if _, err := recur.Parse(draft.RecurrenceRule); err != nil {
			log.Error().Err(err).Str("rule", draft.RecurrenceRule).
				Msg("assembled a recurrence rule that cannot be read back")
			return store.ActionDraft{}, huma.Error500InternalServerError("cannot record that rhythm")
		}

		// The series begins on its own rhythm rather than wherever the date
		// picker was left: a monthly-fourth-Thursday series starting on a
		// Monday is a contradiction the RFC does not define away, and the
		// first occurrence is the honest thing to store.
		if first, ok := recur.Next(rule, when, when.Add(-time.Nanosecond)); ok {
			draft.StartsAt = &first
		}
	}

	// Somewhere of its own, or the group's place. Saying so explicitly is what
	// lets an action be moved back: see ActionBody.Elsewhere.
	switch {
	case body.Elsewhere && (body.Latitude != 0 || body.Longitude != 0):
		draft.Location = a.resolveVenue(ctx, body.Latitude, body.Longitude, body.Zoom,
			trimTo(body.Place, maxNicknameRunes), strings.ToUpper(trimTo(body.Country, 2)))
	case !body.Elsewhere:
		draft.InheritLocation = true
	}
	return draft, nil
}

// parseClock reads "19:00".
func parseClock(value string) (hour, minute int, err error) {
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return 0, 0, err
	}
	return parsed.Hour(), parsed.Minute(), nil
}

// ruleFromForm turns the form's choices into a rule.
//
// The form offers choices; this assembles them. No field anywhere accepts an
// RRULE directly — one that did would be a field somebody could put anything
// into, and "anything" here means a rhythm nothing can evaluate on a page that
// tells people when to turn up.
func ruleFromForm(body ActionBody) (recur.Rule, error) {
	rule := recur.Rule{Interval: body.Interval}
	if rule.Interval < 1 {
		rule.Interval = 1
	}

	switch body.Repeat {
	case "weekly":
		rule.Frequency = recur.Weekly
		for _, day := range body.Weekdays {
			if day < 0 || day > 6 {
				return recur.Rule{}, fmt.Errorf("that is not a day of the week")
			}
			rule.Weekdays = append(rule.Weekdays, time.Weekday(day))
		}
		if len(rule.Weekdays) == 0 {
			return recur.Rule{}, fmt.Errorf("a weekly rhythm needs at least one day")
		}

	case "monthly", "yearly":
		rule.Frequency = recur.Monthly
		rule.Interval = 1

		if body.Repeat == "yearly" {
			rule.Frequency = recur.Yearly
			if body.Month < 1 || body.Month > 12 {
				return recur.Rule{}, fmt.Errorf("a yearly rhythm needs a month")
			}
			rule.Month = body.Month
		}

		// Monthly and yearly place a day inside a month the same way; only
		// the month is extra. Sharing the branch is what keeps the two from
		// drifting into accepting different things.
		switch body.Monthly {
		case "day":
			if body.Day < 1 || body.Day > 31 {
				return recur.Rule{}, fmt.Errorf("that is not a day of the month")
			}
			rule.Day = body.Day
		default:
			if body.Weekday < 0 || body.Weekday > 6 {
				return recur.Rule{}, fmt.Errorf("that is not a day of the week")
			}
			if (body.Week < 1 || body.Week > 4) && body.Week != recur.Last {
				return recur.Rule{}, fmt.Errorf("a rhythm falls on the first to " +
					"fourth, or the last, of a weekday")
			}
			rule.Week, rule.Weekday = body.Week, time.Weekday(body.Weekday)
		}

	default:
		return recur.Rule{}, fmt.Errorf("a rhythm repeats weekly, monthly or yearly")
	}
	return rule, nil
}

func (a *API) createAction(ctx context.Context, in *NewActionInput) (*ActionListOutput, error) {
	if _, err := a.groupHost(ctx, in.ID); err != nil {
		return nil, err
	}

	draft, err := a.draft(ctx, in.Body)
	if err != nil {
		return nil, err
	}

	action, err := a.store.CreateAction(ctx, in.ID, draft)
	if errors.Is(err, store.ErrGroupNotFound) {
		return nil, huma.Error404NotFound("no such group")
	}
	if err != nil {
		log.Error().Err(err).Str("group", in.ID).Msg("cannot record an action")
		return nil, huma.Error500InternalServerError("cannot record the action")
	}

	log.Info().Str("group", in.ID).Str("action", action.ID).
		Msg("action announced and queued for assessment")
	return a.actionList(ctx, in.ID)
}

func (a *API) editAction(ctx context.Context, in *EditActionInput) (*ActionListOutput, error) {
	if _, err := a.groupHost(ctx, in.ID); err != nil {
		return nil, err
	}

	draft, err := a.draft(ctx, in.Body)
	if err != nil {
		return nil, err
	}

	if _, err := a.store.UpdateAction(ctx, in.ID, in.Action, draft); err != nil {
		if errors.Is(err, store.ErrActionNotFound) {
			return nil, huma.Error404NotFound("no such action")
		}
		log.Error().Err(err).Str("action", in.Action).Msg("cannot edit an action")
		return nil, huma.Error500InternalServerError("cannot save the action")
	}

	log.Info().Str("group", in.ID).Str("action", in.Action).
		Msg("action edited and re-queued for assessment")
	return a.actionList(ctx, in.ID)
}

func (a *API) deleteAction(ctx context.Context, in *ActionPathInput) (*ActionListOutput, error) {
	if _, err := a.groupHost(ctx, in.ID); err != nil {
		return nil, err
	}

	if err := a.store.DeleteAction(ctx, in.ID, in.Action); err != nil {
		if errors.Is(err, store.ErrActionNotFound) {
			return nil, huma.Error404NotFound("no such action")
		}
		log.Error().Err(err).Str("action", in.Action).Msg("cannot delete an action")
		return nil, huma.Error500InternalServerError("cannot delete the action")
	}

	log.Info().Str("group", in.ID).Str("action", in.Action).Msg("action called off")
	return a.actionList(ctx, in.ID)
}

// ActionPathInput names one of a group's actions.
type ActionPathInput struct {
	ID     string `path:"id"`
	Action string `path:"action"`
}

func (a *API) confirmAction(ctx context.Context, in *ActionPathInput) (*ActionListOutput, error) {
	if _, err := a.groupHost(ctx, in.ID); err != nil {
		return nil, err
	}

	if err := a.store.ConfirmAction(ctx, in.ID, in.Action); err != nil {
		if errors.Is(err, store.ErrActionNotFound) {
			return nil, huma.Error404NotFound("no such action")
		}
		log.Error().Err(err).Str("action", in.Action).Msg("cannot confirm an action")
		return nil, huma.Error500InternalServerError("cannot confirm the action")
	}

	log.Info().Str("group", in.ID).Str("action", in.Action).Msg("recurrent action confirmed")
	return a.actionList(ctx, in.ID)
}
