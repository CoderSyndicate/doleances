package frontend

import (
	"html"
	"regexp"
	"strings"
	"testing"
)

func cardOn(t *testing.T, card messageCard) string {
	t.Helper()

	return renderPage(t, "register", registerPage{page: testPage(t), Entries: []registerEntry{{Message: &card}}})
}

// TestAnUnverifiedDoleanceOffersAWayToAskForAPerson.
//
// Most of the register is unverified, because a score above the accept
// threshold publishes with nobody involved. That is the state the control
// exists for.
func TestAnUnverifiedDoleanceOffersAWayToAskForAPerson(t *testing.T) {
	rendered := cardOn(t, messageCard{
		ID: "m1", Text: "Le bus ne passe plus le dimanche.", AnonymousLabel: "Anonyme",
	})

	if !strings.Contains(rendered, `href="/doleance/m1/report"`) {
		t.Error("an unverified doléance offers no way to ask for a person")
	}
	// A link, never a form. Nothing on a card may take a published doléance
	// off the register in one press, and a GET also means no prefetcher, link
	// scanner or mis-click can report anything at all.
	if strings.Contains(rendered, `action="/doleance/m1/report"`) {
		t.Error("the card reports in one press instead of asking first")
	}
	if strings.Contains(rendered, "card-action verified") {
		t.Error("an unverified doléance is marked as read by a human")
	}
	// Beside the other two, in the one row, which is where a reader looks for
	// what they can do about a card.
	if !strings.Contains(rendered, "/doleance/m1/like") ||
		!strings.Contains(rendered, `href="/doleance/m1"`) {
		t.Error("the report control displaced the like or the permalink")
	}
}

// TestAVerifiedDoleanceShowsTheMarkAndOffersNothing.
//
// Two things, and the second matters as much: a doléance a curator has already
// let stand must not offer to send the same text back to the same person. The
// store refuses it too, because an interface is not an authorisation.
func TestAVerifiedDoleanceShowsTheMarkAndOffersNothing(t *testing.T) {
	rendered := cardOn(t, messageCard{
		ID: "m1", Text: "La maternité a fermé.", AnonymousLabel: "Anonyme", Verified: true,
	})

	if !strings.Contains(rendered, "card-action verified") {
		t.Error("a verified doléance does not carry the mark")
	}
	if strings.Contains(rendered, "/doleance/m1/report") {
		t.Error("a verified doléance still offers to report it")
	}
	// A fact rather than a control: no form and no button around the mark.
	if strings.Contains(rendered, `<button class="card-action verified`) {
		t.Error("the mark is a button, which promises something pressing it cannot do")
	}
}

// TestTheMarkIsNotColourAlone. A reader who cannot tell green from grey has to
// be able to see which state a card is in, so the two use different shapes —
// a filled tick against an outlined flag — and both carry words.
func TestTheMarkIsNotColourAlone(t *testing.T) {
	verified := cardOn(t, messageCard{ID: "m1", Text: "…", Verified: true})
	unverified := cardOn(t, messageCard{ID: "m1", Text: "…"})

	for _, c := range []struct {
		what     string
		rendered string
		label    string
	}{
		{"verified", verified, "card.verified"},
		{"unverified", unverified, "card.report"},
	} {
		want := testPage(t).T(c.label)
		if !strings.Contains(c.rendered, want) {
			t.Errorf("the %s card does not say %q in words", c.what, want)
		}
	}

	// Different paths, so the two are distinguishable in the markup as well as
	// by eye.
	if strings.Contains(verified, "icon-flag") || strings.Contains(unverified, "M3 8.5l3.5") {
		t.Error("the two states draw the same shape")
	}
}

// TestAReporterIsToldWhatHappens, rather than being returned to a listing where
// the card they pressed has silently vanished.
func TestAReporterIsToldWhatHappens(t *testing.T) {
	page := testPage(t)

	reported := renderPage(t, "permalink", permalinkPage{
		page:     page,
		Message:  messageCard{ID: "m1", Text: "La poste ferme.", Full: true},
		Pending:  true,
		Reported: true,
	})
	if !strings.Contains(reported, page.T("permalink.reported")) {
		t.Error("the reader who reported it is not told a person will look")
	}
	// Not the note written for the author — "exactly as you wrote it" is
	// addressed to somebody this reader is not.
	if strings.Contains(reported, page.T("permalink.pending_note")) {
		t.Error("the reporter was shown the note written for the author")
	}

	// And an author arriving at their own queued doléance still gets theirs.
	waiting := renderPage(t, "permalink", permalinkPage{
		page:    page,
		Message: messageCard{ID: "m1", Text: "La poste ferme.", Full: true},
		Pending: true,
	})
	if !strings.Contains(waiting, page.T("permalink.pending_note")) {
		t.Error("the author's note went missing")
	}
	if strings.Contains(waiting, page.T("permalink.reported")) {
		t.Error("an author was told their own doléance had been reported")
	}
}

// TestTheIconsCarryNoVisibleLabelAndStillHaveNames.
//
// Four controls with words beside each had grown wider than the card — the
// permalink's label wrapped onto its own line. The words moved into
// `.visually-hidden`, which is the one way to take them off the screen without
// taking them away from a screen reader: the accessible name of every control
// is exactly what it was.
func TestTheIconsCarryNoVisibleLabelAndStillHaveNames(t *testing.T) {
	page := signedInPage(t)
	rendered := renderPage(t, "register", registerPage{
		page: page,
		Entries: []registerEntry{{Message: &messageCard{
			ID: "m1", Text: "La poste ferme.", AnonymousLabel: "Anonyme"}}},
	})

	// Every label is still in the markup, so the names survive.
	for _, key := range []string{"card.report", "card.keep", "card.permalink", "card.like"} {
		if !strings.Contains(rendered, page.T(key)) {
			t.Errorf("%s has no accessible name left", key)
		}
	}

	// And none of them is rendered as a visible label: each sits in a
	// visually-hidden span.
	for _, key := range []string{"card.report", "card.keep", "card.permalink"} {
		visible := `<span>` + page.T(key) + `</span>`
		if strings.Contains(rendered, visible) {
			t.Errorf("%s is still printed beside its icon", key)
		}
	}
}

// TestEveryControlCanSayWhatItIs. With the words gone, the tooltip is the only
// thing that answers "what is this icon?" — so every control has to carry one,
// and it has to be the one CSS draws rather than the browser's, whose delay
// nobody can shorten.
func TestEveryControlCanSayWhatItIs(t *testing.T) {
	rendered := renderPage(t, "register", registerPage{
		page: signedInPage(t),
		Entries: []registerEntry{{Message: &messageCard{
			ID: "m1", Text: "La poste ferme.", AnonymousLabel: "Anonyme"}}},
	})

	// Four controls on the row, four tips.
	if tips := strings.Count(rendered, "data-tip="); tips < 4 {
		t.Errorf("%d controls carry a tooltip, want the whole row", tips)
	}
	// And none of them falls back to the browser's, which would appear a
	// second later and sit on top of ours. Scoped to the controls themselves:
	// the page carries a `title` elsewhere — the theme toggle — and a search
	// of the whole document would fail on something that has nothing to do
	// with a card.
	withTitle := regexp.MustCompile(`<[^>]*class="card-action[^"]*"[^>]*title=`)
	if found := withTitle.FindString(rendered); found != "" {
		t.Errorf("a card control still uses the native title tooltip: %s", found)
	}
}

// TestReportingAsksBeforeItActs is the whole point of the confirmation.
//
// The control is an unlabelled icon beside three others and what it does is
// the one thing on the card that cannot happen by accident, so the page it
// leads to changes nothing: it shows the doléance, says what would happen, and
// keeps the only thing that acts behind a second, deliberate press.
func TestReportingAsksBeforeItActs(t *testing.T) {
	page := testPage(t)
	rendered := renderPage(t, "report", reportPage{
		page: page,
		Message: messageCard{
			ID: "m1", Text: "La poste du village est ouverte deux matinées par semaine.",
			AnonymousLabel: "Anonyme", Full: true,
		},
	})

	// It says nothing has happened, which is the first thing somebody who
	// mis-clicked needs to read.
	if !strings.Contains(rendered, page.T("report.lede")) {
		t.Error("the page does not say that nothing has happened yet")
	}
	// It shows the words the decision is about.
	if !strings.Contains(rendered, "La poste du village") {
		t.Error("the page asks about a doléance without showing it")
	}
	// The only thing that acts is a post, and it is the one form here.
	if !strings.Contains(rendered, `<form method="post" action="/doleance/m1/report">`) {
		t.Error("the confirmation does not post anywhere")
	}
	// And the way out is a plain link back to the doléance.
	if !strings.Contains(rendered, `href="/doleance/m1"`) {
		t.Error("there is no way off this page except confirming")
	}
	// It says what is not happening, too: nobody's words are being deleted.
	//
	// Compared escaped, because that is how it reaches the page: `html/template`
	// turns the apostrophe in "anybody's" into `&#39;`, and a test matching the
	// raw catalogue string would fail on the escaping working.
	if !strings.Contains(rendered, html.EscapeString(page.T("report.not"))) {
		t.Error("the page does not say what reporting does not do")
	}
}
