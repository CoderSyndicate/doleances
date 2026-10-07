package frontend

import (
	"math/rand"
	"net/http"
	"sort"

	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/apiclient"
	"github.com/CoderSyndicate/doleances/internal/geo"
)

// registerEntry is one row of the register: a doléance, or a passage from
// 1789. Exactly one of the two is set.
//
// A single list rather than two sections, because the register's claim is
// that the two are one act performed twice — and a page with the past filed
// under its own heading says the opposite of that while appearing to agree.
type registerEntry struct {
	Message    *messageCard
	Historical *historicalCard
}

// registerListing is a page of the register, and what the page needs to say
// about itself.
type registerListing struct {
	Entries []registerEntry

	// Messages counts the doléances among them, which is not the same as the
	// number of entries: a page may be mostly passages from 1789, and saying
	// "24 doléances here" over seven of them and seventeen of those would be
	// a plain untruth.
	Messages int

	// Placed counts the doléances on this page that sit on a point, which is
	// what the map draws.
	Placed int
}

// registerEntries builds a page: the doléances the query asks for, the ones
// with no point that keep their slots whatever the map shows, and the
// passages mixed through them.
//
// See mix.go for the rules and why each exists.
func (s *site) registerEntries(r *http.Request, p page, query apiclient.MessageQuery) registerListing {
	reader := s.reader(r)

	// The located ones first, filtered by whatever the reader asked for.
	placed := query
	placed.Limit = messageSlots()
	found, err := reader.FindMessages(r.Context(), placed)
	if err != nil {
		log.Warn().Err(err).Msg("cannot read the register")
		return registerListing{}
	}

	// Then the ones that named a country or a region and nothing finer. They
	// cannot answer a viewport, so they are asked for separately and keep
	// their own slots — which is what retired the "see everything, even
	// without a place" button.
	unplaced := query
	unplaced.Limit = unplacedSlots
	unplaced.Unplaced = true
	unplaced.HasBounds = false
	other, err := reader.FindMessages(r.Context(), unplaced)
	if err != nil {
		// Not fatal: a page of located doléances is still the register.
		log.Warn().Err(err).Msg("cannot read the unplaced doléances")
	}

	messages := fill(found.Messages, other.Messages)
	if query.Shuffled {
		// Both lists arrived shuffled; concatenating them would still put
		// every unplaced doléance at the end, which is a block of them rather
		// than a few among the rest.
		rand.Shuffle(len(messages), func(i, j int) {
			messages[i], messages[j] = messages[j], messages[i]
		})
	} else {
		// Newest first across both, so paging stays an order rather than two.
		sort.SliceStable(messages, func(i, j int) bool {
			return messages[i].CreatedAt.After(messages[j].CreatedAt)
		})
	}

	cards := make([]*messageCard, 0, len(messages))
	placedCount := 0
	for _, message := range messages {
		card := toMessageCard(message, p)
		if message.Latitude != 0 || message.Longitude != 0 {
			placedCount++
		}
		cards = append(cards, &card)
	}

	voices := s.voiceCards(r, p, query, len(cards))
	entries := make([]registerEntry, 0, len(cards)+len(voices))
	entries = append(entries, interleave(asEntries(cards), voiceEntries(voices))...)
	return registerListing{Entries: entries, Messages: len(cards), Placed: placedCount}
}

func asEntries(cards []*messageCard) []registerEntry {
	out := make([]registerEntry, 0, len(cards))
	for _, card := range cards {
		out = append(out, registerEntry{Message: card})
	}
	return out
}

func voiceEntries(cards []*historicalCard) []registerEntry {
	out := make([]registerEntry, 0, len(cards))
	for _, card := range cards {
		out = append(out, registerEntry{Historical: card})
	}
	return out
}

// voiceCards picks the passages for this page.
//
// # Where a passage is not shown
//
// A subject filter removes them all. The classifier assigns subjects to
// doléances and never to the corpus, so a passage matches no subject — and
// showing one to somebody who asked for `transport` would be claiming it was
// about transport, which nothing here knows.
//
// A viewport removes the ones outside it, which it can do honestly now that
// the corpus carries coordinates. A passage the corpus never placed sits on
// no point and is therefore absent whenever the map is narrowed, on the same
// terms as a doléance that named only a country.
func (s *site) voiceCards(r *http.Request, p page, query apiclient.MessageQuery, messages int) []*historicalCard {
	if len(query.Subjects) > 0 {
		return nil
	}

	texts, err := s.reader(r).ListHistorical(r.Context())
	if err != nil {
		log.Warn().Err(err).Msg("cannot load the historical corpus")
		return nil
	}

	box := geo.Box{North: query.North, South: query.South, East: query.East, West: query.West}
	inView := func(text apiclient.HistoricalText) bool {
		if !query.HasBounds {
			return true
		}
		if text.Latitude == 0 && text.Longitude == 0 {
			return false // never placed: sits on no point, like an unplaced doléance
		}
		return box.Contains(text.Latitude, text.Longitude)
	}

	available := make([]*historicalCard, 0, len(texts))
	for _, text := range texts {
		if inView(text) {
			card := toCard(text, p.Lang, p.T("card.placeholder"))
			available = append(available, &card)
		}
	}

	// # Nothing here is not the same as nothing to read
	//
	// A viewport over the Atlantic has no doléances and no passages, and the
	// register answered it with a blank page under a line saying nothing had
	// been written there. The line is true and the blankness is not useful:
	// this is a register, and a reader who has panned somewhere empty should
	// still be given something to read rather than told to go away.
	//
	// So when the viewport yields neither, the corpus is shown from wherever
	// it is. This is the one place a passage appears outside the viewport
	// that selected it, and it does not pretend otherwise — the page still
	// says nothing was written here, and every passage carries its own region
	// on the card.
	if len(available) == 0 && messages == 0 {
		for _, text := range texts {
			card := toCard(text, p.Lang, p.T("card.placeholder"))
			available = append(available, &card)
		}
	}

	// Ordered by this process's own shuffle, so the same passages are not the
	// whole corpus in practice, and so two readers do not meet the identical
	// four on every visit.
	sort.Slice(available, func(i, j int) bool {
		return s.rank(available[i].ID) < s.rank(available[j].ID)
	})

	want := wantedVoices(messages, len(available))
	return available[:want]
}
