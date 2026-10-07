package console

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/authentik"
	"github.com/CoderSyndicate/doleances/internal/console/auth"
)

// personRow is one member of staff as the page shows them.
type personRow struct {
	PK       int
	Username string
	Name     string
	Email    string

	Admin   bool
	Curator bool

	// Self marks the person reading the page, so the controls that would lock
	// them out of their own console can be left off.
	Self bool
}

// peoplePage lists who may do what.
type peoplePage struct {
	page

	People []personRow

	// RecoveryReady says somebody added here can actually be given a way in.
	// False means the directory has no recovery flow, and an admin would be
	// creating accounts nobody can sign into — which has to be said before
	// they do it, not after.
	RecoveryReady bool

	// Added and RecoveryLink are shown once, immediately after somebody is
	// created. The link is the whole onboarding: no password exists and
	// nothing is emailed, so an admin passes this on themselves.
	Added        string
	RecoveryLink string

	Error  string
	Notice string

	// AddOpen forces the add panel open, and Draft is what was typed into it.
	//
	// Without these a refusal would close the panel and empty it: the answer
	// arrives through a redirect, so the fragment that opened it is gone and
	// so is the form state. Somebody who mistyped one field would retype all
	// four — and the most likely refusal here is the one that costs nothing to
	// correct, a role nobody ticked.
	AddOpen bool
	Draft   inviteDraft
}

// inviteDraft is what somebody typed into the add panel.
type inviteDraft struct {
	Name     string
	Username string
	Email    string
	Admin    bool
	Curator  bool
}

// people lists the staff.
//
// Read from authentik every time. The plan's rule is that the directory is
// authoritative, and a page that cached its staff list would be one where
// somebody removed in authentik still appears to hold a role — which is the
// exact confusion this page exists to resolve.
func (c *console) people(w http.ResponseWriter, r *http.Request) {
	if !c.mayAdminister(r) {
		http.Error(w, "only an admin may manage people", http.StatusForbidden)
		return
	}

	data := peoplePage{page: c.newPage(r, "people.title")}
	data.Added = r.URL.Query().Get("added")
	data.RecoveryLink = r.URL.Query().Get("link")
	data.Notice = r.URL.Query().Get("notice")
	data.Error = r.URL.Query().Get("error")

	query := r.URL.Query()
	data.AddOpen = query.Get("open") != ""
	data.Draft = inviteDraft{
		Name:     query.Get("name"),
		Username: query.Get("username"),
		Email:    query.Get("email"),
		Admin:    query.Get("role_admin") != "",
		Curator:  query.Get("role_curator") != "",
	}

	if c.directory == nil {
		data.Error = "this console has no identity provider configured"
		c.renderer.Render(w, http.StatusOK, "people", data)
		return
	}

	rows, ready, err := c.staff(r)
	if err != nil {
		log.Error().Err(err).Msg("cannot read the staff list")
		data.Error = "cannot reach the directory: " + err.Error()
		c.renderer.Render(w, http.StatusBadGateway, "people", data)
		return
	}
	data.People = rows
	data.RecoveryReady = ready

	c.renderer.Render(w, http.StatusOK, "people", data)
}

// staff reads both role groups and merges them into one list.
//
// Two calls rather than one listing of everybody: this console has no business
// enumerating an operator's whole directory, only the people it has given a
// role to.
func (c *console) staff(r *http.Request) ([]personRow, bool, error) {
	ctx := r.Context()

	admins, err := c.directory.UsersInGroup(ctx, c.adminGroupName)
	if err != nil {
		return nil, false, err
	}
	curators, err := c.directory.UsersInGroup(ctx, c.curatorGroupName)
	if err != nil {
		return nil, false, err
	}

	me := whoIs(ctx)
	byPK := map[int]*personRow{}

	collect := func(people []authentik.User, mark func(*personRow)) {
		for _, person := range people {
			row, seen := byPK[person.PK]
			if !seen {
				row = &personRow{
					PK: person.PK, Username: person.Username,
					Name: person.Name, Email: person.Email,
					Self: me != nil && me.Username == person.Username,
				}
				byPK[person.PK] = row
			}
			mark(row)
		}
	}
	collect(admins, func(row *personRow) { row.Admin = true })
	collect(curators, func(row *personRow) { row.Curator = true })

	rows := make([]personRow, 0, len(byPK))
	for _, row := range byPK {
		rows = append(rows, *row)
	}
	// By name, so the list reads the same way twice running. authentik's own
	// order is not something to depend on.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].Username < rows[j].Username
	})

	ready, err := c.directory.Check(ctx)
	if err != nil {
		// Not fatal to showing the list. The warning about onboarding is worth
		// having and not worth failing the page over.
		log.Warn().Err(err).Msg("cannot check whether the directory can onboard anybody")
		return rows, true, nil
	}
	return rows, ready.HasRecoveryFlow, nil
}

// savePeople adds somebody, changes what they may do, or takes it all away.
//
// Every branch re-reads the acting person's roles from the directory first.
// Managing staff is the most consequential thing on this console — it decides
// who may accept and reject other people's words — so it is checked against
// authentik as it is now rather than against a session's copy.
func (c *console) savePeople(w http.ResponseWriter, r *http.Request) {
	who, allowed := c.confirm(w, r, auth.RoleAdmin)
	if !allowed {
		return
	}
	if c.directory == nil {
		http.Error(w, "this console has no identity provider configured", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "that form could not be read", http.StatusBadRequest)
		return
	}

	switch {
	case r.PostFormValue("invite") != "":
		c.invite(w, r, who)
	case r.PostFormValue("remove") != "":
		c.removePerson(w, r, who)
	case r.PostFormValue("relink") != "":
		c.relink(w, r, who)
	case r.PostFormValue("update") != "":
		c.changeRoles(w, r, who)
	default:
		c.backToPeople(w, r, outcome{Problem: "nothing to do"})
	}
}

// invite creates somebody and mints the one-time link that is their way in.
//
// **No password is set and nothing is emailed.** authentik has no flag that
// forces a password change at next sign-in, so a temporary one would be a
// credential the admin knows and that outlives the first login. A recovery
// link expires, is single-use, and ends with the person setting up their own
// passkey — which is also what the rest of this register gives its readers.
func (c *console) invite(w http.ResponseWriter, r *http.Request, who *auth.Identity) {
	username := strings.TrimSpace(r.PostFormValue("username"))
	name := strings.TrimSpace(r.PostFormValue("name"))
	email := strings.TrimSpace(r.PostFormValue("email"))

	draft := &inviteDraft{
		Name: name, Username: username, Email: email,
		Admin:   r.PostFormValue("role_admin") != "",
		Curator: r.PostFormValue("role_curator") != "",
	}

	if username == "" || name == "" {
		c.backToPeople(w, r, outcome{
			Problem: "a username and a name are both needed", Draft: draft})
		return
	}

	// Nothing is created until the directory can actually hand the account
	// over. The page already says a recovery flow is missing, and creating
	// anyway produced exactly what that warning described: an account in
	// somebody else's directory that nobody can sign into, made by pressing a
	// button that reported an error.
	//
	// The same rule the wizard follows — prove the prerequisites before
	// writing into a directory that is not ours. A Check that cannot be made
	// does not block: an unreachable directory is not proof that a flow is
	// missing, and `relink` is the way back if it turns out to be.
	if found, err := c.directory.Check(r.Context()); err == nil && !found.HasRecoveryFlow {
		c.backToPeople(w, r, outcome{
			Problem: "nobody was created: this directory has no recovery flow, " +
				"so there would be no way to hand the account over — set one as the brand's " +
				"recovery flow in authentik first",
			Draft: draft})
		return
	}

	var groups []string
	if r.PostFormValue("role_admin") != "" {
		groups = append(groups, c.adminGroup)
	}
	if r.PostFormValue("role_curator") != "" {
		groups = append(groups, c.curatorGroup)
	}
	if len(groups) == 0 {
		// Somebody with no role is not staff here, and creating them would put
		// an account in the operator's directory that this console can neither
		// use nor explain.
		c.backToPeople(w, r, outcome{Problem: "tick at least one role", Draft: draft})
		return
	}

	// # An account that already exists is adopted, not refused
	//
	// The directory is the operator's and people are already in it. Typing the
	// username of a colleague who has an authentik account — the ordinary case
	// on any instance that is not brand new — used to hand back authentik's
	// own `username: user with this username already exists`, and an admin
	// with no way forward: they cannot create the account, and the person is
	// not on the page to be given a role because the page lists only staff.
	//
	// So this reconciles, like every other object the wizard touches. Nothing
	// on an adopted record is overwritten — a name or an address typed here
	// does not rewrite what their directory already says about them. Only the
	// roles are granted.
	existing, err := c.directory.UserByUsername(r.Context(), username)
	switch {
	case err == nil:
		c.adopt(w, r, who, existing, groups)
		return
	case !errors.Is(err, authentik.ErrNotFound):
		c.backToPeople(w, r, outcome{Problem: err.Error(), Draft: draft})
		return
	}

	created, err := c.directory.CreateUser(r.Context(), authentik.UserSpec{
		Username: username, Name: name, Email: email, Groups: groups,
	})
	if err != nil {
		c.backToPeople(w, r, outcome{Problem: err.Error(), Draft: draft})
		return
	}

	link, err := c.directory.RecoveryLink(r.Context(), created.PK)
	if err != nil {
		// The account exists and cannot be handed over. Said plainly, with the
		// account named, so an admin knows what state they are in rather than
		// pressing the button again and making a second one.
		log.Error().Err(err).Str("username", username).
			Msg("the account was created and no way in could be minted")
		c.backToPeople(w, r, outcome{Problem: username + " was created, but no recovery link could be made: " + err.Error()})
		return
	}

	log.Warn().Str("by", who.Username).Str("username", username).
		Bool("admin", r.PostFormValue("role_admin") != "").
		Bool("curator", r.PostFormValue("role_curator") != "").
		Msg("console: a member of staff was added")

	c.backToPeople(w, r, outcome{Added: username, Link: link})
}

// relink mints a fresh way in for somebody who already exists.
//
// # Why this is not the same as keeping a copy
//
// The link shown when somebody is created is shown once and never stored — a
// page an admin can revisit to re-read somebody else's way in is a page
// anybody who reaches this console can revisit. This mints a **new** one,
// which invalidates nothing about that rule: the admin is asking the directory
// for a fresh single-use link, exactly as they did when they created them.
//
// # Why it has to exist
//
// A recovery link expires and works once, so an invitation that was mislaid,
// or sat in a chat window too long, leaves somebody with no way in and an
// admin with nothing to do but create a second account for the same person.
// And an account created while the directory had no recovery flow — which is
// how this gap was found — is stranded permanently without it.
func (c *console) relink(w http.ResponseWriter, r *http.Request, who *auth.Identity) {
	pk, err := strconv.Atoi(r.PostFormValue("pk"))
	if err != nil {
		c.backToPeople(w, r, outcome{Problem: "that is not a person"})
		return
	}
	username := strings.TrimSpace(r.PostFormValue("username"))

	link, err := c.directory.RecoveryLink(r.Context(), pk)
	if err != nil {
		c.backToPeople(w, r, outcome{Problem: "no recovery link could be made: " + err.Error()})
		return
	}

	log.Warn().Str("by", who.Username).Str("username", username).Int("person", pk).
		Msg("console: a fresh way in was minted for a member of staff")

	c.backToPeople(w, r, outcome{Added: username, Link: link})
}

// removePerson takes somebody out of this register entirely.
//
// # It removes a role, never a person
//
// What it does is take them out of both of this register's groups. **Their
// authentik account is left exactly as it was** — the directory is the
// operator's and may hold their mail, their other applications and their whole
// working identity. A console that provisioned itself into somebody else's
// directory has no business deleting people out of it.
//
// It is therefore the same write as unticking both boxes, and exists as its
// own control because that is not what unticking two boxes reads as. An admin
// looking for the way to revoke somebody should find it, rather than deduce
// it.
//
// # Two refusals, and neither is left to the page
//
// The last admin, for the reason a group's last admin cannot leave and the
// last passkey cannot be removed: what follows is a console nobody can
// administer. And yourself — the control is not drawn on your own row, but a
// hidden control is not a guard, and this one would lock the person pressing
// it out of the page they pressed it on.
func (c *console) removePerson(w http.ResponseWriter, r *http.Request, who *auth.Identity) {
	pk, err := strconv.Atoi(r.PostFormValue("pk"))
	if err != nil {
		c.backToPeople(w, r, outcome{Problem: "that is not a person"})
		return
	}
	username := strings.TrimSpace(r.PostFormValue("username"))

	if username != "" && username == who.Username {
		c.backToPeople(w, r, outcome{Problem: "you cannot remove yourself — " +
			"another admin can, or untick the roles you no longer want"})
		return
	}

	admins, err := c.directory.UsersInGroup(r.Context(), c.adminGroupName)
	if err != nil {
		c.backToPeople(w, r, outcome{Problem: "cannot check the other admins: " + err.Error()})
		return
	}
	var others, wasAdmin int
	for _, person := range admins {
		if person.PK == pk {
			wasAdmin++
			continue
		}
		others++
	}
	if wasAdmin > 0 && others == 0 {
		c.backToPeople(w, r, outcome{
			Problem: "that is the only admin left — make somebody else an admin first"})
		return
	}

	for _, group := range []string{c.adminGroup, c.curatorGroup} {
		if err := c.directory.RemoveFromGroup(r.Context(), group, pk); err != nil {
			c.backToPeople(w, r, outcome{Problem: err.Error()})
			return
		}
	}

	// WARN, like a role change: it decides who may accept and reject other
	// people's words.
	log.Warn().Str("by", who.Username).Str("username", username).Int("person", pk).
		Msg("console: somebody was removed from this register's roles")

	name := username
	if name == "" {
		name = "that person"
	}
	c.backToPeople(w, r, outcome{
		Notice: name + " no longer holds a role here; their directory account is untouched"})
}

// adopt gives an existing directory account this register's roles.
//
// # No way in is minted, deliberately
//
// Somebody who already has an account already has a credential — they sign in
// with whatever their directory gave them, and a recovery link would be this
// console offering to **reset** a colleague rather than onboard them. Pressed
// without thinking on a username that happens to belong to somebody important,
// that is a password reset nobody asked for.
//
// The page says the account was adopted rather than created, so an admin knows
// which of the two happened, and "new way in" is there for the case where they
// really do need one — a separate, deliberate press, guarded by
// authentik.ErrWouldEscalate.
func (c *console) adopt(w http.ResponseWriter, r *http.Request,
	who *auth.Identity, person authentik.User, groups []string) {
	for _, group := range groups {
		if err := c.directory.AddToGroup(r.Context(), group, person.PK); err != nil {
			c.backToPeople(w, r, outcome{Problem: err.Error()})
			return
		}
	}

	log.Warn().Str("by", who.Username).Str("username", person.Username).
		Msg("console: an existing directory account was given a role here")

	c.backToPeople(w, r, outcome{Notice: person.Username +
		" already had an account in the directory, so it was given the roles rather than " +
		"created. They sign in with the credential they already have — use \"new way in\" " +
		"only if they need a fresh one."})
}

// changeRoles grants and revokes, and refuses the one change that would leave
// nobody able to administer this register.
func (c *console) changeRoles(w http.ResponseWriter, r *http.Request, who *auth.Identity) {
	pk, err := strconv.Atoi(r.PostFormValue("pk"))
	if err != nil {
		c.backToPeople(w, r, outcome{Problem: "that is not a person"})
		return
	}

	wantAdmin := r.PostFormValue("role_admin") != ""
	wantCurator := r.PostFormValue("role_curator") != ""

	// The last admin cannot unmake themselves. The same shape as a group's
	// last admin and as the last passkey on an account: the way out is named
	// rather than the action silently allowed, because what follows is a
	// console nobody can administer.
	if !wantAdmin {
		remaining, err := c.directory.UsersInGroup(r.Context(), c.adminGroupName)
		if err != nil {
			c.backToPeople(w, r, outcome{Problem: "cannot check the other admins: " + err.Error()})
			return
		}
		others := 0
		for _, person := range remaining {
			if person.PK != pk {
				others++
			}
		}
		if others == 0 {
			c.backToPeople(w, r, outcome{Problem: "that is the only admin left — make somebody else an admin first"})
			return
		}
	}

	for _, change := range []struct {
		group string
		want  bool
	}{
		{c.adminGroup, wantAdmin},
		{c.curatorGroup, wantCurator},
	} {
		var err error
		if change.want {
			err = c.directory.AddToGroup(r.Context(), change.group, pk)
		} else {
			err = c.directory.RemoveFromGroup(r.Context(), change.group, pk)
		}
		if err != nil {
			c.backToPeople(w, r, outcome{Problem: err.Error()})
			return
		}
	}

	// WARN, because it changes who may accept and reject other people's words.
	log.Warn().Str("by", who.Username).Int("person", pk).
		Bool("admin", wantAdmin).Bool("curator", wantCurator).
		Msg("console: somebody's roles were changed")

	c.backToPeople(w, r, outcome{})
}

// outcome is what the page says after an action, and nothing else.
type outcome struct {
	// Added and Link are the new account and the one-time way in, shown
	// together or not at all.
	Added string
	Link  string

	// Notice is a plain confirmation — something happened and there is nothing
	// to hand over.
	Notice string

	// Problem is a refusal or a failure.
	Problem string

	// Draft reopens the add panel with what was typed still in it. Set on
	// every refusal that happens while somebody is adding a person, and on
	// nothing else — a panel that reopened after an unrelated failure would be
	// in the way.
	Draft *inviteDraft
}

// backToPeople redirects, carrying whatever has to be shown once.
//
// A redirect rather than rendering in place, so a reload does not repeat the
// action — and the recovery link travels in the query string because it is
// shown exactly once and the console keeps no copy of it.
func (c *console) backToPeople(w http.ResponseWriter, r *http.Request, said outcome) {
	where := "/settings/people"
	query := make([]string, 0, 4)
	if said.Added != "" {
		query = append(query, "added="+urlValue(said.Added))
	}
	if said.Link != "" {
		query = append(query, "link="+urlValue(said.Link))
	}
	if said.Notice != "" {
		query = append(query, "notice="+urlValue(said.Notice))
	}
	if said.Problem != "" {
		query = append(query, "error="+urlValue(said.Problem))
	}
	if draft := said.Draft; draft != nil {
		query = append(query, "open=1",
			"name="+urlValue(draft.Name),
			"username="+urlValue(draft.Username),
			"email="+urlValue(draft.Email))
		if draft.Admin {
			query = append(query, "role_admin=1")
		}
		if draft.Curator {
			query = append(query, "role_curator=1")
		}
	}
	if len(query) > 0 {
		where += "?" + strings.Join(query, "&")
	}
	http.Redirect(w, r, where, http.StatusSeeOther)
}

// urlValue escapes a value for the query string.
func urlValue(value string) string { return url.QueryEscape(value) }
