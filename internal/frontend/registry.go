package frontend

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/CoderSyndicate/doleances/internal/apiclient"
)

// registerRegistryRoutes adds the endpoint the register page's map calls as
// the reader pans and zooms.
//
// It exists because the page has to change without reloading: a full page load
// on every map movement would throw away the map's own state, and the reader
// would be fighting the interface rather than reading the register.
func (s *site) registerRegistryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/messages", s.messagesJSON)
}

// messageJSON is one doléance as the register page's script renders it.
//
// The date is formatted here rather than in the browser: the page is
// translated server-side and the script has no locale of its own, so letting
// JavaScript format it would produce a date in the wrong language.
type messageJSON struct {
	ID       string   `json:"id"`
	Nickname string   `json:"nickname,omitempty"`
	Place    string   `json:"place,omitempty"`
	Date     string   `json:"date"`
	Subjects []string `json:"subjects,omitempty"`

	// The card-sized version and whether it was cut, both off the row. The
	// whole text is deliberately absent: this list draws cards, and a page
	// that only shows excerpts has no business holding the rest.
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`

	Likes int `json:"likes"`

	// Kept says whether the signed-in reader has this in their own list.
	// Always sent, never omitted: a boolean that disappears when false makes
	// "no" and "not asked" the same answer. See backend.MessageItem.
	Kept bool `json:"kept"`

	// Verified says a human read this doléance and let it stand, so the card
	// this script draws offers the same two states as the one the server
	// rendered. Always sent, never omitted, for the same reason.
	Verified bool `json:"verified"`

	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
}

// voiceJSON is one passage from 1789 as the register's script renders it.
//
// It carries a point, which is new: the corpus is placed now, so a passage
// sits on the map beside the doléances written where it was written — drawn
// in its own colour, because it is not one of them.
type voiceJSON struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Text          string `json:"text"`
	Period        string `json:"period,omitempty"`
	Region        string `json:"region,omitempty"`
	DocumentTitle string `json:"document_title,omitempty"`
	Source        string `json:"source,omitempty"`

	// TranslatedFrom is set when the reader is not reading the writer's own
	// words, and the card says so.
	TranslatedFrom string `json:"translated_from,omitempty"`

	// Placeholder marks seeded example content, so it can never be mistaken
	// for archival material.
	Placeholder bool `json:"placeholder"`

	Likes int  `json:"likes"`
	Kept  bool `json:"kept"`

	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
}

// entryJSON is one row of the register, which is a doléance or a passage.
//
// Kind is always sent and is what the script switches on. One list rather
// than two, for the reason the rendered page uses one: the register's claim
// is that the two are the same act, and a payload with the past in its own
// array would be a second shape to keep in step for no gain.
type entryJSON struct {
	Kind    string       `json:"kind"`
	Message *messageJSON `json:"message,omitempty"`
	Voice   *voiceJSON   `json:"voice,omitempty"`
}

const (
	entryDoleance = "doleance"
	entryVoice    = "voice"
)

func (s *site) messagesJSON(w http.ResponseWriter, r *http.Request) {
	// Shuffled unless the reader asked to page, which is the default because
	// newest-first buries everything older behind a page nobody turns.
	query := apiclient.MessageQuery{
		Limit:    registerMessages,
		Subjects: r.URL.Query()["subject"],
		Shuffled: r.URL.Query().Get("order") != "recent",
	}
	if !query.Shuffled {
		if offset, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && offset > 0 {
			query.Offset = offset
		}
	}

	// One parameter, "north,south,east,west", the same spelling the backend
	// takes. Four separate edges were two contracts for one idea, and the
	// mismatch was invisible: the page filtered nothing and looked like it had.
	if edges, ok := parseEdges(r.URL.Query().Get("bounds")); ok {
		query.North, query.South = edges[0], edges[1]
		query.East, query.West = edges[2], edges[3]
		query.HasBounds = true
	}

	// The same composition the rendered page uses — the reserved slots for
	// doléances with no point, and the passages mixed through — so panning the
	// map does not quietly produce a different kind of register from the one
	// that was served.
	listing := s.registerEntries(r, s.newPage(r, "register.title"), query)

	entries := make([]entryJSON, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		switch {
		case entry.Message != nil:
			card := entry.Message
			entries = append(entries, entryJSON{Kind: entryDoleance, Message: &messageJSON{
				ID:        card.ID,
				Nickname:  card.Nickname,
				Place:     card.Place,
				Date:      card.Date,
				Text:      card.Text,
				Truncated: card.Truncated,
				Likes:     card.Likes,
				Kept:      card.Kept,
				Verified:  card.Verified,
				Subjects:  card.Subjects,
				Latitude:  card.Latitude,
				Longitude: card.Longitude,
			}})
		case entry.Historical != nil:
			card := entry.Historical
			entries = append(entries, entryJSON{Kind: entryVoice, Voice: &voiceJSON{
				ID:             card.ID,
				Title:          card.Title,
				Text:           card.Text,
				Period:         card.Period,
				Region:         card.Region,
				DocumentTitle:  card.DocumentTitle,
				Source:         card.Source,
				TranslatedFrom: card.TranslatedFrom,
				Placeholder:    card.Placeholder,
				Likes:          card.Likes,
				Kept:           card.Kept,
				Latitude:       card.Latitude,
				Longitude:      card.Longitude,
			}})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		// Doléances among them. The count under the map reports this rather
		// than the number of cards, and so does whether the page says nothing
		// was written here: a list filled with passages is still a viewport
		// with nothing written in it.
		"messages": listing.Messages,
		// How many of them sit on a point, which is what the map draws. The
		// count under it reports entries, not markers: a reader is told what
		// is on the page, and some of it is deliberately not on the map.
		"placed": listing.Placed,
	})
}

// parseEdges reads "north,south,east,west". A malformed value is treated as
// no filter rather than as an error: the reader asked for the register, and
// showing all of it beats showing them a failure.
func parseEdges(raw string) ([4]float64, bool) {
	var edges [4]float64
	if raw == "" {
		return edges, false
	}

	parts := strings.Split(raw, ",")
	if len(parts) != 4 {
		return edges, false
	}
	for i, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return edges, false
		}
		edges[i] = value
	}
	return edges, true
}

// writeJSON answers with a JSON body.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body) //nolint:errcheck
}
