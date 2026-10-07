package frontend

import (
	"net/http"

	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/apiclient"
)

// registerBookmarkRoutes declares the reader's own list and the two controls
// that change it.
//
// Outside the groups switch, like the account pages themselves: keeping a
// doléance has nothing to do with whether this deployment offers local groups.
//
// Two paths rather than one toggle, mirroring join and leave on a group. A
// toggle would mean a stale page undoing what somebody just did — the button
// says "keep", so it posts a keep, whatever has happened since.
func (s *site) registerBookmarkRoutes(mux *http.ServeMux) {
	localized := func(h http.HandlerFunc) http.Handler {
		return s.localization.Middleware(h)
	}

	mux.Handle("GET /account/bookmarks", localized(s.bookmarks))

	mux.Handle("POST /doleance/{id}/keep", localized(s.keepDoleance))
	mux.Handle("POST /doleance/{id}/release", localized(s.releaseDoleance))
	mux.Handle("POST /voices/{id}/keep", localized(s.keepVoice))
	mux.Handle("POST /voices/{id}/release", localized(s.releaseVoice))
}

// bookmarksPage is what one reader kept.
type bookmarksPage struct {
	page

	// Kept is the list, most recently kept first. Each entry carries either a
	// doléance's card or a passage's, never both.
	Kept []keptCard
}

// keptCard is one entry of the list.
//
// The two card shapes rather than one, because they are different objects and
// a common shape would show neither properly — a doléance has a place and a
// date, a passage has a document and a source.
type keptCard struct {
	When string

	Message    *messageCard
	Historical *historicalCard
}

func (s *site) bookmarks(w http.ResponseWriter, r *http.Request) {
	if !signedIn(r) {
		s.toSignin(w, r, "/account/bookmarks")
		return
	}

	kept, err := s.reader(r).Bookmarks(r.Context())
	if err != nil {
		log.Debug().Err(err).Msg("cannot read the kept texts; treating the session as gone")
		s.clearSession(w)
		s.toSignin(w, r, "/account/bookmarks")
		return
	}

	data := bookmarksPage{page: s.newPage(r, "bookmarks.title")}
	for _, entry := range kept {
		card := keptCard{When: entry.When()}
		switch {
		case entry.Message != nil:
			message := entry.Message
			card.Message = &messageCard{
				ID:        message.ID,
				Nickname:  message.Nickname,
				Place:     message.Place,
				Date:      message.CreatedAt.Format("2 January 2006"),
				Text:      message.Excerpt,
				Truncated: message.Truncated,
				Subjects:  message.Subjects,
				Likes:     message.Likes,
				// Every entry of this list is kept by definition, so the card
				// offers taking it back rather than keeping it again.
				Kept:           true,
				Verified:       message.Verified,
				AnonymousLabel: data.T("card.anonymous"),
			}
		case entry.Historical != nil:
			passage := toCard(*entry.Historical, data.Lang, data.T("card.placeholder"))
			passage.Kept = true
			card.Historical = &passage
		default:
			// An entry naming neither is one this page cannot draw, and the
			// backend already removes what it cannot resolve.
			continue
		}
		data.Kept = append(data.Kept, card)
	}

	s.renderer.Render(w, http.StatusOK, "bookmarks", data)
}

func (s *site) keepDoleance(w http.ResponseWriter, r *http.Request) {
	s.changeList(w, r, apiclient.KeptMessage, true, "/doleance/")
}

func (s *site) releaseDoleance(w http.ResponseWriter, r *http.Request) {
	s.changeList(w, r, apiclient.KeptMessage, false, "/doleance/")
}

func (s *site) keepVoice(w http.ResponseWriter, r *http.Request) {
	s.changeList(w, r, apiclient.KeptHistorical, true, "/voices/")
}

func (s *site) releaseVoice(w http.ResponseWriter, r *http.Request) {
	s.changeList(w, r, apiclient.KeptHistorical, false, "/voices/")
}

// changeList puts a text in the reader's own list, or takes it out, and sends
// them back to the card they pressed.
//
// A failure is swallowed, exactly as it is for a "me too": the reader came to
// read, and a page of apology because a bookmark did not save would be the
// wrong thing to interrupt them with. The control they see next is drawn from
// the list as it really is, so an unsaved press shows as an unchanged button
// rather than as a lie.
func (s *site) changeList(w http.ResponseWriter, r *http.Request, kind string, keep bool, home string) {
	id := r.PathValue("id")

	var err error
	if keep {
		err = s.reader(r).Keep(r.Context(), kind, id)
	} else {
		err = s.reader(r).Release(r.Context(), kind, id)
	}
	if err != nil {
		log.Debug().Err(err).Str("kind", kind).Str("text", id).
			Bool("keeping", keep).Msg("cannot change the kept list")
	}

	s.sendBack(w, r, home+pathEscape(id))
}
