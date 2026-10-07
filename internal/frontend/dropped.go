package frontend

import (
	"net/http"
	"strconv"

	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// registerDroppedRoutes publishes what this register refused, and the one
// control a reader has over it.
func (s *site) registerDroppedRoutes(mux *http.ServeMux) {
	localized := func(h http.HandlerFunc) http.Handler {
		return s.localization.Middleware(h)
	}
	mux.Handle("GET /dropped", localized(s.dropped))
	mux.Handle("POST /dropped/{id}/plea", localized(s.plead))
}

// droppedCard is one refusal as the page draws it.
type droppedCard struct {
	ID string

	// Reason is the sentence this page shows, already in the reader's
	// language. The API sends a category word; turning it into prose happens
	// here, because the words a register uses about a refusal are part of
	// what the refusal means.
	Reason string

	// Text is what was written, empty when the category withholds it.
	Text string

	// Withheld says the words are not shown at all, and WithheldWhy says why
	// in the reader's language. Hidden says they are shown behind a press.
	Withheld    bool
	WithheldWhy string
	Hidden      bool

	Pleas int
	Date  string
}

type droppedPage struct {
	page
	Cards []droppedCard

	// Held is how many refusals are being kept, and Hours how long each stays
	// readable. Both are on the page because they are the measure of the
	// promise: a sample nobody can reach in time checks nothing.
	Held  int64
	Hours int

	// Page is which page of the sample this is, and HasNext whether there is
	// another after it. Paged because the sample outgrows a screen: showing
	// the first twenty-five and saying nothing would make the page a claim
	// about the filter that the page itself could not support.
	Page    int
	PerPage int
	HasNext bool
}

// PrevPage and NextPage are the numbers the arrows point at.
func (p droppedPage) PrevPage() int { return p.Page - 1 }
func (p droppedPage) NextPage() int { return p.Page + 1 }

// HasPrev reports whether there is a page before this one.
func (p droppedPage) HasPrev() bool { return p.Page > 1 }

func (s *site) dropped(w http.ResponseWriter, r *http.Request) {
	data := droppedPage{page: s.newPage(r, "dropped.title")}

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	data.Page = page

	sample, err := s.reader(r).ListDropped(r.Context(), page)
	if err != nil {
		// A page saying the sample cannot be read is better than an error
		// page: the explanation above it is still worth serving.
		log.Warn().Err(err).Msg("cannot read the dropped sample")
		s.renderer.Render(w, http.StatusOK, "dropped", data)
		return
	}

	data.Held = sample.Held
	data.Hours = sample.RetentionHours
	data.PerPage = sample.PerPage
	// A full page is the only evidence of a next one that does not cost a
	// count over the whole sample on every request — the same bargain the
	// register's own paging makes. One empty page at the exact multiple is a
	// cheaper mistake than counting every time.
	data.HasNext = sample.PerPage > 0 && len(sample.Items) == sample.PerPage
	data.Cards = make([]droppedCard, 0, len(sample.Items))
	for _, item := range sample.Items {
		data.Cards = append(data.Cards, droppedCard{
			ID:          item.ID,
			Reason:      data.T(reasonKey(item.Reason)),
			Text:        item.Text,
			Withheld:    item.Withheld,
			WithheldWhy: data.T(withheldKey(item.Reason)),
			Hidden:      item.Hidden,
			Pleas:       item.Pleas,
			Date:        item.DroppedAt.Format("2 January 2006"),
		})
	}

	s.renderer.Render(w, http.StatusOK, "dropped", data)
}

// reasonKey names the sentence a category gets.
//
// A category the reader's catalogue does not know falls back to the plain
// "a curator decided", which is true of every refusal here in the sense that
// matters: somebody is answerable for it.
func reasonKey(reason string) string {
	switch reason {
	case models.DropDuplicate:
		return "dropped.reason_duplicate"
	case models.DropPayload:
		return "dropped.reason_payload"
	case models.DropThreat:
		return "dropped.reason_threat"
	case models.DropContact:
		return "dropped.reason_contact"
	case models.DropIdentifies:
		return "dropped.reason_identifies"
	default:
		return "dropped.reason_score"
	}
}

// withheldKey says why the words are not here.
func withheldKey(reason string) string {
	switch reason {
	case models.DropPayload:
		return "dropped.withheld_payload"
	default:
		// contact and identifies: somebody who is not the author is named in
		// it, and they did not ask to be on this page either.
		return "dropped.withheld_person"
	}
}

// plead records a reader saying a refusal was wrong.
//
// A form post rather than a fetch, so it works with no script at all — the
// same shape as the "me too" control on a card, and for the same reason: this
// is the one thing a reader can do about a decision nobody human took, and it
// must not depend on JavaScript having loaded.
func (s *site) plead(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.reader(r).PleadForDropped(r.Context(), id); err != nil {
		// Purged, never dropped, or already published: the register answers
		// the same way for all three, so the page does too.
		log.Debug().Err(err).Str("message", id).Msg("a plea could not be recorded")
	}
	s.sendBack(w, r, "/dropped")
}
