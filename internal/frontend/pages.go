package frontend

import (
	"net/http"

	"github.com/CoderSyndicate/doleances/internal/apiclient"
	"github.com/CoderSyndicate/doleances/internal/web"
)

// page is what every frontend template receives.
type page struct {
	web.Page

	// UsesMap makes the layout load Leaflet. Pages that show no map should
	// not pay for the library.
	UsesMap bool

	// InlinesFooter says this page carries the footer's content in its own
	// body, so the layout must not repeat it underneath. The about page does:
	// "open, and checkable" is part of the explanation there rather than a
	// coda to it, and the same paragraph twice on one page reads as a
	// template that forgot.
	InlinesFooter bool

	// AssetVersion is appended to every /static URL, so a changed stylesheet
	// is fetched rather than served from cache.
	AssetVersion string

	// Country seeds the map's starting view. It is a guess from the browser's
	// language, shown at country level precisely because it is a guess.
	Country string

	// SignedIn is whether this request carries a session cookie.
	//
	// **It costs nothing**, which is the whole reason the navigation can use
	// it: it reads a cookie and asks the backend nothing. What it cannot say
	// is whether that session is still *valid* — only the backend knows that,
	// and asking would put a call on every page of a site most readers use
	// without an account.
	//
	// The consequence is benign and worth naming: a reader whose session
	// expired sees "sign out" until they press it, and pressing it does
	// exactly the right thing — the cookie goes, and the backend refusing a
	// token it has already forgotten changes nothing.
	//
	// It decides what the navigation offers and never what anybody may do.
	SignedIn bool

	// Groups is whether this deployment offers local action groups.
	//
	// When it does not, every trace of them goes: the navigation entry, the
	// pages, the links. A register that only collects doléances should not
	// advertise a map that will always be empty and a form that answers 404 —
	// a switched-off feature that is still visible reads as a broken one.
	Groups bool
}

func (s *site) newPage(r *http.Request, titleKey string) page {
	return page{
		Page:         web.NewPage(r, s.localization, titleKey),
		AssetVersion: s.assetVersion,
		Country:      web.CountryFromRequest(r),
		SignedIn:     signedIn(r),
		Groups:       s.groups,
	}
}

// messageCard is one doléance as the templates render it.
type messageCard struct {
	// ID is what the card links to. Every card is a way into the doléance it
	// shows, because a card is an excerpt and the excerpt is not the point.
	ID string

	Nickname string
	Place    string
	Date     string

	// Text is what the card shows: the excerpt on a listing, the whole thing
	// on the doléance's own page. Truncated says which, so the card can offer
	// the rest rather than leaving somebody to wonder whether a sentence
	// ended oddly or was cut.
	Text      string
	Truncated bool

	Subjects []string

	// Latitude and Longitude are the coarsened cell the contributor pinned,
	// or nothing at all when they named a country or a region. The template
	// does not read them; the map does, and both are drawn from one list.
	Latitude  float64
	Longitude float64

	// Likes is how many readers said this happened to them too.
	Likes int

	// Full suppresses the card's own chrome — the link over it and the
	// controls under it — on the page that *is* the doléance. A card there is
	// not a way in; it is the destination.
	Full bool

	// AnonymousLabel is what stands in for a name when there is none — most
	// of the time, because anonymity is the default.
	AnonymousLabel string

	// Verified says a human read this doléance and let it stand. It decides
	// which of two things the card offers: a way to ask for a person, or the
	// mark saying one came.
	//
	// Most of the register is false, and that is the honest state rather than
	// a gap — a doléance published on a score above the accept threshold has
	// been read by nobody, and the card says so instead of implying a review
	// that did not happen.
	Verified bool

	// Kept says whether the signed-in reader has this in their own list, so
	// the control can offer the thing they have not done rather than a
	// toggle whose state they have to infer.
	//
	// It is false for everybody else, which is most readers, and the template
	// shows nothing at all then — a button that demanded an account before it
	// would do anything would be a worse page than no button.
	Kept bool
}

// historicalCard is one passage from an earlier register.
type historicalCard struct {
	// ID is what the permalink points at, and what a like is recorded
	// against.
	ID string

	// Likes is how many readers recognised themselves in it.
	Likes int

	// Full suppresses the card's own chrome on the page that *is* the
	// passage, the same way a doléance's card does.
	Full bool

	Title string

	// DocumentTitle sits in the header beside the date: what this is, before
	// what it says.
	DocumentTitle string

	Period string
	Region string
	Text   string

	// Latitude and Longitude are the commune the document was written for.
	// A passage the corpus did not place carries none, keeps its Region in
	// words, and sits on no point of the map.
	Latitude  float64
	Longitude float64

	// Source is the citation. A passage from someone who cannot correct the
	// record is shown with its provenance or not at all.
	Source string

	// TranslatedFrom names the language the passage was written in, and is
	// set only when the reader is seeing a translation. A translated text
	// shown as if it were the original misrepresents what was said.
	TranslatedFrom string

	// Placeholder marks seeded example content, so it can never be mistaken
	// for real archival material.
	Placeholder      bool
	PlaceholderLabel string

	// Kept is the same fact a doléance's card carries, for the same reason:
	// the two registers are one act performed twice, and a control on one
	// card and not the one beside it would say otherwise.
	Kept bool
}

// corpusWindow is a slice of the historical corpus plus the links that move
// through it. The sequence is fixed for the lifetime of the process, so ‹ and
// › are reversible and a shared link shows what its sender saw.
type corpusWindow struct {
	Texts []historicalCard

	// Position is the window's index, one-based, for "3 / 5".
	Position int
	// Total is how many windows the corpus makes.
	Total int

	// Previous and Next are the offsets the arrows point at. They wrap, so
	// the register has no dead end at either end.
	Previous int
	Next     int
}

// HasMore reports whether there is anything to page through.
func (w corpusWindow) HasMore() bool { return w.Total > 1 }

// aboutPage is prose and a way in, and nothing else.
//
// It used to be the landing page and carried a handful of doléances and a
// window onto the corpus beneath its explanation. Both are gone: the register
// is the landing page now, so the doléances are the first thing anybody sees
// rather than a sample under an essay, and the passages are mixed in among
// them rather than filed in a section of their own.
type aboutPage struct {
	page
}

type doleancePage struct {
	page
	Examples corpusWindow

	// BirthYears fills the year selector. It is computed rather than written
	// into the template so the list does not quietly go stale as years pass.
	BirthYears []int
}

// registerPage is the register, which is this site's front page.
type registerPage struct {
	page
	Subjects []apiclient.Subject

	// Entries are the doléances and the passages from 1789 in one list,
	// mixed rather than sectioned. See mix.go for the rules.
	Entries []registerEntry

	Place string
}

// HasMessages reports whether any doléance is on this page.
//
// Not whether any entry is: the page fills itself with passages from 1789
// when a viewport holds nothing, and the line saying nothing has been written
// here is still true when it does.
func (p registerPage) HasMessages() bool {
	for _, entry := range p.Entries {
		if entry.Message != nil {
			return true
		}
	}
	return false
}

// aroundPage is the map of groups and what they have announced.
//
// It carries no subject list. There was a filter here and it did nothing:
// `/api/groups` reads the viewport and nothing else, so the subjects the form
// submitted were ignored and the map answered identically whatever was ticked.
//
// Removing it is more than deleting dead wiring. A group is not about a
// subject — people who meet about the buses end up talking about the surgery
// and the post office — so filtering a map of groups by subject asks a
// question groups do not have an answer to. The register is where subjects
// belong, because a doléance is one text about one thing.
type aroundPage struct {
	page
}

type voicesPage struct {
	page

	// Texts is the whole corpus in one shuffled grid. It was grouped under a
	// heading per language, which sorted it by the property a reader is least
	// likely to be looking for — see historicalShuffled.
	Texts []historicalCard
}
