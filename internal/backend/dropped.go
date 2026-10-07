package backend

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/store"
)

// registerDroppedRoutes publishes what this register refused.
//
// # Why a refusal is published at all
//
// Dropping is the one decision no human takes part in, so it is the one that
// can be silently wrong. The console has always held a sample for curators;
// this is the same sample shown to everybody, and it exists because "trust us,
// we only refuse what deserves it" is exactly the kind of claim this project
// says elsewhere nobody should have to take on faith.
//
// A reader who thinks a refusal was wrong can say so, and what they press
// moves that submission to the top of the curator's page. It does not
// overturn anything: a refusal reversed by a show of hands would be a
// register where the largest group decides what may be written.
func (a *API) registerDroppedRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-dropped",
		Method:      http.MethodGet,
		Path:        "/v1/dropped",
		Summary:     "The submissions this register refused, and why",
		Tags:        []string{"Register"},
	}, a.listDropped)

	huma.Register(api, huma.Operation{
		OperationID: "plead-for-dropped",
		Method:      http.MethodPost,
		Path:        "/v1/dropped/{id}/plea",
		Summary:     "Say this refusal was wrong",
		Tags:        []string{"Register"},
	}, a.pleadForDropped)
}

// DroppedItem is one refused submission as anybody may read it.
//
// # What is deliberately not on it
//
// The model's own sentence and its score are both absent, and both are on the
// console's version. Publishing them would tell whoever is flooding this
// register exactly which of their variants failed and by how much, which
// turns a page about the filter into a tuning instrument for getting past it
// — the same reason the submission receipt says `pending` whatever happened.
//
// What is published is the category, which says enough for a reader to
// disagree and nothing a flooder can steer by.
type DroppedItem struct {
	ID string `json:"id"`

	// Reason is the category: "duplicate", "payload", "threat", "contact",
	// "identifies", or empty when the score decided alone. A word rather than
	// a sentence, and the page turns it into one in the reader's language.
	Reason string `json:"reason,omitempty"`

	// Text is what was written, and is empty when the category withholds it.
	// See models.WithholdsText: two of these categories are refusals about
	// publication, and reproducing the words here would perform the harm the
	// refusal prevented.
	Text     string `json:"text,omitempty"`
	Language string `json:"language,omitempty"`

	// Withheld says the words are not being shown at all, and Hidden says
	// they are shown only to a reader who presses for them. They are
	// different states and a page that conflated them would be unable to
	// explain either.
	Withheld bool `json:"withheld"`
	Hidden   bool `json:"hidden"`

	// Pleas is how many readers have said this refusal was wrong.
	Pleas int `json:"pleas"`

	DroppedAt time.Time `json:"dropped_at"`
}

// DroppedOutput is the sample and how long anything stays in it.
type DroppedOutput struct {
	Body struct {
		Items []DroppedItem `json:"items"`

		// Held is how many are being kept, which may exceed what is listed.
		Held int64 `json:"held"`

		// RetentionHours is how long a refusal stays readable. Published
		// because it is the measure of the promise: a sample nobody can reach
		// in time is not a check on anything.
		RetentionHours int `json:"retention_hours"`

		// Page and PerPage, so a reader can walk the whole sample. The page
		// before this one was the first twenty-five and nothing said so.
		Page    int `json:"page"`
		PerPage int `json:"per_page"`
	}
}

// DroppedInput selects a page of the listing.
type DroppedInput struct {
	Page    int `query:"page" minimum:"1" default:"1"`
	PerPage int `query:"per_page" minimum:"1" maximum:"100" default:"25"`
}

func (a *API) listDropped(ctx context.Context, in *DroppedInput) (*DroppedOutput, error) {
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

	messages, err := a.store.ListDropped(ctx, retention, perPage, (page-1)*perPage)
	if err != nil {
		log.Error().Err(err).Msg("cannot list the dropped submissions")
		return nil, huma.Error500InternalServerError("cannot read the dropped submissions")
	}
	held, err := a.store.CountSpam(ctx, retention)
	if err != nil {
		log.Error().Err(err).Msg("cannot count the dropped submissions")
		return nil, huma.Error500InternalServerError("cannot read the dropped submissions")
	}

	out := &DroppedOutput{}
	out.Body.Held = held
	out.Body.RetentionHours = settings.SpamRetentionHours
	out.Body.Page, out.Body.PerPage = page, perPage
	out.Body.Items = make([]DroppedItem, 0, len(messages))
	for _, message := range messages {
		item := DroppedItem{
			ID:        message.ID,
			Reason:    message.DropReason,
			Language:  message.Language,
			Pleas:     message.Pleas,
			DroppedAt: message.UpdatedAt,
			Withheld:  models.WithholdsText(message.DropReason),
			Hidden:    models.HidesTextUntilAsked(message.DropReason),
		}
		// The withholding happens here, on the way out, rather than in the
		// template that draws the page. A page can be rewritten by somebody
		// who does not know the rule; an API that never sent the bytes cannot
		// leak them however the page is rebuilt.
		if !item.Withheld {
			item.Text = message.Text
		}
		out.Body.Items = append(out.Body.Items, item)
	}
	return out, nil
}

// PleaInput names the refusal being objected to.
type PleaInput struct {
	ID string `path:"id"`
}

// PleaOutput is the new count, so the page can show it without re-reading.
type PleaOutput struct {
	Body struct {
		Pleas int `json:"pleas"`
	}
}

func (a *API) pleadForDropped(ctx context.Context, in *PleaInput) (*PleaOutput, error) {
	settings, err := a.store.CurationSettings(ctx)
	if err != nil {
		log.Error().Err(err).Msg("cannot read curation settings")
		return nil, huma.Error500InternalServerError("cannot read the curation settings")
	}
	retention := time.Duration(settings.SpamRetentionHours) * time.Hour

	pleas, err := a.store.PleadForMessage(ctx, in.ID, retention)
	if errors.Is(err, store.ErrMessageNotFound) {
		// Purged, never dropped, or published: all the same answer, and
		// deliberately. A plea that reported which of those it was would let
		// somebody discover whether a particular text had been refused by
		// pleading for identifiers.
		return nil, huma.Error404NotFound("no refusal to plead against")
	}
	if err != nil {
		log.Error().Err(err).Str("message", in.ID).Msg("cannot record a plea")
		return nil, huma.Error500InternalServerError("cannot record that")
	}

	out := &PleaOutput{}
	out.Body.Pleas = pleas
	return out, nil
}
