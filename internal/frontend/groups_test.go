package frontend

import (
	"strings"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/apiclient"
)

// TestGroupPagesEscape covers the pages a group's own words reach.
//
// A group name and description are stranger-supplied text like a doléance,
// and they are rendered before a curator has seen them: the management page
// shows them to whoever holds the token, and the public page shows an accepted
// group to everybody. The same guarantee as the register applies — markup in,
// text out — and it cannot depend on the assessment having run.
func TestGroupPagesEscape(t *testing.T) {
	for name, tc := range payloads {
		t.Run(name, func(t *testing.T) {
			public := renderPage(t, "group", groupPage{
				page:        testPage(t),
				Name:        tc.payload,
				Description: tc.payload,
				Place:       tc.payload,
			})
			assertNeutralised(t, public, tc.payload, tc.marker)

			managed := renderPage(t, "manage", managePage{
				page:  testPage(t),
				ID:    "a-group",
				Name:  tc.payload,
				Descr: tc.payload,
				Place: tc.payload,
				// A member's name is stranger-supplied too, and so is a
				// message somebody wrote to the group — which is the first
				// private text this project renders, and gets the same
				// guarantee as everything public.
				Members:  []apiclient.Member{{AccountID: "an-account", Name: tc.payload}},
				Messages: []apiclient.GroupMessage{{ID: "a-message", From: tc.payload, Text: tc.payload}},
			})
			assertNeutralised(t, managed, tc.payload, tc.marker)

			// The rejected form carries the draft back, which is the one
			// place a group's text is rendered into form attributes rather
			// than into the body.
			draft := renderPage(t, "propose", proposePage{
				page: testPage(t),
				Draft: apiclient.GroupDraft{
					Name:        tc.payload,
					Description: tc.payload,
					Place:       tc.payload,
				},
				Error: tc.payload,
			})
			assertNeutralised(t, draft, tc.payload, tc.marker)
		})
	}
}

// TestAPublishedGroupSaysItsEditIsReviewed.
//
// The form is offered — a published group can be edited now — but what it does
// is different: the change waits on a decision while the map carries on saying
// the old thing. Somebody who is not told that goes and looks at the map.
func TestAPublishedGroupSaysItsEditIsReviewed(t *testing.T) {
	rendered := renderPage(t, "manage", managePage{
		page:    testPage(t),
		ID:      "a-group",
		Name:    "Assemblée de Vierzon",
		Pending: false,
	})

	if !strings.Contains(rendered, `<form method="post"`) {
		t.Fatal("a published group is offered no way to change anything")
	}
	if !strings.Contains(rendered, "stays exactly as it is on the map") {
		t.Error("the page does not say the group is unchanged while the edit waits")
	}
}

// TestTheInboxIsWhereAGroupIsReached.
//
// This replaced the contact address. The register holds no way to contact
// anybody, so reaching a group happens inside it — and an admin who cannot see
// what arrived is a group that reads as unreachable.
func TestTheInboxIsWhereAGroupIsReached(t *testing.T) {
	rendered := renderPage(t, "manage", managePage{
		page: testPage(t),
		ID:   "a-group",
		Messages: []apiclient.GroupMessage{
			{ID: "unread", From: "Dominique", Text: "La réunion de mardi tient-elle ?"},
			{ID: "read", From: "Camille", Text: "Merci", Read: true},
		},
		Unread: 1,
	})

	if !strings.Contains(rendered, "La réunion de mardi tient-elle ?") {
		t.Error("a message to the group is not shown to its admins")
	}
	// Only the unread one offers the control, and it is marked as unread.
	if !strings.Contains(rendered, `name="read" value="unread"`) {
		t.Error("an unread message cannot be marked read")
	}
	if strings.Contains(rendered, `name="read" value="read"`) {
		t.Error("a message already read is offered a control that does nothing")
	}
	if strings.Count(rendered, "card unread") != 1 {
		t.Error("the wrong number of messages are marked unread")
	}
}

// TestNoAddressReachesTheManagementPage.
//
// The old management page was the only view in the product that showed email
// addresses, which is why it had a table of them. There are none to show now,
// and this is what notices if one ever comes back: an account is a chosen name
// and nothing else, so a member list with an address in it would mean the
// account model had grown one.
func TestNoAddressReachesTheManagementPage(t *testing.T) {
	rendered := renderPage(t, "manage", managePage{
		page: testPage(t),
		ID:   "a-group",
		Members: []apiclient.Member{
			{AccountID: "one", Name: "Camille", Role: "admin"},
			{AccountID: "two", Name: "Dominique"},
		},
		Messages: []apiclient.GroupMessage{{ID: "a-message", From: "Dominique", Text: "Bonjour"}},
	})

	if strings.Contains(rendered, "@") {
		t.Error("something that looks like an address reached the management page")
	}
}

// TestJoiningIsAButton.
//
// Joining used to be a name, an address, a six-digit code and a form to type it
// back into — all of it to establish one thing, that the address was real,
// because the address *was* the identity. An account has proved itself with a
// passkey and carries no address to prove, so what is left is a press.
func TestJoiningIsAButton(t *testing.T) {
	rendered := renderPage(t, "group", groupPage{
		page: signedInPage(t),
		ID:   "a-group",
		Name: "Assemblée de Vierzon",
	})

	if !strings.Contains(rendered, `action="/groups/a-group/join"`) {
		t.Error("there is no way to join the group")
	}
	// Nothing is asked for, which is the whole point.
	for _, gone := range []string{`name="email"`, `name="code"`, `name="name"`} {
		if strings.Contains(rendered, gone) {
			t.Errorf("the join form still asks for %s", gone)
		}
	}
}

// TestAMemberIsOfferedTheWayOut, and never both at once: a page showing "join"
// to somebody who has joined is a page that has not been told who is reading
// it.
func TestAMemberIsOfferedTheWayOut(t *testing.T) {
	member := renderPage(t, "group", groupPage{
		page:   signedInPage(t),
		ID:     "a-group",
		Name:   "Assemblée de Vierzon",
		Member: true,
	})

	if !strings.Contains(member, `action="/groups/a-group/leave"`) {
		t.Error("a member has no way to leave")
	}
	if strings.Contains(member, `action="/groups/a-group/join"`) {
		t.Error("a member is still invited to join")
	}

	stranger := renderPage(t, "group", groupPage{
		page: testPage(t), ID: "a-group", Name: "Assemblée de Vierzon",
	})
	if strings.Contains(stranger, `action="/groups/a-group/leave"`) {
		t.Error("somebody who has not joined is offered a way to leave")
	}
}

// TestOnlyAnAdminIsOfferedTheManagementPage. The backend refuses anybody else
// with a 404, and offering a link that 404s is offering a puzzle.
func TestOnlyAnAdminIsOfferedTheManagementPage(t *testing.T) {
	admin := renderPage(t, "group", groupPage{
		page: signedInPage(t), ID: "a-group", Name: "Assemblée de Vierzon",
		Member: true, Role: "admin",
	})
	if !strings.Contains(admin, `href="/groups/a-group/manage"`) {
		t.Error("an admin is not offered the management page")
	}

	member := renderPage(t, "group", groupPage{
		page: signedInPage(t), ID: "a-group", Name: "Assemblée de Vierzon",
		Member: true,
	})
	if strings.Contains(member, `href="/groups/a-group/manage"`) {
		t.Error("an ordinary member is offered a page the backend will refuse")
	}
}

// TestWritingToAGroupSaysNothingAboutDelivery.
//
// Whether an admin has a live push subscription is a fact about that person's
// devices, and a sentence that varied with it would leak one. One sentence,
// always the same.
func TestWritingToAGroupSaysNothingAboutDelivery(t *testing.T) {
	rendered := renderPage(t, "group", groupPage{
		page: signedInPage(t), ID: "a-group", Name: "Assemblée de Vierzon",
		Sent: true,
	})

	if !strings.Contains(rendered, `action="/groups/a-group/write"`) {
		t.Error("there is no way to write to the group")
	}
	for _, leak := range []string{"notified", "notifié", "benachrichtigt"} {
		if strings.Contains(strings.ToLower(rendered), leak) {
			t.Errorf("the page claims something about delivery: %q", leak)
		}
	}
}

// TestGroupPageEscapesAMessageDraft. A refused message renders back what a
// stranger typed, into a form field.
func TestGroupPageEscapesAMessageDraft(t *testing.T) {
	for name, tc := range payloads {
		t.Run(name, func(t *testing.T) {
			rendered := renderPage(t, "group", groupPage{
				page:  testPage(t),
				ID:    "a-group",
				Name:  "Assemblée de Vierzon",
				Draft: tc.payload,
				Error: tc.payload,
			})
			assertNeutralised(t, rendered, tc.payload, tc.marker)
		})
	}
}

// TestAnAdminCanBeUnmade. A privileged grant with no way back is a trap the
// first time somebody mis-clicks, and taking the role away must not take the
// membership with it.
func TestAnAdminCanBeUnmade(t *testing.T) {
	rendered := renderPage(t, "manage", managePage{
		page: testPage(t),
		ID:   "a-group",
		Members: []apiclient.Member{
			{AccountID: "one", Name: "Camille", Role: "admin"},
			{AccountID: "two", Name: "Dominique"},
		},
	})

	// The admin is offered the way back, the ordinary member the way up, and
	// neither is offered both.
	if !strings.Contains(rendered, `value="one">`) || !strings.Contains(rendered, `value="two">`) {
		t.Fatal("a member is missing from the table")
	}
	if strings.Count(rendered, `name="role" value="admin"`) != 1 {
		t.Error("promotion is offered to the wrong number of people")
	}
	if strings.Count(rendered, `name="role" value=""`) != 1 {
		t.Error("demotion is offered to the wrong number of people")
	}
}

// TestThePublicPageSaysNothingAboutWhoJoined is the line between the two
// views. A group page is an invitation to a place; who is already there is the
// group's own business, and a public list of them would be a list of people
// who joined something political, at a public address, for ever.
func TestThePublicPageSaysNothingAboutWhoJoined(t *testing.T) {
	rendered := renderPage(t, "group", groupPage{
		page:   signedInPage(t),
		ID:     "a-group",
		Name:   "Assemblée de Vierzon",
		Member: true,
	})

	for _, leak := range []string{"Camille", "Dominique", "@"} {
		if strings.Contains(rendered, leak) {
			t.Errorf("the public group page leaks %q", leak)
		}
	}
}

// TestTheWritePanelIsClosedUntilAskedFor. It opens on a fragment the server
// never sees, so the default has to be closed — a panel that rendered open
// would cover the group every time somebody followed a link to it.
func TestTheWritePanelIsClosedUntilAskedFor(t *testing.T) {
	rendered := renderPage(t, "group", groupPage{
		page: testPage(t),
		ID:   "a-group",
		Name: "Assemblée de Vierzon",
	})

	if strings.Contains(rendered, `class="overlay open"`) {
		t.Error("the write panel renders open by default")
	}
	if !strings.Contains(rendered, `href="#write-panel"`) {
		t.Error("there is no way to open the write panel")
	}
}

// TestARefusedMessageReopensThePanelWithItsText. A refusal costs a correction,
// never what somebody wrote — and the server has to open the panel itself,
// because a fragment never reaches it.
func TestARefusedMessageReopensThePanelWithItsText(t *testing.T) {
	rendered := renderPage(t, "group", groupPage{
		page:  signedInPage(t),
		ID:    "a-group",
		Name:  "Assemblée de Vierzon",
		Draft: "La réunion de mardi tient-elle ?",
		Error: "That cannot be sent.",
	})

	if !strings.Contains(rendered, `class="overlay open"`) {
		t.Error("a refused message left the panel shut")
	}
	if !strings.Contains(rendered, "La réunion de mardi tient-elle ?") {
		t.Error("the draft was lost")
	}
}

// TestTheActionsTableOffersWhatEachRowNeeds. A retired recurring action is the
// only one that can be vouched for — a date that has passed cannot be argued
// with, and offering to confirm it would be offering to change the past.
func TestTheActionsTableOffersWhatEachRowNeeds(t *testing.T) {
	rendered := renderPage(t, "manage", managePage{
		page: testPage(t),
		ID:   "a-group",
		Actions: []apiclient.Action{
			{ID: "live", Title: "Réunion mensuelle", Type: "recurrent",
				Parts: &apiclient.RecurrenceInfo{Key: "recur.monthly_weekday", Week: 1,
					Weekdays: []int{2}}, NextOn: "2026-10-06T19:00", Status: "accepted"},
			{ID: "over", Title: "Manifestation", Type: "onetime",
				StartsOn: "2026-01-01", Status: "accepted", Retired: true},
			{ID: "stale", Title: "Ancienne réunion", Type: "recurrent",
				Parts: &apiclient.RecurrenceInfo{Key: "recur.monthly_weekday", Week: 1,
					Weekdays: []int{2}}, Status: "accepted", Retired: true},
		},
	})

	if strings.Count(rendered, `name="confirm"`) != 1 {
		t.Error("the confirm control is offered to the wrong number of actions")
	}
	if !strings.Contains(rendered, `name="confirm" value="stale"`) {
		t.Error("the unconfirmed rhythm is not the one offered a confirmation")
	}
	// Every row can be edited and called off.
	for _, id := range []string{"live", "over", "stale"} {
		if !strings.Contains(rendered, `href="#edit-action-`+id+`"`) {
			t.Errorf("%s has no edit control", id)
		}
		if !strings.Contains(rendered, `name="delete" value="`+id+`"`) {
			t.Errorf("%s has no way to be called off", id)
		}
	}
}

// TestARefusedAnnouncementKeepsWhatWasTyped, and opens the panel itself: a
// fragment never reaches the server.
func TestARefusedAnnouncementKeepsWhatWasTyped(t *testing.T) {
	rendered := renderPage(t, "manage", managePage{
		page:       testPage(t),
		ID:         "a-group",
		ActionOpen: true,
		ActionDraft: apiclient.ActionDraft{
			Title: "Demo am Rathaus", Type: "onetime",
			Description: "Kundgebung gegen die Schließung",
		},
		Error: "A one-off action needs a date.",
	})

	if !strings.Contains(rendered, `class="overlay open"`) {
		t.Error("a refused announcement did not reopen the panel")
	}
	for _, want := range []string{`value="Demo am Rathaus"`, "Kundgebung gegen die Schließung"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the draft lost %q", want)
		}
	}
}

// TestTheGroupPageNeverShowsAVerdict. What a machine made of somebody's
// meeting is for whoever manages the group and whoever curates it; a reader
// deciding whether to turn up has no business with the score.
func TestTheGroupPageNeverShowsAVerdict(t *testing.T) {
	rendered := renderPage(t, "group", groupPage{
		page: testPage(t),
		ID:   "a-group",
		Name: "Assemblée de Vierzon",
		Actions: []apiclient.Action{{
			ID: "one", Title: "Réunion mensuelle", Type: "recurrent",
			Parts: &apiclient.RecurrenceInfo{Key: "recur.monthly_weekday", Week: 1,
				Weekdays: []int{2}}, NextOn: "2026-10-06T19:00", Status: "accepted",
			Assessed: true, Confidence: 95, Reason: "une vraie réunion",
		}},
	})

	if !strings.Contains(rendered, "Réunion mensuelle") {
		t.Fatal("the action is not shown at all")
	}
	for _, leak := range []string{"95", "une vraie réunion", "accepted"} {
		if strings.Contains(rendered, leak) {
			t.Errorf("the public page leaked %q", leak)
		}
	}
}

// TestActionsAreEscaped. An announcement is stranger text on a public page.
func TestActionsAreEscaped(t *testing.T) {
	for name, tc := range payloads {
		t.Run(name, func(t *testing.T) {
			rendered := renderPage(t, "group", groupPage{
				page: testPage(t),
				ID:   "a-group",
				Name: "Assemblée de Vierzon",
				Actions: []apiclient.Action{{
					ID: "one", Title: tc.payload, Description: tc.payload,
					Note: tc.payload, Place: tc.payload, Type: "recurrent",
				}},
			})
			assertNeutralised(t, rendered, tc.payload, tc.marker)
		})
	}
}

// TestEveryCardIsAWayIntoItsDoleance. A card shows an excerpt, so it has to
// offer the rest — and the offer is the whole card, not a link somebody has
// to find.
func TestEveryCardIsAWayIntoItsDoleance(t *testing.T) {
	rendered := renderCard(t, messageCard{
		ID:             "a-doleance",
		Text:           "La maternité a fermé",
		Truncated:      true,
		Likes:          3,
		AnonymousLabel: "Anonyme",
	})

	for _, want := range []string{
		`class="card-excerpt" href="/doleance/a-doleance"`, // the words themselves
		`action="/doleance/a-doleance/like"`,               // me too
		`href="/doleance/a-doleance"`,                      // the permalink
		`data-like="a-doleance"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the card is missing %s", want)
		}
	}
	// The cut is admitted where it happened, or the card reads as a mistake.
	if !strings.Contains(rendered, "card-more") {
		t.Error("a truncated card does not say it was cut")
	}
	if !strings.Contains(rendered, ">3<") {
		t.Error("the like count is not shown")
	}
}

// TestAWholeDoleanceIsNotACardIntoItself. On the page a card links to, the
// card is the destination: no cover link, and nothing saying "more".
func TestAWholeDoleanceIsNotACardIntoItself(t *testing.T) {
	rendered := renderPage(t, "permalink", permalinkPage{
		page: testPage(t),
		Message: messageCard{
			ID:             "a-doleance",
			Text:           "La maternité a fermé",
			Full:           true,
			AnonymousLabel: "Anonyme",
		},
	})

	if strings.Contains(rendered, "card-excerpt") {
		t.Error("the doléance's own page links its text to itself")
	}
	if strings.Contains(rendered, "card-more") {
		t.Error("the whole text is marked as cut")
	}
	// The controls stay: this is still where somebody says "me too".
	if !strings.Contains(rendered, `action="/doleance/a-doleance/like"`) {
		t.Error("the destination page lost its controls")
	}
}

// TestAnUncutCardOffersNoMore. "[more]" on text that is already whole is a
// promise of something that is not there.
func TestAnUncutCardOffersNoMore(t *testing.T) {
	rendered := renderCard(t, messageCard{
		ID:             "a-doleance",
		Text:           "Le bus ne passe plus le dimanche.",
		AnonymousLabel: "Anonyme",
	})

	if strings.Contains(rendered, "card-more") {
		t.Error("an uncut card offers more than it has")
	}
}

// TestNobodyIsInvitedToWriteSomethingThatWillBeThrownAway.
//
// Writing to a group needs an account. The panel used to offer the textarea to
// everybody, so an anonymous reader wrote their message, pressed send, and was
// redirected to the sign-in page — with the words gone and nothing said about
// it. The POST never reached the backend at all.
//
// A refusal is allowed to cost a correction and is never allowed to cost what
// somebody wrote, and the cheapest way to keep that promise is not to ask for
// the writing until it can be sent.
func TestNobodyIsInvitedToWriteSomethingThatWillBeThrownAway(t *testing.T) {
	anonymous := renderPage(t, "group", groupPage{
		page: testPage(t),
		ID:   "a-group",
		Name: "Assemblée de Vierzon",
	})

	if strings.Contains(anonymous, `action="/groups/a-group/write"`) {
		t.Error("a reader who cannot post a message is offered the form anyway")
	}
	if strings.Contains(anonymous, `name="text"`) {
		t.Error("there is a textarea whose contents would be discarded")
	}
	// And the way on says where it goes, so signing in comes back to this
	// group rather than to a dashboard.
	if !strings.Contains(anonymous, `/account/signin?next=/groups/a-group`) {
		t.Error("there is no way on that returns to this group")
	}

	// Signed in, the form is there.
	signedIn := renderPage(t, "group", groupPage{
		page: signedInPage(t),
		ID:   "a-group",
		Name: "Assemblée de Vierzon",
	})
	if !strings.Contains(signedIn, `action="/groups/a-group/write"`) {
		t.Error("somebody who can write is not offered the form")
	}
}
