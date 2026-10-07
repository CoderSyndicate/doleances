package frontend

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/apiclient"
)

// maxFormBytes bounds a submission at the edge. It is well above the longest
// life anybody is going to type and well below what makes the service a
// convenient place to post a file.
const maxFormBytes = 1 << 20 // 1 MiB

// receiptPage is what a contributor sees once, immediately after writing.
//
// The token appears here and nowhere else — not in an email, not in the
// database, not in a log. If they lose it, nobody can reissue it, and the page
// has to say so plainly rather than let them assume it can be recovered.
type receiptPage struct {
	page

	ID        string
	Permalink string
	Token     string
}

// submitPage is the form re-rendered with what went wrong and what was typed,
// because a rejected submission must never cost somebody their text.
type submitPage struct {
	doleancePage

	Error string
	Draft apiclient.MessageSubmission
}

func (s *site) registerSubmitRoutes(mux *http.ServeMux) {
	mux.Handle("POST /doleance", s.localization.Middleware(http.HandlerFunc(s.submitDoleance)))
	mux.Handle("GET /doleance/{id}", s.localization.Middleware(http.HandlerFunc(s.viewDoleance)))
	mux.Handle("POST /doleance/{id}/like", s.localization.Middleware(http.HandlerFunc(s.likeDoleance)))
	mux.Handle("GET /doleance/{id}/report", s.localization.Middleware(http.HandlerFunc(s.confirmReport)))
	mux.Handle("POST /doleance/{id}/report", s.localization.Middleware(http.HandlerFunc(s.reportDoleance)))
}

// likeDoleance records that this happened to somebody else too.
//
// A form post and a redirect back, so it works with JavaScript switched off.
// Nothing about who pressed it is asked for or kept — see
// models.Message.Likes — and a failure is swallowed rather than shown: the
// reader came to read, and a page of apology because a counter did not move
// would be the wrong thing to interrupt them with.
func (s *site) likeDoleance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if _, err := s.backend.LikeMessage(r.Context(), id); err != nil {
		log.Debug().Err(err).Str("message", id).Msg("cannot record a like")
	}

	s.sendBack(w, r, "/doleance/"+pathEscape(id))
}

// sendBack returns somebody to the card they pressed.
//
// Shared by every control that is a form post on a card — "me too", keep,
// release — because the three had the same twelve lines and the interesting
// half of them is a security property: Referer is followed **only** when it
// points at this site, or every card would be a way to turn this origin into
// somebody else's landing page.
//
// The fallback is the text's own page, which is where somebody who arrived
// with no Referer at all should end up rather than on the front page.
func (s *site) sendBack(w http.ResponseWriter, r *http.Request, fallback string) {
	back := fallback
	if referer := r.Referer(); referer != "" {
		if parsed, err := url.Parse(referer); err == nil && parsed.Host == r.Host {
			back = parsed.RequestURI()
		}
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// pathEscape is url.PathEscape, named here so the call sites read as what they
// are doing: an identifier from a request put back into a path.
func pathEscape(id string) string { return url.PathEscape(id) }

// reportPage asks whether somebody meant to press the flag.
type reportPage struct {
	page

	// Message is the doléance they are about to take off the register, shown
	// whole. The decision is about these words, and a page that asked for it
	// without showing them would be asking somebody to confirm a click rather
	// than a judgement.
	Message messageCard
}

// confirmReport is the page the flag leads to.
//
// The control it comes from is an unlabelled icon beside three others, which
// is easy to press without meaning to — and what it does, taking somebody's
// published doléance off the register, is the last thing on that card that
// should happen by accident. So the flag is a link and nothing has happened
// yet when this renders: the text is still published, and the only thing that
// changes anything is the form below it.
//
// It refuses what the API would refuse, so somebody does not confirm an action
// that was never going to work. A doléance that is not published, is already
// in the queue, or has already been read by a curator answers as not found —
// the same answer for all of them, for the same reason the API gives one.
func (s *site) confirmReport(w http.ResponseWriter, r *http.Request) {
	message, err := s.reader(r).GetMessage(r.Context(), r.PathValue("id"))
	if err != nil || message.Status != statusAccepted || message.Verified {
		s.renderNotFound(w, r)
		return
	}

	data := reportPage{page: s.newPage(r, "report.title")}
	data.Message = messageCard{
		ID:             message.ID,
		Nickname:       message.Nickname,
		Place:          message.Place,
		Date:           message.CreatedAt.Format("2 January 2006"),
		Text:           message.Text,
		Subjects:       message.Subjects,
		Likes:          message.Likes,
		Verified:       message.Verified,
		Kept:           message.Kept,
		Full:           true,
		AnonymousLabel: data.T("card.anonymous"),
	}

	s.renderer.Render(w, http.StatusOK, "report", data)
}

// reportDoleance asks for a person to look at a doléance.
//
// Unlike the "me too" beside it, a failure here is **not** swallowed. A like
// that did not register costs a number nobody ranks by; a report that did not
// register leaves somebody believing they have raised an objection that nobody
// will ever see. So the two outcomes are told apart, and the only two things
// that can go wrong both land on the doléance's own page: either it says a
// person will look, or it says nothing happened.
//
// It does not say *why* nothing happened. The backend answers one 404 for a
// doléance that is not published, is already in the queue, has already been
// read by a curator, or never existed — and repeating that distinction here
// would undo the reason it is one answer.
func (s *site) reportDoleance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	where := "/doleance/" + pathEscape(id)

	if err := s.backend.ReportMessage(r.Context(), id); err != nil {
		log.Debug().Err(err).Str("message", id).Msg("cannot record a report")
		http.Redirect(w, r, where, http.StatusSeeOther)
		return
	}

	// To the doléance rather than back to the listing: the card they pressed
	// has just left the register, so returning them to where they were would
	// answer a deliberate act with a page where nothing visibly happened.
	http.Redirect(w, r, where+"?reported=1", http.StatusSeeOther)
}

func (s *site) submitDoleance(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		s.rejectSubmission(w, r, apiclient.MessageSubmission{}, "write.error_too_long")
		return
	}

	submission := apiclient.MessageSubmission{
		Text:      r.PostFormValue("text"),
		Nickname:  r.PostFormValue("nickname"),
		Activity:  r.PostFormValue("activity"),
		ExpiresOn: r.PostFormValue("expires_on"),
		Agreement: r.PostFormValue("agreement") != "",
	}
	if year, err := strconv.Atoi(r.PostFormValue("birth_year")); err == nil {
		submission.BirthYear = year
	}
	if lat, err := strconv.ParseFloat(r.PostFormValue("latitude"), 64); err == nil {
		submission.Latitude = lat
	}
	if lon, err := strconv.ParseFloat(r.PostFormValue("longitude"), 64); err == nil {
		submission.Longitude = lon
	}
	submission.Place = r.PostFormValue("place")
	submission.Country = r.PostFormValue("country")
	if zoom, err := strconv.Atoi(r.PostFormValue("zoom")); err == nil {
		submission.Zoom = zoom
	}

	if strings.TrimSpace(submission.Text) == "" {
		s.rejectSubmission(w, r, submission, "write.error_empty")
		return
	}
	if !submission.Agreement {
		s.rejectSubmission(w, r, submission, "write.error_agreement")
		return
	}

	receipt, err := s.backend.SubmitMessage(r.Context(), submission)
	if err != nil {
		// Whatever went wrong, the person still has their text on screen.
		log.Error().Err(err).Msg("cannot submit a doléance")
		s.rejectSubmission(w, r, submission, "write.error_backend")
		return
	}

	data := receiptPage{page: s.newPage(r, "receipt.title")}
	data.ID = receipt.ID
	data.Permalink = "/doleance/" + receipt.ID
	data.Token = receipt.Token

	s.renderer.Render(w, http.StatusOK, "receipt", data)
}

// rejectSubmission re-renders the form with the text intact.
//
// It answers 422 rather than redirecting: a redirect would lose the draft, and
// somebody who has just written the story of their life does not get a second
// chance to type it.
func (s *site) rejectSubmission(w http.ResponseWriter, r *http.Request, draft apiclient.MessageSubmission, errorKey string) {
	data := submitPage{doleancePage: s.doleanceData(r)}
	data.Draft = draft
	data.Error = data.T(errorKey)

	s.renderer.Render(w, http.StatusUnprocessableEntity, "doleance", data)
}

// viewDoleance is the permalink.
//
// It resolves before a curator has seen the message, because the contributor
// holding the link needs to see what they submitted and how it is getting on.
// Identifiers are random, so the link is only held by whoever was given it.
func (s *site) viewDoleance(w http.ResponseWriter, r *http.Request) {
	message, err := s.reader(r).GetMessage(r.Context(), r.PathValue("id"))
	if err != nil {
		log.Debug().Err(err).Str("message", r.PathValue("id")).Msg("permalink not resolved")
		s.renderNotFound(w, r)
		return
	}

	data := permalinkPage{page: s.newPage(r, "permalink.title")}
	// The doléance entire, and marked as the destination rather than a way to
	// one: this page is what every card links to, so a card here would offer
	// a link to itself.
	data.Message = messageCard{
		ID:             message.ID,
		Nickname:       message.Nickname,
		Place:          message.Place,
		Date:           message.CreatedAt.Format("2 January 2006"),
		Text:           message.Text,
		Subjects:       message.Subjects,
		Likes:          message.Likes,
		Kept:           message.Kept,
		Verified:       message.Verified,
		Full:           true,
		AnonymousLabel: data.T("card.anonymous"),
	}
	data.Pending = message.Status != statusAccepted
	// Set by the redirect a report makes, so the reader who pressed it is
	// answered. Anybody can put it in the query string by hand and all they
	// get is a line of explanation, which costs nothing.
	data.Reported = r.URL.Query().Has("reported")

	s.renderer.Render(w, http.StatusOK, "permalink", data)
}

// statusAccepted is the one state that means published.
const statusAccepted = "accepted"

// permalinkPage shows one doléance at its own address.
type permalinkPage struct {
	page

	Message messageCard

	// Pending says the doléance is not yet in the public register, so the page
	// can explain that rather than let its author think it was lost.
	Pending bool

	// Reported is set when a reader has just asked for a person to look, and
	// is what they are shown instead of the note written for the author.
	//
	// The card they pressed has left the register, so sending them back to the
	// listing would answer a deliberate act with a page where nothing had
	// visibly happened. Here they see the text they objected to and a line
	// saying what comes next, which is the whole of what this control promises.
	Reported bool
}

func (s *site) renderNotFound(w http.ResponseWriter, r *http.Request) {
	data := s.newPage(r, "notfound.title")
	s.renderer.Render(w, http.StatusNotFound, "notfound", notFoundPage{page: data})
}

type notFoundPage struct{ page }
