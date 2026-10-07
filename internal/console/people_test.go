package console

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/console/auth"
	"github.com/CoderSyndicate/doleances/internal/web"
)

// invited builds the form an admin fills in and posts it at invite directly.
//
// Directly rather than through savePeople, because what is under test is the
// order of two calls to somebody else's directory, not the role check in front
// of them — which signin_test already covers.
func invited(t *testing.T, c *console) *httptest.ResponseRecorder {
	t.Helper()

	form := url.Values{
		"name": {"Luscus"}, "username": {"luscus"},
		"email": {"luscus@example.org"}, "role_curator": {"1"},
	}
	r := httptest.NewRequest(http.MethodPost, "/settings/people",
		strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("parse the form: %v", err)
	}

	recorder := httptest.NewRecorder()
	c.invite(recorder, r, &auth.Identity{Username: "dominique"})
	return recorder
}

// TestNobodyIsCreatedWhenThereIsNoWayToHandTheAccountOver.
//
// This is what a live run did: the page said the directory had no recovery
// flow, the admin pressed the button anyway, and authentik created the account
// and then refused the link — leaving a person in somebody else's directory
// who could never sign in, made by a button that reported an error.
//
// The prerequisite is proved before anything is written, the same rule the
// wizard follows for flows.
func TestNobodyIsCreatedWhenThereIsNoWayToHandTheAccountOver(t *testing.T) {
	d := newDirectory(t)
	d.noRecovery = true
	c := &console{directory: d.client(t), curatorGroup: "uuid-curators"}

	got := invited(t, c)
	if got.Code != http.StatusSeeOther {
		t.Fatalf("code = %d, want a redirect carrying the refusal", got.Code)
	}
	if where := got.Header().Get("Location"); !strings.Contains(where, "error=") {
		t.Errorf("location = %q, want the refusal said in words", where)
	}
	for _, call := range d.seen {
		if call == "POST /api/v3/core/users/" {
			t.Fatalf("an account was created that nobody could ever sign into: %v", d.seen)
		}
	}
}

// TestAnAccountComesWithTheLinkThatIsItsWayIn.
//
// The other half: where the directory can onboard, the account is made and the
// one-time link comes back to be passed on by hand. No password is set and
// nothing is emailed.
func TestAnAccountComesWithTheLinkThatIsItsWayIn(t *testing.T) {
	d := newDirectory(t)
	c := &console{directory: d.client(t), curatorGroup: "uuid-curators"}

	where := invited(t, c).Header().Get("Location")
	if !strings.Contains(where, "link=") || !strings.Contains(where, "added=luscus") {
		t.Errorf("location = %q, want the new account and its link", where)
	}
	if strings.Contains(where, "error=") {
		t.Errorf("location = %q, want no error", where)
	}

	sent := d.bodies["POST /api/v3/core/users/"]
	if sent == nil {
		t.Fatal("nobody was created")
	}
	if sent["password"] != nil {
		t.Error("a password was set: authentik cannot force a change at next sign-in")
	}
}

// TestAStrandedAccountCanBeGivenAFreshWayIn.
//
// A recovery link expires and works once, so an invitation mislaid or left in
// a chat window too long leaves somebody with no way in — and an account made
// while the directory had no recovery flow is stranded for good. Without this
// an admin's only move is a second account for the same person.
func TestAStrandedAccountCanBeGivenAFreshWayIn(t *testing.T) {
	d := newDirectory(t)
	c := &console{directory: d.client(t)}

	form := url.Values{"pk": {"5"}, "username": {"luscus"}, "relink": {"1"}}
	r := httptest.NewRequest(http.MethodPost, "/settings/people",
		strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("parse the form: %v", err)
	}

	recorder := httptest.NewRecorder()
	c.relink(recorder, r, &auth.Identity{Username: "dominique"})

	where := recorder.Header().Get("Location")
	if !strings.Contains(where, "link=") {
		t.Errorf("location = %q, want a fresh link", where)
	}
	var minted bool
	for _, call := range d.seen {
		if call == "POST /api/v3/core/users/5/recovery/" {
			minted = true
		}
		if call == "POST /api/v3/core/users/" {
			t.Error("a second account was created rather than the person re-invited")
		}
	}
	if !minted {
		t.Errorf("calls = %v, want the directory asked for a new link", d.seen)
	}
}

// removed posts the removal form for one person.
func removed(t *testing.T, c *console, pk, username, by string) *httptest.ResponseRecorder {
	t.Helper()

	form := url.Values{"pk": {pk}, "username": {username}, "remove": {"1"}}
	r := httptest.NewRequest(http.MethodPost, "/settings/people",
		strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("parse the form: %v", err)
	}

	recorder := httptest.NewRecorder()
	c.removePerson(recorder, r, &auth.Identity{Username: by})
	return recorder
}

// TestRemovingSomebodyTakesTheirRolesAndNotTheirAccount.
//
// The directory belongs to the operator, not to this register: somebody's
// authentik account may carry their mail, their other applications and their
// whole working identity. A console that provisioned itself into it deletes
// nobody out of it.
func TestRemovingSomebodyTakesTheirRolesAndNotTheirAccount(t *testing.T) {
	d := newDirectory(t)
	d.admins = []any{
		map[string]any{"pk": 1, "username": "dominique"},
		map[string]any{"pk": 5, "username": "luscus"},
	}
	c := &console{
		directory: d.client(t), adminGroupName: "doleances_admin",
		adminGroup: "uuid-admins", curatorGroup: "uuid-curators",
	}

	where := removed(t, c, "5", "luscus", "dominique").Header().Get("Location")
	if !strings.Contains(where, "notice=") {
		t.Errorf("location = %q, want it to say what happened", where)
	}

	var fromAdmins, fromCurators bool
	for _, call := range d.seen {
		switch call {
		case "POST /api/v3/core/groups/uuid-admins/remove_user/":
			fromAdmins = true
		case "POST /api/v3/core/groups/uuid-curators/remove_user/":
			fromCurators = true
		}
		if strings.HasPrefix(call, "DELETE") {
			t.Errorf("something was deleted from the operator's directory: %v", d.seen)
		}
	}
	if !fromAdmins || !fromCurators {
		t.Errorf("calls = %v, want both roles taken away", d.seen)
	}
}

// TestTheLastAdminCannotBeRemoved.
//
// The same shape as a group's last admin and the last passkey on an account:
// what follows otherwise is a console nobody can administer, and no way back
// except the setup wizard and the network boundary in front of it.
func TestTheLastAdminCannotBeRemoved(t *testing.T) {
	d := newDirectory(t)
	d.admins = []any{map[string]any{"pk": 5, "username": "luscus"}}
	c := &console{
		directory: d.client(t), adminGroupName: "doleances_admin",
		adminGroup: "uuid-admins", curatorGroup: "uuid-curators",
	}

	where := removed(t, c, "5", "luscus", "dominique").Header().Get("Location")
	if !strings.Contains(where, "error=") {
		t.Errorf("location = %q, want the refusal", where)
	}
	if d.groupRemoved {
		t.Error("the only admin was removed, leaving a console nobody can administer")
	}
}

// TestYouCannotRemoveYourself.
//
// The control is not drawn on your own row, and that is not the guard: a form
// post is a form post. Pressing it would lock the person out of the page they
// pressed it on, and of every other page on this console.
func TestYouCannotRemoveYourself(t *testing.T) {
	d := newDirectory(t)
	d.admins = []any{
		map[string]any{"pk": 1, "username": "dominique"},
		map[string]any{"pk": 5, "username": "luscus"},
	}
	c := &console{
		directory: d.client(t), adminGroupName: "doleances_admin",
		adminGroup: "uuid-admins", curatorGroup: "uuid-curators",
	}

	where := removed(t, c, "1", "dominique", "dominique").Header().Get("Location")
	if !strings.Contains(where, "error=") {
		t.Errorf("location = %q, want the refusal", where)
	}
	if d.groupRemoved {
		t.Error("an admin removed themselves through a control the page does not draw")
	}
}

// TestACuratorWhoIsNotAnAdminIsRemovedWithoutTheAdminQuestion.
//
// The last-admin guard must not refuse somebody who was never an admin — which
// it would, if it counted the group rather than this person's place in it.
func TestACuratorWhoIsNotAnAdminIsRemovedWithoutTheAdminQuestion(t *testing.T) {
	d := newDirectory(t)
	d.admins = []any{map[string]any{"pk": 1, "username": "dominique"}}
	c := &console{
		directory: d.client(t), adminGroupName: "doleances_admin",
		adminGroup: "uuid-admins", curatorGroup: "uuid-curators",
	}

	where := removed(t, c, "5", "luscus", "dominique").Header().Get("Location")
	if strings.Contains(where, "error=") {
		t.Errorf("location = %q, want a curator removed without complaint", where)
	}
	if !d.groupRemoved {
		t.Error("nothing was removed")
	}
}

// TestThePageDrawsTheControlsItClaimsTo.
//
// The page is otherwise never rendered by a test, which is how a whole working
// page stayed out of the navigation for a day. Rendering it catches what
// parsing cannot — an undefined `{{template}}`, a `.Field` that moved — and
// asserts the two controls that are only ever on somebody else's row.
func TestThePageDrawsTheControlsItClaimsTo(t *testing.T) {
	renderer, err := web.NewRenderer(assets, "templates/shared/*.html", "templates/pages/*.html")
	if err != nil {
		t.Fatalf("parse the console templates: %v", err)
	}

	data := peoplePage{
		page: page{Page: web.Page{Lang: "en"}, Admin: true},
		People: []personRow{
			{PK: 5, Username: "luscus", Name: "Luscus", Curator: true},
			{PK: 1, Username: "dominique", Name: "Dominique", Admin: true, Self: true},
		},
		RecoveryReady: true,
	}

	recorder := httptest.NewRecorder()
	renderer.Render(recorder, http.StatusOK, "people", data)
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d, want the page to render", recorder.Code)
	}
	body := recorder.Body.String()

	for _, want := range []string{
		`name="remove"`, `name="relink"`, `name="update"`,
		`<svg`, `form="roles-5"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page has no %s", want)
		}
	}

	// Nothing acting on the person reading the page: removing yourself locks
	// you out of the console you are standing in, and you need no way in.
	if strings.Contains(body, `value="dominique"`) {
		t.Error("a control was drawn on the reader's own row")
	}

	// The headers promise four columns. They described three for a while and
	// the body put everything in one cell under the second.
	if headers := strings.Count(body, "<th "); headers != 4 {
		t.Errorf("%d column headers, want 4", headers)
	}
}

// TestTheAddPanelIsClosedUntilItIsAskedFor.
//
// It is a `:target` panel, so "closed" is the absence of the class the server
// adds — there is no script to ask. The form is still in the markup, which is
// the point: the button is a fragment link and the browser does the rest.
func TestTheAddPanelIsClosedUntilItIsAskedFor(t *testing.T) {
	renderer, err := web.NewRenderer(assets, "templates/shared/*.html", "templates/pages/*.html")
	if err != nil {
		t.Fatalf("parse the console templates: %v", err)
	}

	render := func(data peoplePage) string {
		recorder := httptest.NewRecorder()
		renderer.Render(recorder, http.StatusOK, "people", data)
		if recorder.Code != http.StatusOK {
			t.Fatalf("code = %d, want the page to render", recorder.Code)
		}
		return recorder.Body.String()
	}

	shut := render(peoplePage{page: page{Page: web.Page{Lang: "en"}}})
	if strings.Contains(shut, `class="overlay open"`) {
		t.Error("the panel is open before anybody asked for it")
	}
	if !strings.Contains(shut, `href="#add-panel"`) {
		t.Error("nothing opens the panel")
	}

	// Forced open, carrying what was typed. A refusal arrives through a
	// redirect, so without this the panel would shut and empty itself and the
	// admin would retype all four fields over a ticked box.
	open := render(peoplePage{
		page:    page{Page: web.Page{Lang: "en"}},
		AddOpen: true,
		Error:   "tick at least one role",
		Draft: inviteDraft{
			Name: "Luscus", Username: "luscus", Email: "luscus@example.org", Curator: true,
		},
	})
	if !strings.Contains(open, `class="overlay open"`) {
		t.Error("a refusal left the panel shut")
	}
	for _, want := range []string{`value="Luscus"`, `value="luscus"`, `value="luscus@example.org"`} {
		if !strings.Contains(open, want) {
			t.Errorf("the panel came back without %s", want)
		}
	}
	// The ticked box specifically, and only that one: `Contains(body,
	// "checked")` would pass on any row of the table above.
	ticked := regexp.MustCompile(`name="role_(\w+)" value="1"\s*\n?\s*checked`)
	var roles []string
	for _, found := range ticked.FindAllStringSubmatch(open, -1) {
		roles = append(roles, found[1])
	}
	if len(roles) != 1 || roles[0] != "curator" {
		t.Errorf("checked roles = %v, want only the curator box the admin ticked", roles)
	}
}

// TestARefusedInviteComesBackWithWhatWasTyped.
//
// The other half, in the handler: the redirect has to carry the draft, or the
// page above has nothing to redraw.
func TestARefusedInviteComesBackWithWhatWasTyped(t *testing.T) {
	d := newDirectory(t)
	c := &console{directory: d.client(t)}

	// No role ticked, which is the likeliest refusal and the cheapest to fix.
	form := url.Values{"name": {"Luscus"}, "username": {"luscus"}, "email": {"luscus@example.org"}}
	r := httptest.NewRequest(http.MethodPost, "/settings/people",
		strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("parse the form: %v", err)
	}
	recorder := httptest.NewRecorder()
	c.invite(recorder, r, &auth.Identity{Username: "dominique"})

	where, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse the redirect: %v", err)
	}
	query := where.Query()
	if query.Get("open") == "" {
		t.Error("the panel was not reopened, so the refusal is announced over an empty form")
	}
	for field, want := range map[string]string{
		"name": "Luscus", "username": "luscus", "email": "luscus@example.org",
	} {
		if query.Get(field) != want {
			t.Errorf("%s = %q, want %q", field, query.Get(field), want)
		}
	}
}

// TestASuccessfulInviteDoesNotReopenTheForm.
//
// A panel that came back prefilled after somebody was created is an invitation
// to create them twice — and the account's one-time link is on the page behind
// it, which is the thing to read.
func TestASuccessfulInviteDoesNotReopenTheForm(t *testing.T) {
	d := newDirectory(t)
	c := &console{directory: d.client(t), curatorGroup: "uuid-curators"}

	where, err := url.Parse(invited(t, c).Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse the redirect: %v", err)
	}
	if where.Query().Get("open") != "" {
		t.Error("the add panel reopened over the link that has to be read")
	}
}

// TestAForcedPanelCanStillBeClosed.
//
// A `:target` panel closes by changing the fragment — but the server does not
// open it with a fragment, it opens it with a class, and no fragment removes a
// class. So a close link that only changed the fragment did nothing at all:
// a refused invite left the panel pinned open over the page, with its own
// close button inert.
//
// Once forced open, closing has to be a fresh request for the page without the
// query that reopened it.
func TestAForcedPanelCanStillBeClosed(t *testing.T) {
	renderer, err := web.NewRenderer(assets, "templates/shared/*.html", "templates/pages/*.html")
	if err != nil {
		t.Fatalf("parse the console templates: %v", err)
	}
	render := func(data peoplePage) string {
		recorder := httptest.NewRecorder()
		renderer.Render(recorder, http.StatusOK, "people", data)
		return recorder.Body.String()
	}

	forced := render(peoplePage{
		page: page{Page: web.Page{Lang: "en"}}, AddOpen: true, Error: "tick at least one role",
	})
	if strings.Contains(forced, `href="#people"`) {
		t.Error("the close control is a fragment, which cannot remove the class holding it open")
	}
	if !strings.Contains(forced, `href="/settings/people"`) {
		t.Error("nothing closes a panel the server forced open")
	}

	// And the ordinary case stays a fragment: no round trip to shut a panel
	// the browser opened by itself.
	opened := render(peoplePage{page: page{Page: web.Page{Lang: "en"}}})
	if !strings.Contains(opened, `href="#people"`) {
		t.Error("closing an untouched panel costs a request it does not need")
	}
}

// TestAnAccountThatAlreadyExistsIsAdoptedRatherThanRefused.
//
// The directory belongs to the operator and people are already in it, so
// naming a colleague who has an authentik account is the ordinary case — not
// an error. It used to hand back authentik's own "user with this username
// already exists" and leave an admin with nowhere to go: they cannot create
// the account, and the person is not on the page to be given a role, because
// the page lists only staff.
func TestAnAccountThatAlreadyExistsIsAdoptedRatherThanRefused(t *testing.T) {
	d := newDirectory(t)
	d.existingUsers = []any{map[string]any{
		"pk": 42, "username": "luscus", "name": "Luscus", "is_active": true}}
	c := &console{directory: d.client(t), curatorGroup: "uuid-curators"}

	where := invited(t, c).Header().Get("Location")
	if strings.Contains(where, "error=") {
		t.Errorf("location = %q, want an existing account adopted", where)
	}
	if !strings.Contains(where, "notice=") {
		t.Error("nothing said which of create-or-adopt happened")
	}
	if _, created := d.bodies["POST /api/v3/core/users/"]; created {
		t.Error("a second account was created for somebody who already exists")
	}

	var granted bool
	for _, call := range d.seen {
		if strings.HasSuffix(call, "/add_user/") {
			granted = true
		}
		if strings.HasSuffix(call, "/recovery/") {
			t.Error("a way in was minted for somebody who already has a credential — " +
				"that is offering to reset a colleague, not onboarding them")
		}
	}
	if !granted {
		t.Errorf("calls = %v, want the roles granted", d.seen)
	}
}

// TestNoWayInIsMintedForSomebodyWhoRunsTheDirectory.
//
// A recovery link lets whoever holds it set that account's credential, and
// this console mints them with a token belonging to whoever provisioned it —
// usually a directory superuser. So without this an **admin of this register**
// could name `akadmin` and be handed the operator's whole authentik. Running a
// curation queue is not the same job as administering the directory the
// register borrows its identities from.
func TestNoWayInIsMintedForSomebodyWhoRunsTheDirectory(t *testing.T) {
	d := newDirectory(t)
	d.superuser = true
	c := &console{directory: d.client(t)}

	form := url.Values{"pk": {"5"}, "username": {"akadmin"}, "relink": {"1"}}
	r := httptest.NewRequest(http.MethodPost, "/settings/people",
		strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("parse the form: %v", err)
	}
	recorder := httptest.NewRecorder()
	c.relink(recorder, r, &auth.Identity{Username: "dominique"})

	where := recorder.Header().Get("Location")
	if !strings.Contains(where, "error=") {
		t.Errorf("location = %q, want the refusal", where)
	}
	for _, call := range d.seen {
		if strings.HasSuffix(call, "/recovery/") {
			t.Fatal("a register admin minted a way into the directory itself")
		}
	}
}
