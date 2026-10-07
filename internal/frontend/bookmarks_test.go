package frontend

import (
	"strings"
	"testing"
)

func keptCardOn(t *testing.T, p page, card messageCard) string {
	t.Helper()

	return renderPage(t, "register", registerPage{page: p, Entries: []registerEntry{{Message: &card}}})
}

// TestNobodyIsOfferedAButtonThatWouldDemandAnAccount.
//
// The register asks nobody to sign in to read it, and keeping a text needs an
// account because a bookmark is a row held for one named person. The resolution
// is not a button that bounces an anonymous reader to a sign-in page: it is no
// button. A control that refuses the majority of this register's readers would
// be a worse page than one without it.
func TestNobodyIsOfferedAButtonThatWouldDemandAnAccount(t *testing.T) {
	card := messageCard{ID: "m1", Text: "Le bus ne passe plus le dimanche.", AnonymousLabel: "Anonyme"}

	anonymous := keptCardOn(t, testPage(t), card)
	if strings.Contains(anonymous, "/doleance/m1/keep") {
		t.Error("an anonymous reader was offered a control that needs an account")
	}
	// The "me too" is still there, because that one really does need nobody.
	if !strings.Contains(anonymous, "/doleance/m1/like") {
		t.Error("the anonymous reader lost the control that asks for no identity")
	}

	signedIn := keptCardOn(t, signedInPage(t), card)
	if !strings.Contains(signedIn, "/doleance/m1/keep") {
		t.Error("a signed-in reader was not offered the keep control")
	}
}

// TestTheControlOffersWhatHasNotBeenDoneYet.
//
// Two paths rather than one toggle, so a stale card cannot undo what somebody
// just did: a button saying "keep" posts a keep, whatever has happened since.
// A toggle at one address would mean a page left open in another tab silently
// reversing the press made in this one.
func TestTheControlOffersWhatHasNotBeenDoneYet(t *testing.T) {
	notYet := keptCardOn(t, signedInPage(t), messageCard{ID: "m1", Text: "Plus de médecin."})
	if !strings.Contains(notYet, "/doleance/m1/keep") {
		t.Error("a doléance nobody kept does not offer keeping it")
	}
	if strings.Contains(notYet, "/doleance/m1/release") {
		t.Error("a doléance nobody kept offers taking it back")
	}

	already := keptCardOn(t, signedInPage(t), messageCard{ID: "m1", Text: "Plus de médecin.", Kept: true})
	if !strings.Contains(already, "/doleance/m1/release") {
		t.Error("a kept doléance does not offer taking it back")
	}
	if strings.Contains(already, "/doleance/m1/keep\"") {
		t.Error("a kept doléance offers keeping it again")
	}
}

// TestAPassageCarriesTheSameControl. The two registers are one act performed
// twice, and the pages say so: a keep button on a doléance and none on the
// 1789 passage beside it would quietly take a position the rest of the design
// refuses.
func TestAPassageCarriesTheSameControl(t *testing.T) {
	rendered := renderPage(t, "voice", voicePage{
		page: signedInPage(t),
		Text: historicalCard{
			ID: "sjdl-deesses", Title: "Des déesses d'un autre genre",
			Text: "Nous étions à leurs yeux autant de déesses.", Full: true,
		},
	})
	if !strings.Contains(rendered, "/voices/sjdl-deesses/keep") {
		t.Error("a passage offers no way to keep it")
	}

	anonymous := renderPage(t, "voice", voicePage{
		page: testPage(t),
		Text: historicalCard{ID: "sjdl-deesses", Text: "…", Full: true},
	})
	if strings.Contains(anonymous, "/keep") {
		t.Error("an anonymous reader was offered a control that needs an account")
	}
}

// TestTheListShowsBothRegisters, which is the feature: one chronological list
// of what this reader set aside, whichever register each piece came from.
func TestTheListShowsBothRegisters(t *testing.T) {
	message := messageCard{
		ID: "m1", Text: "La maternité a fermé.", AnonymousLabel: "Anonyme", Kept: true,
	}
	passage := historicalCard{
		ID: "h1", Title: "Doléances du sexe", Text: "Nous demandons…", Kept: true,
	}

	rendered := renderPage(t, "bookmarks", bookmarksPage{
		page: signedInPage(t),
		Kept: []keptCard{
			{When: "4 March 2026", Message: &message},
			{When: "3 March 2026", Historical: &passage},
		},
	})

	for _, want := range []string{
		"La maternité a fermé.",
		"Doléances du sexe",
		// Every entry is kept by definition, so each offers taking it back
		// rather than keeping it again.
		"/doleance/m1/release",
		"/voices/h1/release",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the list does not show %q", want)
		}
	}
}

// TestAnEmptyListSaysWhereToStart rather than showing a blank page. Somebody
// who followed the link from their account page and found nothing has to be
// told what the button looks like and where it is.
func TestAnEmptyListSaysWhereToStart(t *testing.T) {
	rendered := renderPage(t, "bookmarks", bookmarksPage{page: signedInPage(t)})

	if !strings.Contains(rendered, "/register") || !strings.Contains(rendered, "/voices") {
		t.Error("an empty list offers no way into either register")
	}
}
