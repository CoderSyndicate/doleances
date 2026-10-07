package frontend

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/apiclient"
)

// maxGroupFormBytes bounds a group form, which is far smaller than a doléance:
// a name, a few lines and a pin.
const maxGroupFormBytes = 64 << 10

// groupPage is one local action group at its own address.
type groupPage struct {
	page

	ID          string
	Name        string
	Description string
	Place       string

	// Hidden says the group is no longer on the map.
	//
	// It still has a page, and the page says so plainly: leaving the map is
	// not disappearing. Returning a 404 would strand exactly the people who
	// could bring the group back — somebody holding the link is usually
	// somebody who was part of it.
	Hidden bool

	// Member and Role decide what the page offers.
	//
	// Answered by the same request that fetched the group, so the page cannot
	// show "join" to somebody who has already joined. Whether anybody is
	// signed in at all is on the embedded page, which every template has.
	Member bool
	Role   string

	// Actions is what the group is offering: accepted, not retired, soonest
	// first, capped at five. A page listing everything a group ever did is a
	// history; this is an invitation.
	Actions []apiclient.Action

	// Draft carries a refused message back to the form, so a refusal costs a
	// correction rather than what somebody wrote.
	Draft string

	// Sent says a message reached the group.
	//
	// It says nothing about whether anybody has been *reached*: whether an
	// admin has a live push subscription is a fact about that person's
	// devices, and a sentence that varied with it would leak one. One
	// sentence, always the same — the group will see this.
	Sent bool

	// Joined and Left say what just happened, so the page can acknowledge a
	// press rather than silently redrawing.
	Joined bool
	Left   bool

	Error string
}

// Admin reports whether the reader may manage this group.
func (g groupPage) Admin() bool { return g.Role == "admin" }

// group renders one group.
//
// # What resolves here and what does not
//
// An accepted group always does, whether or not it is currently on the map.
//
// A group that has never been accepted does **not**: it is unreviewed text at
// a public address, and a page that rendered it would publish by URL what the
// curation queue has not published by decision. Its admins reach it through
// the management page instead, which is behind being one of them.
func (s *site) group(w http.ResponseWriter, r *http.Request) {
	s.renderGroup(w, r, r.PathValue("id"), http.StatusOK, nil)
}

// proposePage is the form that starts a group.
type proposePage struct {
	page

	// Draft carries a rejected submission back to the form. Making somebody
	// retype what they already wrote is how a form loses the people it most
	// wants to hear from.
	Draft apiclient.GroupDraft

	// Error is a translation key, empty when nothing went wrong.
	Error string
}

func (s *site) proposeGroup(w http.ResponseWriter, r *http.Request) {
	// Signing in first, because the account that proposes a group becomes its
	// first admin: there is nobody to make an admin otherwise, and a group
	// with no admin is a door nobody can open.
	if !signedIn(r) {
		s.toSignin(w, r, "/groups/new")
		return
	}

	data := proposePage{page: s.newPage(r, "propose.title")}
	data.UsesMap = true
	s.renderer.Render(w, http.StatusOK, "propose", data)
}

func (s *site) createGroup(w http.ResponseWriter, r *http.Request) {
	if !signedIn(r) {
		s.toSignin(w, r, "/groups/new")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxGroupFormBytes)
	if err := r.ParseForm(); err != nil {
		s.rejectGroup(w, r, apiclient.GroupDraft{}, "propose.error_too_long")
		return
	}

	draft := apiclient.GroupDraft{
		Name:        strings.TrimSpace(r.PostFormValue("name")),
		Description: strings.TrimSpace(r.PostFormValue("description")),
		Place:       r.PostFormValue("place"),
		Country:     r.PostFormValue("country"),
	}
	if lat, err := strconv.ParseFloat(r.PostFormValue("latitude"), 64); err == nil {
		draft.Latitude = lat
	}
	if lon, err := strconv.ParseFloat(r.PostFormValue("longitude"), 64); err == nil {
		draft.Longitude = lon
	}
	if zoom, err := strconv.Atoi(r.PostFormValue("zoom")); err == nil {
		draft.Zoom = zoom
	}

	switch {
	case draft.Name == "":
		s.rejectGroup(w, r, draft, "propose.error_name")
		return
	case draft.Latitude == 0 && draft.Longitude == 0:
		// A group without a meeting place cannot be found by anybody near it,
		// which is the only thing this map is for.
		s.rejectGroup(w, r, draft, "propose.error_place")
		return
	}

	group, err := s.reader(r).CreateGroup(r.Context(), draft)
	if err != nil {
		// A taken name is the one failure the person can fix, and the backend
		// answers 409 for it. Everything else is ours, not theirs.
		if strings.Contains(err.Error(), "already taken") {
			s.rejectGroup(w, r, draft, "propose.error_taken")
			return
		}
		log.Error().Err(err).Msg("cannot propose a group")
		s.rejectGroup(w, r, draft, "propose.error_backend")
		return
	}

	// Straight to the group's own management page. There is no confirmation
	// link to wait for any more: the group exists, it is pending, and the
	// person who proposed it is its admin and can already fix a typo while a
	// curator looks.
	http.Redirect(w, r, "/groups/"+url.PathEscape(group.ID)+"/manage?proposed=1",
		http.StatusSeeOther)
}

func (s *site) rejectGroup(w http.ResponseWriter, r *http.Request, draft apiclient.GroupDraft, key string) {
	data := proposePage{page: s.newPage(r, "propose.title")}
	data.UsesMap = true
	data.Draft = draft
	data.Error = data.T(key)
	s.renderer.Render(w, http.StatusUnprocessableEntity, "propose", data)
}

// managePage is a group as one of its admins sees it.
type managePage struct {
	page

	ID      string
	Name    string
	Descr   string
	Place   string
	Status  string
	Pending bool

	// Proposed says the group was created a moment ago, so the page can
	// explain what happens next rather than looking like an edit form that
	// has already been used.
	Proposed bool

	// Members is who has joined, and who holds which role. There is no
	// address in it and nothing else to put there: an account is a chosen
	// name and nothing more, so what a group's admins see about their own
	// members is what those members decided to be called.
	Members []apiclient.Member

	// Messages is what people have written to the group. This replaced the
	// contact address, and it is the first private text this project carries.
	Messages []apiclient.GroupMessage
	Unread   int64

	// Actions is everything the group has announced, at any status — an
	// action waiting on a curator has to be visible to whoever wrote it.
	Actions []apiclient.Action

	// ActionOpen forces the announcement panel open, and ActionDraft carries
	// back what was typed into it: the panel opens on a fragment the server
	// never sees, so a refused announcement is answered by rendering the page
	// rather than redirecting.
	ActionOpen  bool
	ActionDraft apiclient.ActionDraft

	// EditingAction is which action the panel is editing, empty for a new one.
	EditingAction string

	// Draft seeds the map with the group as it stands.
	Draft apiclient.ManagedGroup

	// Queued says the last change is waiting on a review rather than live.
	// A published group stays on the map exactly as it was meanwhile, and
	// saying "saved" would send somebody to check the map and find it
	// unchanged.
	Queued bool

	Saved bool
	Error string
}

func (s *site) manageGroup(w http.ResponseWriter, r *http.Request) {
	if !signedIn(r) {
		s.toSignin(w, r, "/groups/"+r.PathValue("id")+"/manage")
		return
	}
	s.renderManage(w, r, http.StatusOK, nil)
}

// renderManage draws the management page, optionally with something to say
// about an attempt that has just failed.
//
// Shared by the GET and by the one POST that answers with a page instead of a
// redirect, so the two cannot drift into showing different pages.
func (s *site) renderManage(w http.ResponseWriter, r *http.Request, status int, adjust func(*managePage)) {
	reader := s.reader(r)

	group, err := reader.ManageGroup(r.Context(), r.PathValue("id"))
	if err != nil {
		// The backend answers 404 for a group that does not exist *and* for
		// one the caller may not manage, so that working through identifiers
		// tells nobody which is which. This page can only pass that on.
		log.Debug().Err(err).Str("group", r.PathValue("id")).
			Msg("group management page withheld")
		s.renderNotFound(w, r)
		return
	}

	data := managePage{page: s.newPage(r, "manage.title")}
	// The page carries a map, for the pin and for moving it.
	data.UsesMap = true
	data.ID = group.ID
	data.Name = group.Name
	data.Descr = group.Description
	data.Place = group.Place
	data.Status = group.Status
	data.Pending = group.Status != statusAccepted
	data.Members = group.Members
	data.Unread = group.Unread
	data.Draft = group

	if messages, _, err := reader.GroupMessages(r.Context(), group.ID); err != nil {
		// The rest of the page is still worth serving: somebody who came to
		// fix a description should not be stopped by the inbox.
		log.Warn().Err(err).Str("group", group.ID).Msg("cannot read the group's messages")
	} else {
		data.Messages = messages
	}

	if actions, err := reader.GroupActions(r.Context(), group.ID); err != nil {
		log.Warn().Err(err).Str("group", group.ID).Msg("cannot read the group actions")
	} else {
		data.Actions = actions
	}

	data.Saved = r.URL.Query().Get("saved") != ""
	data.Queued = r.URL.Query().Get("queued") != ""
	data.Proposed = r.URL.Query().Get("proposed") != ""

	if key := r.URL.Query().Get("error"); key != "" {
		data.Error = data.T(manageErrors[key])
	}
	if adjust != nil {
		adjust(&data)
	}

	s.renderer.Render(w, status, "manage", data)
}

// manageErrors maps what went wrong to what to say about it.
//
// Named rather than numbered, so a link carrying "?error=taken" says what
// happened in the address bar as well as on the page — and so an unknown value
// lands on the general sentence instead of an empty box.
var manageErrors = map[string]string{
	"taken":     "manage.error_taken",
	"lastadmin": "manage.error_last_admin",
	"general":   "manage.error_general",
}

func (s *site) saveGroup(w http.ResponseWriter, r *http.Request) {
	if !signedIn(r) {
		s.toSignin(w, r, "/groups/"+r.PathValue("id")+"/manage")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxGroupFormBytes)
	if err := r.ParseForm(); err != nil {
		s.renderNotFound(w, r)
		return
	}

	id := r.PathValue("id")
	edit := apiclient.GroupEdit{
		Name:        strings.TrimSpace(r.PostFormValue("name")),
		Description: strings.TrimSpace(r.PostFormValue("description")),
		Place:       r.PostFormValue("place"),
		Country:     r.PostFormValue("country"),
	}
	if lat, err := strconv.ParseFloat(r.PostFormValue("latitude"), 64); err == nil {
		edit.Latitude = lat
	}
	if lon, err := strconv.ParseFloat(r.PostFormValue("longitude"), 64); err == nil {
		edit.Longitude = lon
	}
	if zoom, err := strconv.Atoi(r.PostFormValue("zoom")); err == nil {
		edit.Zoom = zoom
	}

	back := func(query string) {
		http.Redirect(w, r, "/groups/"+url.PathEscape(id)+"/manage"+query, http.StatusSeeOther)
	}

	queued, err := s.reader(r).EditGroup(r.Context(), id, edit)
	if err != nil {
		log.Debug().Err(err).Str("group", id).Msg("cannot save a group")
		if strings.Contains(err.Error(), "already taken") {
			back("?error=taken")
			return
		}
		back("?error=general")
		return
	}

	// Redirected rather than rendered, so a reload does not resubmit.
	if queued {
		back("?queued=1")
		return
	}
	back("?saved=1")
}

// joinGroup is a press.
//
// Joining used to be a name, an address, a six-digit code and a form to type it
// back into — all of it to establish one thing, that the address was real,
// because the address *was* the identity. An account has proved itself with a
// passkey and carries no address to prove.
func (s *site) joinGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !signedIn(r) {
		// Back to this group once signed in, rather than to a dashboard:
		// somebody who pressed "join" wanted to be in *this* group.
		s.toSignin(w, r, "/groups/"+id)
		return
	}

	if _, err := s.reader(r).JoinGroup(r.Context(), id); err != nil {
		log.Debug().Err(err).Str("group", id).Msg("cannot join a group")
		s.renderGroup(w, r, id, http.StatusUnprocessableEntity, func(data *groupPage) {
			data.Error = data.T("join.error_failed")
		})
		return
	}

	s.renderGroup(w, r, id, http.StatusOK, func(data *groupPage) { data.Joined = true })
}

// leaveGroup takes somebody out of a group.
func (s *site) leaveGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !signedIn(r) {
		s.toSignin(w, r, "/groups/"+id)
		return
	}

	if _, err := s.reader(r).LeaveGroup(r.Context(), id); err != nil {
		log.Debug().Err(err).Str("group", id).Msg("cannot leave a group")
		s.renderGroup(w, r, id, http.StatusUnprocessableEntity, func(data *groupPage) {
			data.Error = data.T("join.error_failed")
		})
		return
	}

	s.renderGroup(w, r, id, http.StatusOK, func(data *groupPage) { data.Left = true })
}

// writeToGroup is how a group is reached.
//
// The register holds no way to contact anybody, so reaching a group happens
// inside it: a signed-in person writes, the group's admins are notified, and
// neither side learns anything about the other beyond a chosen name.
func (s *site) writeToGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !signedIn(r) {
		s.toSignin(w, r, "/groups/"+id)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxGroupFormBytes)
	if err := r.ParseForm(); err != nil {
		s.renderNotFound(w, r)
		return
	}

	text := strings.TrimSpace(r.PostFormValue("text"))
	if text == "" {
		s.renderGroup(w, r, id, http.StatusUnprocessableEntity, func(data *groupPage) {
			data.Error = data.T("contact.error_empty")
		})
		return
	}

	if err := s.reader(r).WriteToGroup(r.Context(), id, text); err != nil {
		log.Debug().Err(err).Str("group", id).Msg("cannot write to a group")
		s.renderGroup(w, r, id, http.StatusUnprocessableEntity, func(data *groupPage) {
			// The draft comes back: a refusal costs a correction, never what
			// somebody wrote.
			data.Draft = text
			data.Error = data.T("contact.error_failed")
		})
		return
	}

	s.renderGroup(w, r, id, http.StatusOK, func(data *groupPage) { data.Sent = true })
}

// renderGroup draws the public group page, optionally with something to say
// about an attempt that has just been made.
//
// Shared by the GET and by the POSTs that answer with a page rather than a
// redirect. They answer with a page because a refused message has to come back
// with its text, and the only other place to carry text is a query string.
func (s *site) renderGroup(w http.ResponseWriter, r *http.Request, id string,
	status int, adjust func(*groupPage)) {

	group, err := s.reader(r).GetGroup(r.Context(), id)
	if err != nil || group.Status != statusAccepted {
		// A group awaiting review answers exactly as one that does not exist.
		// Saying "this one is being looked at" would confirm to anybody
		// guessing identifiers that something is there.
		log.Debug().Err(err).Str("group", id).Msg("group page withheld")
		s.renderNotFound(w, r)
		return
	}

	data := groupPage{page: s.newPage(r, "group.title")}
	data.ID = group.ID
	data.Name = group.Name
	data.Description = group.Description
	data.Place = group.Place
	data.Hidden = !group.Visible
	data.Member = group.Member
	data.Role = group.Role

	actions, err := s.backend.PublicActions(r.Context(), id)
	if err != nil {
		// The group is still worth showing without its diary.
		log.Warn().Err(err).Str("group", id).Msg("cannot read the group's actions")
	}
	data.Actions = actions

	if adjust != nil {
		adjust(&data)
	}

	s.renderer.Render(w, status, "group", data)
}

// saveMemberRole grants or takes back a role.
//
// One endpoint for both, because they are one decision made twice and the form
// that sends them is the same row.
func (s *site) saveMemberRole(w http.ResponseWriter, r *http.Request) {
	if !signedIn(r) {
		s.toSignin(w, r, "/groups/"+r.PathValue("id")+"/manage")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxGroupFormBytes)
	if err := r.ParseForm(); err != nil {
		s.renderNotFound(w, r)
		return
	}

	id := r.PathValue("id")
	_, err := s.reader(r).SetMemberRole(r.Context(), id,
		r.PostFormValue("account"), r.PostFormValue("role"))

	query := "?saved=1"
	if err != nil {
		log.Debug().Err(err).Str("group", id).Msg("cannot change a member role")
		// The refusal somebody can act on: they are the only admin, and the
		// group would be left with nobody who can run it.
		if strings.Contains(err.Error(), "only admin") {
			query = "?error=lastadmin"
		} else {
			query = "?error=general"
		}
	}
	http.Redirect(w, r, "/groups/"+url.PathEscape(id)+"/manage"+query, http.StatusSeeOther)
}

// readGroupMessage marks one message in a group's inbox as read.
func (s *site) readGroupMessage(w http.ResponseWriter, r *http.Request) {
	if !signedIn(r) {
		s.toSignin(w, r, "/groups/"+r.PathValue("id")+"/manage")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxGroupFormBytes)
	if err := r.ParseForm(); err != nil {
		s.renderNotFound(w, r)
		return
	}

	id := r.PathValue("id")
	if err := s.reader(r).ReadGroupMessage(r.Context(), id, r.PostFormValue("read")); err != nil {
		log.Debug().Err(err).Str("group", id).Msg("cannot mark a group message read")
	}
	http.Redirect(w, r, "/groups/"+url.PathEscape(id)+"/manage#messages", http.StatusSeeOther)
}

// saveAction announces, changes, calls off or confirms one.
//
// One endpoint for all four, because every control in the actions table is a
// form post returning to the same page, and which one was pressed is the field
// that is filled.
func (s *site) saveAction(w http.ResponseWriter, r *http.Request) {
	if !signedIn(r) {
		s.toSignin(w, r, "/groups/"+r.PathValue("id")+"/manage")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxGroupFormBytes)
	if err := r.ParseForm(); err != nil {
		s.renderNotFound(w, r)
		return
	}

	id := r.PathValue("id")
	reader := s.reader(r)
	back := func(query string) {
		http.Redirect(w, r, "/groups/"+url.PathEscape(id)+"/manage"+query, http.StatusSeeOther)
	}

	switch {
	case r.PostFormValue("delete") != "":
		if _, err := reader.DeleteAction(r.Context(), id, r.PostFormValue("delete")); err != nil {
			log.Debug().Err(err).Str("group", id).Msg("cannot delete an action")
			back("?error=general")
			return
		}
		back("?saved=1")
		return

	case r.PostFormValue("confirm") != "":
		if _, err := reader.ConfirmAction(r.Context(), id, r.PostFormValue("confirm")); err != nil {
			log.Debug().Err(err).Str("group", id).Msg("cannot confirm an action")
			back("?error=general")
			return
		}
		back("?saved=1")
		return
	}

	draft := apiclient.ActionDraft{
		Title:       strings.TrimSpace(r.PostFormValue("title")),
		Description: strings.TrimSpace(r.PostFormValue("description")),
		Type:        r.PostFormValue("type"),
		StartsOn:    strings.TrimSpace(r.PostFormValue("starts_on")),
		StartsTime:  strings.TrimSpace(r.PostFormValue("starts_time")),
		Repeat:      r.PostFormValue("repeat"),
		Monthly:     r.PostFormValue("monthly"),
		Note:        strings.TrimSpace(r.PostFormValue("note")),
	}
	// Where it happens. The switch is read whatever its state, because "back
	// to where the group meets" is a change the backend cannot infer from an
	// empty pin — that reads as "leave the place alone".
	draft.Elsewhere = r.PostFormValue("elsewhere") != ""
	if draft.Elsewhere {
		draft.Place = r.PostFormValue("place")
		draft.Country = r.PostFormValue("country")
		if lat, err := strconv.ParseFloat(r.PostFormValue("latitude"), 64); err == nil {
			draft.Latitude = lat
		}
		if lon, err := strconv.ParseFloat(r.PostFormValue("longitude"), 64); err == nil {
			draft.Longitude = lon
		}
		draft.Zoom = formInt(r, "zoom", 0)
	}

	// The rhythm arrives as choices, never as a rule: a field that accepted an
	// RRULE would be a field somebody could put anything into, and "anything"
	// here is a date on a page telling people when to turn up.
	draft.Interval = formInt(r, "interval", 1)
	draft.Week = formInt(r, "week", 1)
	draft.Weekday = formInt(r, "weekday", 0)
	draft.Day = formInt(r, "day", 1)
	draft.Month = formInt(r, "month", 1)
	for _, value := range r.PostForm["weekdays"] {
		if day, err := strconv.Atoi(value); err == nil {
			draft.Weekdays = append(draft.Weekdays, day)
		}
	}

	editing := r.PostFormValue("action")
	if editing != "" {
		if _, err := reader.EditAction(r.Context(), id, editing, draft); err != nil {
			s.rejectAction(w, r, editing, draft, err)
			return
		}
	} else {
		if _, err := reader.CreateAction(r.Context(), id, draft); err != nil {
			s.rejectAction(w, r, "", draft, err)
			return
		}
	}
	back("?queued=1")
}

// rejectAction re-renders the page with the panel open and the draft in it.
//
// A render rather than a redirect: the panel opens on a fragment the server
// never sees, and the only other way to carry a draft back is a query string.
func (s *site) rejectAction(w http.ResponseWriter, r *http.Request, editing string,
	draft apiclient.ActionDraft, cause error) {

	log.Debug().Err(cause).Msg("cannot save an action")

	// The backend refuses an announcement for a handful of reasons and says
	// which in its answer. They are all things the person can fix by changing
	// what they typed, so they get one sentence and their words back.
	key := "actions.error"
	switch {
	case strings.Contains(cause.Error(), "needs a date"):
		key = "actions.error_date"
	case strings.Contains(cause.Error(), "needs its rhythm"):
		key = "actions.error_recurrence"
	case strings.Contains(cause.Error(), "needs a title"):
		key = "actions.error_title"
	}

	s.renderManage(w, r, http.StatusUnprocessableEntity, func(data *managePage) {
		data.ActionOpen = true
		data.ActionDraft = draft
		data.EditingAction = editing
		data.Error = data.T(key)
	})
}

// formInt reads a whole number from a form, falling back when it is absent or
// unreadable. The backend validates the range; this only has to avoid turning
// an empty select into a zero that means something.
func formInt(r *http.Request, field string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue(field)))
	if err != nil {
		return fallback
	}
	return value
}
