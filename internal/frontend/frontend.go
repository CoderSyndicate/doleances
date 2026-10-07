// Package frontend is the public site and the participant pages.
//
// It is an exposed service and holds no credentials beyond the backend API's:
// no database driver, no LLM key. Its pages are server-rendered templates with
// their assets embedded in the binary, so the running container needs no
// writable path at all.
package frontend

import (
	"context"
	"embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/CoderSyndicate/doleances/internal/apiclient"
	"github.com/CoderSyndicate/doleances/internal/cli"
	"github.com/CoderSyndicate/doleances/internal/config"
	"github.com/CoderSyndicate/doleances/internal/service"
	"github.com/CoderSyndicate/doleances/internal/web"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// Assets are the templates, translations and static files, compiled into the
// binary so the container needs no writable path and nothing to mount.
//
//go:embed templates/shared/*.html templates/pages/*.html static/* locales/*.toml
var assets embed.FS

// Default ports follow the house convention: 8xxx public, 7xxx internal
// tools, 9xxx maintenance. Every service uses a distinct pair so all three run
// side by side in development.
const (
	DefaultAppPort         = 8201
	DefaultMaintenancePort = 9201
)

// defaultBackendURL is where the backend listens by default.
const defaultBackendURL = "http://127.0.0.1:7300"

// fallbackLanguage is the language used when nothing the visitor offers is
// available.
const fallbackLanguage = "en"

// historicalPerPage is how many passages a window shows.
const historicalPerPage = 3

// registerMessages is how many entries the register shows at once.
//
// It is the landing page, so this is the first thing anybody sees of this
// project — which is the point: the register's whole claim is that a thousand
// private troubles read side by side are one shared condition, and an essay
// about that claim is not the same as the evidence for it.
const registerMessages = 40

// Definition describes the frontend binary.
func Definition() cli.Definition {
	return cli.Definition{
		Name:  "frontend",
		Title: "Doléances",
		Short: "The public doléances site",
		Long: "The frontend serves the register, the submission form, the map and the\n" +
			"historical texts, in every language the site is translated into.",
		DefaultAppPort:         DefaultAppPort,
		DefaultMaintenancePort: DefaultMaintenancePort,
		RegisterFlags: func(cmd *cobra.Command) {
			config.RegisterClientFlags(defaultBackendURL)(cmd)
			config.RegisterProjectFlags(cmd)
			// This service needs its own public address for one thing: the
			// device-linking code a phone scans has to be absolute.
			config.RegisterSiteFlags(cmd)
			// The same switch the backend reads, so one environment variable
			// shapes the whole deployment rather than each service being told
			// separately and one of them being forgotten.
			config.RegisterFeatureFlags(cmd)
			// This is the service abuse arrives at, and the only one that
			// knows a reader's own address.
			config.RegisterRateLimitFlags(cmd)
		},
		Setup: Setup,
	}
}

// Command returns the frontend's root command.
func Command(version, commit string) *cobra.Command {
	return cli.Build(Definition(), version, commit)
}

// site holds the frontend's dependencies.
type site struct {
	// assetVersion busts the browser cache when the embedded CSS or scripts
	// change. Without it a redeploy leaves visitors on the old stylesheet
	// until their cache expires.
	assetVersion string

	renderer     *web.Renderer
	localization *web.Localization
	backend      *apiclient.Client

	// project is where the source of this software lives. Resolved once at
	// startup and rendered into the page: it is fixed for the lifetime of a
	// deployment, so no request should pay to fetch it.
	project config.Project

	// groups is whether this deployment offers local action groups. Read once
	// at startup from the same setting the backend reads, so the two cannot
	// drift within one environment.
	groups bool

	// proxies is which hops may say who a request is from. Held on the site
	// rather than resolved per request so that the limiter in front and the
	// address forwarded to the backend can never answer differently.
	proxies web.TrustedProxies

	// order fixes the sequence the historical corpus is read in. It is drawn
	// once per process: random enough that the same few passages do not
	// dominate every deployment, stable enough that the arrows are reversible
	// and a shared link shows what its sender saw.
	order uint64

	// overlay serves the active theme's token overrides on top of the static
	// defaults.
	overlay *web.ThemeOverlay

	// assets serves the active theme's images, falling back to the built-in
	// ones.
	assets *web.ThemeAssetHandler

	// files serves the package files a theme's own tokens reference.
	files *web.ThemeFileHandler

	// siteURL is this register's own public address, used where a page has to
	// hand somebody an absolute one — the device-linking QR code being the
	// only such place. Absent, the QR is skipped and the page shows the path.
	siteURL string

	// secureCookies marks the session cookie TLS-only.
	//
	// Derived from siteURL rather than configured separately: a production
	// deployment told one thing and behaving another is exactly the mistake a
	// second setting invites, and the cost of getting it wrong is a session
	// token on the wire.
	secureCookies bool

	// development mirrors the --development flag.
	//
	// It no longer changes anything about groups. The tokenless management
	// bypass it used to open existed for one reason — the management token was
	// a hash nothing could recover, so the console could not build a link to a
	// page it was listing — and both the token and the bypass went with email.
	development bool
}

// Setup parses the templates, loads the translations and registers the routes.
func Setup(svc *service.Service, common config.Common) error {
	clientConfig, err := config.LoadClient()
	if err != nil {
		return fmt.Errorf("client configuration: %w", err)
	}
	backend, err := apiclient.New(clientConfig.BackendURL)
	if err != nil {
		return err
	}

	projectConfig, err := config.LoadProject()
	if err != nil {
		return fmt.Errorf("project configuration: %w", err)
	}

	// Templates and catalogues are loaded once, at startup: a broken template
	// or a malformed catalogue should stop the service now rather than
	// surprise a visitor later.
	renderer, err := web.NewRenderer(assets, "templates/shared/*.html", "templates/pages/*.html")
	if err != nil {
		return err
	}
	localization, err := web.NewLocalization(assets, "locales", fallbackLanguage)
	if err != nil {
		return err
	}

	siteURL := config.LoadSiteURL()

	// Resolved before anything that needs an address, because two notions of
	// "who is this" is the one way this arrangement fails quietly.
	proxies, err := config.LoadTrustedProxies()
	if err != nil {
		return fmt.Errorf("--%s: %w", config.KeyTrustedProxy, err)
	}

	s := &site{
		siteURL:       siteURL,
		secureCookies: secureFor(siteURL),
		assetVersion:  web.AssetVersion(assets, "static"),
		renderer:      renderer,
		localization:  localization,
		backend:       backend,
		project:       projectConfig,
		groups:        config.GroupsEnabled(),
		order:         rand.Uint64(),
		overlay:       web.NewThemeOverlay(backend),
		assets:        web.NewThemeAssetHandler(backend),
		files:         web.NewThemeFileHandler(backend),
		development:   common.Development,
		proxies:       proxies,
	}
	// Every page this service serves carries a Content-Security-Policy, and
	// every inline script it renders carries the nonce that policy names. See
	// internal/web/csp.go for what it refuses and why it matters more since
	// accounts became passkeys.
	svc.Use(web.SecurityHeaders())

	// Every route, including the assets and the proxied ceremonies. Declared
	// here rather than per handler: the route somebody forgets to bound is
	// always the one that needed it.
	//
	// It is this service and not the backend because of where a reader's
	// address is. The backend sees it only on the calls the browser's own
	// ceremonies are forwarded through — a server-rendered page reads the
	// backend from this process, so a limiter there would see the whole
	// register arriving from one address and throttle the site itself. The
	// backend keeps its tighter ceremony limit as the backstop behind this.
	bounds := config.LoadBounds()
	log.Info().
		Int("assets", bounds.Assets).Int("pages", bounds.Pages).
		Int("writes", bounds.Writes).
		Msg("per-address limits, in requests a minute")
	svc.Use(web.RateLimited(bounds, s.proxies))

	if err := s.registerRoutes(svc); err != nil {
		return err
	}

	log.Info().
		Strs("languages", localization.Supported()).
		Str("backend", clientConfig.BackendURL).
		Msg("frontend ready to serve")
	return nil
}

func (s *site) registerRoutes(svc *service.Service) error {
	static, err := web.StaticHandler(assets, "static")
	if err != nil {
		return err
	}

	mux := svc.Mux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", static))

	// The flags the language selector draws, shared with the other web
	// service rather than copied into each one's assets.
	flags, err := web.FlagsHandler()
	if err != nil {
		return err
	}
	mux.Handle("GET /flags/", http.StripPrefix("/flags/", flags))

	// The service worker is served from the root, and it has to be: a worker's
	// scope is the directory it was served from, so one at /static/sw.js could
	// only ever receive a push for /static — which is nothing anybody reads.
	mux.HandleFunc("GET /sw.js", s.serviceWorker)

	// The shared palette, component styles and toggle, served from the theme
	// package so neither service keeps a copy.
	web.ThemeAssets(mux, s.assetVersion)
	// Linked after the static defaults, so it overrides only what a theme
	// declares and the built-ins remain the fallback.
	mux.Handle("GET /theme/tokens.css", s.overlay)
	mux.Handle("GET /theme/assets/{slot}", s.assets)

	// The tab icon, as a raster, at the three fixed addresses browsers ask for
	// without being told. See web.FaviconHandler: Safari takes the SVG link
	// and draws its own letter tile anyway.
	web.NewFaviconHandler(s.assets).Register(mux)
	mux.Handle("GET /theme/files/{name}/{path...}", s.files)

	// Every page is localized, so language resolution wraps the page handlers
	// rather than being repeated inside each one.
	localized := func(h http.HandlerFunc) http.Handler {
		return s.localization.Middleware(h)
	}
	// The register is the front page. Everything else about this project is an
	// argument for reading it, and an argument is not evidence.
	mux.Handle("GET /{$}", localized(s.register))
	mux.Handle("GET /doleance", localized(s.doleance))
	mux.Handle("GET /about", localized(s.about))
	// Where the register used to live. Moved rather than removed, because
	// links people saved are the one thing this project cannot ask them to
	// fix: a register whose addresses rot is the failure it exists against.
	mux.Handle("GET /register", http.RedirectHandler("/", http.StatusMovedPermanently))
	// Not routed at all when groups are off, so the page 404s like any other
	// address this site does not have — rather than rendering an empty map
	// under a heading promising local groups.
	// Accounts are not behind the groups switch. A register that only collects
	// doléances has no use for them either — nothing else needs a session —
	// but the pages cost nothing when nobody visits them, and a signup that
	// 404s because of a flag about groups would be a confusing way to find
	// that out. What is behind the switch is what groups are *for*.
	mux.Handle("GET /account/signin", localized(s.signin))
	mux.Handle("GET /account", localized(s.account))
	mux.Handle("POST /account", localized(s.saveAccount))
	mux.Handle("GET /account/link", localized(s.linkDevice))
	mux.Handle("GET /notifications", localized(s.notifications))
	mux.Handle("POST /notifications", localized(s.saveNotifications))
	// The browser's half of a passkey ceremony and of a push subscription has
	// to post somewhere on this origin: the backend is not publicly exposed.
	s.registerAccountAPI(mux)

	if s.groups {
		mux.Handle("GET /around-me", localized(s.aroundMe))
		mux.Handle("GET /account/groups", localized(s.myGroups))
		// Before the wildcard, or "new" is read as a group identifier.
		mux.Handle("GET /groups/new", localized(s.proposeGroup))
		mux.Handle("POST /groups/new", localized(s.createGroup))
		mux.Handle("GET /groups/{id}", localized(s.group))
		mux.Handle("GET /groups/{id}/manage", localized(s.manageGroup))
		mux.Handle("POST /groups/{id}/manage", localized(s.saveGroup))
		mux.Handle("POST /groups/{id}/members", localized(s.saveMemberRole))
		mux.Handle("POST /groups/{id}/messages", localized(s.readGroupMessage))
		mux.Handle("POST /groups/{id}/actions", localized(s.saveAction))
		mux.Handle("POST /groups/{id}/join", localized(s.joinGroup))
		mux.Handle("POST /groups/{id}/leave", localized(s.leaveGroup))
		mux.Handle("POST /groups/{id}/write", localized(s.writeToGroup))
		// The form asks before the submit rather than rejecting a filled-in
		// page. The frontend holds no backend credentials, so it proxies.
		mux.HandleFunc("GET /api/groups/available", s.groupNameJSON)
		// The map fetches its markers from here. Inside the switch like
		// everything else: the page is gone, so an endpoint still answering
		// with every group in the country would be the one trace left.
		mux.HandleFunc("GET /api/groups", s.groupsJSON)
	}
	mux.Handle("GET /voices", localized(s.voices))
	// Before nothing in particular, but kept beside its listing: a passage
	// has an address of its own, which is what a card's permalink points at.
	mux.Handle("GET /voices/{id}", localized(s.voice))
	mux.Handle("POST /voices/{id}/like", localized(s.likeVoice))
	s.registerSubmitRoutes(mux)
	// The reader's own list, and the two controls that change it. Outside the
	// groups switch, like the account pages themselves.
	s.registerBookmarkRoutes(mux)
	// What this register refused, and the one thing a reader can do about it.
	s.registerDroppedRoutes(mux)
	s.registerArchiveRoutes(mux)
	s.registerRegistryRoutes(mux)
	return nil
}

// about is the explanation: what a cahier de doléances was, what happened to
// the last one, and what this register does differently.
//
// It is a page somebody chooses to read rather than the first thing they meet.
// Putting it at the root made the argument the product and the register a
// sample underneath it, which is backwards — the aggregation is the argument,
// and it only makes itself by being read.
func (s *site) about(w http.ResponseWriter, r *http.Request) {
	data := aboutPage{page: s.newPage(r, "about.title")}
	// "Open, and checkable" is a section of this page, not a coda under it.
	data.InlinesFooter = true
	s.renderer.Render(w, http.StatusOK, "about", data)
}

// publishedCards reads the register and renders it for a template.
//
// A failure is logged and shows as an empty section rather than an error page:
// the historical corpus, the explanation and the link to write are all still
// worth serving when the backend is having trouble.
func (s *site) publishedCards(r *http.Request, p page, limit int) []messageCard {
	// Read as the reader, which is what fills in whether each doléance is
	// already in their own list. An anonymous request sends no credential and
	// the backend does no extra work for it.
	messages, err := s.reader(r).ListMessages(r.Context(), limit)
	if err != nil {
		log.Warn().Err(err).Msg("cannot read the register")
		return nil
	}

	cards := make([]messageCard, 0, len(messages))
	for _, message := range messages {
		cards = append(cards, toMessageCard(message, p))
	}
	return cards
}

// toMessageCard is one doléance as a card, in one place because three pages
// build one.
//
// The excerpt comes off the row rather than being cut here: it is derived
// once when the doléance is written, so a listing reads a column instead of
// doing the same work for every visitor.
func toMessageCard(message apiclient.Message, p page) messageCard {
	return messageCard{
		ID:             message.ID,
		Nickname:       message.Nickname,
		Place:          message.Place,
		Date:           message.CreatedAt.Format("2 January 2006"),
		Text:           message.Excerpt,
		Truncated:      message.Truncated,
		Subjects:       message.Subjects,
		Likes:          message.Likes,
		Kept:           message.Kept,
		Verified:       message.Verified,
		AnonymousLabel: p.T("card.anonymous"),
		Latitude:       message.Latitude,
		Longitude:      message.Longitude,
	}
}

func (s *site) doleance(w http.ResponseWriter, r *http.Request) {
	s.renderer.Render(w, http.StatusOK, "doleance", submitPage{doleancePage: s.doleanceData(r)})
}

// doleanceData builds the form page. It is separate from the handler because a
// rejected submission re-renders the same page carrying the draft, and the
// year list and the examples have to be there too.
func (s *site) doleanceData(r *http.Request) doleancePage {
	data := doleancePage{page: s.newPage(r, "write.title")}
	data.UsesMap = true
	data.Examples = s.historicalWindow(r, data.page, windowOffset(r), historicalPerPage)
	data.BirthYears = birthYears(time.Now().Year())
	return data
}

// birthYearRange bounds the year selector: old enough that no plausible
// contributor is excluded, recent enough that the list is not absurd.
//
// The young end is **7**, the age of reason — the age at which a child has
// long been held capable of telling right from wrong, and in French law the
// age from which discernment is presumed. It is deliberately not a judgement
// about who writes well enough to be here. A child who can say what is wrong
// with their life is describing a grievance, and a register that turned them
// away at the door would be making exactly the sort of decision about whose
// complaint counts that this project exists to refuse.
const (
	oldestContributor   = 110
	youngestContributor = 7
)

// birthYears lists the years offered, most recent first — somebody choosing
// their own birth year is far more likely to be looking near the recent end
// than scrolling from 1916.
func birthYears(currentYear int) []int {
	years := make([]int, 0, oldestContributor-youngestContributor+1)
	for year := currentYear - youngestContributor; year >= currentYear-oldestContributor; year-- {
		years = append(years, year)
	}
	return years
}

// register is the front page: everything published, by place and by subject,
// with passages from 1789 mixed through it.
func (s *site) register(w http.ResponseWriter, r *http.Request) {
	data := registerPage{page: s.newPage(r, "register.title")}
	data.Place = r.URL.Query().Get("place")
	data.Subjects = s.subjects(r.Context())
	// Shuffled, which is the register's default order: newest-first with a cap
	// buries everything older behind a page nobody turns, and this register's
	// whole claim is that every doléance counts the same.
	data.Entries = s.registerEntries(r, data.page,
		apiclient.MessageQuery{Shuffled: true}).Entries
	// The register filters by the map, so the page needs Leaflet.
	data.UsesMap = true

	s.renderer.Render(w, http.StatusOK, "register", data)
}

func (s *site) aroundMe(w http.ResponseWriter, r *http.Request) {
	data := aroundPage{page: s.newPage(r, "around.title")}
	data.UsesMap = true

	s.renderer.Render(w, http.StatusOK, "around", data)
}

// voicePage is one passage at an address of its own.
type voicePage struct {
	page

	Text historicalCard
}

// voice renders one passage from an earlier register.
//
// In the reader's language where a translation exists, like the listing — the
// point of the translations being that no text is closed to anybody.
func (s *site) voice(w http.ResponseWriter, r *http.Request) {
	data := voicePage{page: s.newPage(r, "voice.title")}

	text, err := s.reader(r).GetHistorical(r.Context(), r.PathValue("id"))
	if err != nil {
		log.Debug().Err(err).Str("text", r.PathValue("id")).Msg("passage not resolved")
		s.renderNotFound(w, r)
		return
	}

	data.Text = toCard(text, data.Lang, data.T("card.placeholder"))
	// The destination rather than a way to one: the words are not a link to
	// the page they are already on.
	data.Text.Full = true

	s.renderer.Render(w, http.StatusOK, "voice", data)
}

// likeVoice records one more reader who recognised themselves in a passage.
//
// The same shape as a doléance's, and the same silence about who: a form post
// and a redirect back, with nothing asked for and nothing kept.
func (s *site) likeVoice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if _, err := s.backend.LikeHistorical(r.Context(), id); err != nil {
		log.Debug().Err(err).Str("text", id).Msg("cannot record a like")
	}

	s.sendBack(w, r, "/voices/"+pathEscape(id))
}

func (s *site) voices(w http.ResponseWriter, r *http.Request) {
	data := voicesPage{page: s.newPage(r, "voices.title")}
	data.Texts = s.historicalShuffled(r, data.page)

	s.renderer.Render(w, http.StatusOK, "voices", data)
}

// historicalWindow returns one page of the corpus, in the order fixed for this
// process, together with where the arrows lead.
//
// Ordering is a hash of the passage id mixed with the process seed: a stable
// shuffle that needs no state, survives the corpus growing, and differs
// between deployments so the same three passages do not become the whole
// register in practice.
func (s *site) historicalWindow(r *http.Request, p page, offset, size int) corpusWindow {
	texts, err := s.reader(r).ListHistorical(r.Context())
	if err != nil {
		log.Warn().Err(err).Msg("cannot load the historical corpus")
		return corpusWindow{}
	}
	if len(texts) == 0 || size < 1 {
		return corpusWindow{}
	}

	sort.Slice(texts, func(i, j int) bool {
		return s.rank(texts[i].ID) < s.rank(texts[j].ID)
	})

	total := (len(texts) + size - 1) / size
	// Wrap rather than clamp: a hand-edited or stale offset lands somewhere
	// valid instead of on an empty page.
	offset = ((offset % total) + total) % total

	start := offset * size
	end := min(start+size, len(texts))

	cards := make([]historicalCard, 0, end-start)
	for _, text := range texts[start:end] {
		cards = append(cards, toCard(text, p.Lang, p.T("card.placeholder")))
	}

	return corpusWindow{
		Texts:    cards,
		Position: offset + 1,
		Total:    total,
		Previous: (offset - 1 + total) % total,
		Next:     (offset + 1) % total,
	}
}

// rank is the sort key of a passage under this process's ordering.
func (s *site) rank(id string) uint64 {
	h := fnv.New64a()
	binary.Write(h, binary.LittleEndian, s.order) //nolint:errcheck
	h.Write([]byte(id))                           //nolint:errcheck
	return h.Sum64()
}

// windowOffset reads the page number from the query string. Anything
// unparseable is simply the first window.
func windowOffset(r *http.Request) int {
	offset, err := strconv.Atoi(r.URL.Query().Get("p"))
	if err != nil {
		return 0
	}
	return offset
}

// historicalByLanguage groups the corpus by the language each passage was
// written in — which is how the register is organised, and is separate from
// the language the reader is reading in.
// historicalShuffled returns the whole corpus in one arbitrary order.
//
// # Why the languages stopped being headings
//
// The page used to carry a heading per language with its passages under it,
// which sorted the corpus by the one property a reader is least likely to be
// looking for. It also made the page an inventory of what the archive happens
// to hold — three in French, one in Occitan — which says more about collecting
// than about the people who wrote them.
//
// Mixing them says the thing the page is for instead: these are grievances
// from other centuries, and they rhyme with each other across the language
// they were written in. Every card is already shown in the reader's language
// where a translation exists, so nothing is closed by the mixing.
//
// The order is the same stable shuffle the landing window uses — a hash of the
// passage id against this process's seed — so it needs no state, survives the
// corpus growing, and differs between deployments rather than making the same
// three passages the whole register in practice.
func (s *site) historicalShuffled(r *http.Request, p page) []historicalCard {
	texts, err := s.reader(r).ListHistorical(r.Context())
	if err != nil {
		log.Warn().Err(err).Msg("cannot load the historical corpus")
		return nil
	}

	sort.Slice(texts, func(i, j int) bool {
		return s.rank(texts[i].ID) < s.rank(texts[j].ID)
	})

	cards := make([]historicalCard, 0, len(texts))
	for _, text := range texts {
		cards = append(cards, toCard(text, p.Lang, p.T("card.placeholder")))
	}
	return cards
}

// toCard renders a passage in the reader's language where the corpus has one,
// and in the original otherwise — the point of the translations being that no
// text is closed to anybody.
func toCard(text apiclient.HistoricalText, language, placeholderLabel string) historicalCard {
	card := historicalCard{
		ID:               text.ID,
		Likes:            text.Likes,
		Kept:             text.Kept,
		Title:            text.Title,
		DocumentTitle:    text.DocumentTitle,
		Period:           text.Period,
		Region:           text.Region,
		Text:             text.Text,
		Source:           text.Source,
		Placeholder:      text.Placeholder,
		PlaceholderLabel: placeholderLabel,
		Latitude:         text.Latitude,
		Longitude:        text.Longitude,
	}

	if language == text.Language {
		return card
	}
	for _, translation := range text.Translations {
		if translation.Language != language {
			continue
		}
		card.Title = translation.Title
		card.Text = translation.Text
		if translation.DocumentTitle != "" {
			card.DocumentTitle = translation.DocumentTitle
		}
		// Said plainly: these are not the words the writer used.
		card.TranslatedFrom = text.Language
		if translation.Region != "" {
			card.Region = translation.Region
		}
		if translation.Source != "" {
			card.Source = translation.Source
		}
		break
	}
	return card
}

// groupsJSON feeds the map. There are no accepted groups yet, so it answers
// with an empty set rather than with an error the map would have to handle.
// groupsJSON feeds the map on "Around me".
//
// The viewport narrows what is drawn and never what is counted: the total
// comes back untouched by the bounds, because the number of groups and how
// widely they are spread is the argument the page is making. Somebody seeing
// one pin near them should still learn how many exist elsewhere.
//
// An unreachable backend answers with an empty map rather than an error: the
// page around it is still worth reading, and a map that fails to load should
// not take the heading and the filters with it.
// groupNameJSON answers the propose form's live availability check.
//
// A failure answers "taken", matching the client: the safe direction when the
// backend cannot be reached is to promise nothing, and the write is where the
// name is really claimed.
func (s *site) groupNameJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	available, err := s.backend.GroupNameAvailable(r.Context(), r.URL.Query().Get("name"))
	if err != nil {
		log.Debug().Err(err).Msg("cannot check a group name")
	}

	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"available": available,
	})
}

func (s *site) groupsJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	groups, total, err := s.backend.ListGroups(r.Context(), r.URL.Query().Get("bounds"))
	if err != nil {
		log.Warn().Err(err).Msg("cannot load groups, rendering an empty map")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"groups": []any{}, "total": 0,
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"groups": groups, "total": total,
	})
}

// subjects fetches the filter options, degrading to none when the backend is
// unreachable: a filter that cannot be populated should not take the page down
// with it.
//
// Named in the language the reader is reading the site in — not the language
// the doléances were written in. One subject, and which of its names appears
// is a question about who is looking at it.
func (s *site) subjects(ctx context.Context) []apiclient.Subject {
	subjects, err := s.backend.ListSubjects(ctx, web.LanguageFrom(ctx))
	if err != nil {
		log.Warn().Err(err).Msg("cannot load subjects, rendering without filters")
		return nil
	}
	return subjects
}
