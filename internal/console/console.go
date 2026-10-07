// Package console is the administration and curation service.
//
// It is an exposed service, so it holds no database credentials and no LLM
// key: everything it shows and every decision it records goes through the
// backend's API. Curators authenticate against Authentik over OIDC.
package console

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"strings"

	"github.com/CoderSyndicate/doleances/internal/apiclient"
	"github.com/CoderSyndicate/doleances/internal/authentik"
	"github.com/CoderSyndicate/doleances/internal/cli"
	"github.com/CoderSyndicate/doleances/internal/config"
	"github.com/CoderSyndicate/doleances/internal/console/auth"
	"github.com/CoderSyndicate/doleances/internal/service"
	"github.com/CoderSyndicate/doleances/internal/web"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// Assets are the templates, translations and static files, compiled into the
// binary so the container needs no writable path and nothing to mount.
//
//go:embed templates/shared/*.html templates/pages/*.html templates/setup.html static/* locales/*.toml
var assets embed.FS

// Default ports follow the house convention: 8xxx public, 7xxx internal
// tools, 9xxx maintenance. Every service uses a distinct pair so all three run
// side by side in development.
const (
	DefaultAppPort         = 8200
	DefaultMaintenancePort = 9200
)

const (
	defaultBackendURL = "http://127.0.0.1:7300"
	fallbackLanguage  = "en"
)

// Definition describes the console binary.
func Definition() cli.Definition {
	return cli.Definition{
		Name:  "console",
		Title: "Doléances Console",
		Short: "Administration and curation for doléances",
		Long: "The console is where curators accept or reject submissions.\n" +
			"Curators never edit: a register that has been edited is not a record.",
		DefaultAppPort:         DefaultAppPort,
		DefaultMaintenancePort: DefaultMaintenancePort,
		RegisterFlags: func(cmd *cobra.Command) {
			config.RegisterClientFlags(defaultBackendURL)(cmd)
			// The same switch the backend and the frontend read, so one
			// environment variable shapes the whole deployment.
			config.RegisterFeatureFlags(cmd)
			// Where the public site answers. The console needs it only in
			// development, to link a group's name to its management page.
			config.RegisterSiteFlags(cmd)
			// The door back into setup. Deliberately not --development: this
			// disables nothing, it reopens the wizard on a console that is
			// otherwise running normally and fully authenticated.
			config.RegisterSuperuserFlag(cmd)
			// Where staff reach this console, for the OAuth redirect. Not
			// --site-url, which is the public register's address: authentik
			// matches a redirect URI strictly, so the two must not be confused.
			config.RegisterConsoleURLFlag(cmd)
			// Only a default for the wizard's first field, for a deployment
			// that already knows its own authentik. Nothing reads it after
			// provisioning.
			config.RegisterOIDCIssuerFlag(cmd)
		},
		Setup: Setup,
	}
}

// Command returns the console's root command.
func Command(version, commit string) *cobra.Command {
	return cli.Build(Definition(), version, commit)
}

// console holds the service's dependencies.
type console struct {
	// setupDoor is whether this console offers the provisioning wizard, and
	// whether it would be replacing a configuration that already exists.
	// Decided once at startup: see decideSetupDoor.
	setupDoor SetupDoor

	// authenticator signs staff in, and is nil until an identity provider has
	// been provisioned. Nil is not "let everybody in": the gate refuses
	// instead, because a console that cannot authenticate anybody must not
	// therefore authorise everybody.
	authenticator *auth.Authenticator

	// directory manages the people in the two role groups. Nil alongside the
	// authenticator, for the same reason.
	directory *authentik.Client

	// adminGroup and curatorGroup are the identifiers roles are granted with;
	// adminGroupName and curatorGroupName are what they are called, which is
	// what the directory is listed by. Both are stored rather than rebuilt
	// from a constant: an operator who set this up under a different name must
	// not have the console looking for groups it never created.
	adminGroup       string
	curatorGroup     string
	adminGroupName   string
	curatorGroupName string

	// secureCookies is derived from the console's own URL rather than
	// configured separately — a deployment told one thing and behaving another
	// is the mistake a second setting invites, and the cost of getting it
	// wrong is a session token on the wire.
	secureCookies bool

	// groups is whether this deployment offers local action groups, read from
	// the same setting the backend and the frontend read.
	groups bool

	// assetVersion busts the browser cache when the embedded CSS or scripts
	// change. Without it a redeploy leaves visitors on the old stylesheet
	// until their cache expires.
	assetVersion string

	renderer     *web.Renderer
	localization *web.Localization
	backend      *apiclient.Client

	// overlay serves the active theme's token overrides, and is invalidated
	// whenever the library changes here.
	overlay *web.ThemeOverlay

	// assets serves the active theme's images, falling back to the built-in
	// ones.
	assets *web.ThemeAssetHandler

	// files serves the package files a theme's own tokens reference.
	files *web.ThemeFileHandler

	// oidcIssuer prefills the wizard's instance field, and is read nowhere
	// else: after provisioning the address comes from the settings row.
	oidcIssuer string

	// consoleURL is where staff reach this console. It seeds the setup form;
	// what the handshake uses afterwards is the address the wizard stored.
	//
	// Not siteURL below, which is the public register's address: authentik
	// matches a redirect URI strictly, so sending staff back to the frontend's
	// address would fail at sign-in with an error only the person who tried
	// can see.
	consoleURL string

	// siteURL is where the frontend answers. Used in development only, to
	// turn a group's name into a link to its management page.
	siteURL string

	// development records that authentication is off, so the UI can say so
	// rather than leave a curator assuming they are signed in.
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

	renderer, err := web.NewRenderer(assets, "templates/shared/*.html", "templates/pages/*.html")
	if err != nil {
		return err
	}
	localization, err := web.NewLocalization(assets, "locales", fallbackLanguage)
	if err != nil {
		return err
	}

	// Said at startup, every time, the way --development says its own thing.
	// A switch that is quiet while it is on is a switch somebody leaves on.
	config.WarnIfSuperuser()

	// Whether this console offers the provisioning wizard, decided once: the
	// answer governs whether a route exists at all, and a route that came and
	// went with the backend's availability would be a setup page that opened
	// itself whenever the backend had a bad second.
	door := decideSetupDoor(context.Background(), backend, config.SuperuserEnabled())

	siteURL := config.LoadSiteURL()

	c := &console{
		setupDoor: door,
		// From the console's own address: the cookie is set on this console,
		// not on the public register, and a deployment where the two differ in
		// scheme would otherwise mark the session cookie wrongly.
		secureCookies: strings.HasPrefix(config.LoadConsoleURL(), "https://"),
		assetVersion:  web.AssetVersion(assets, "static"),
		renderer:      renderer,
		localization:  localization,
		backend:       backend,
		overlay:       web.NewThemeOverlay(backend),
		assets:        web.NewThemeAssetHandler(backend),
		files:         web.NewThemeFileHandler(backend),
		development:   common.Development,
		siteURL:       siteURL,
		consoleURL:    config.LoadConsoleURL(),
		oidcIssuer:    config.LoadOIDCIssuer(),
		groups:        config.GroupsEnabled(),
	}
	// Every page this service serves carries a Content-Security-Policy, and
	// every inline script it renders carries the nonce that policy names. See
	// internal/web/csp.go for what it refuses and why it matters more since
	// accounts became passkeys.
	svc.Use(web.SecurityHeaders())

	// The identity provider, when there is one. A console that is not yet
	// provisioned runs without it — the wizard is how it gets one — and the
	// gate refuses everything rather than admitting everybody.
	if err := c.connectIdentityProvider(context.Background()); err != nil {
		// Not fatal. A console that cannot reach its directory should still
		// start, say so, and serve the setup wizard if that is what is needed;
		// refusing to boot would make a directory outage into an outage of the
		// thing an operator uses to fix it.
		log.Error().Err(err).Msg(
			"cannot connect to the identity provider: nobody can sign in until this is fixed")
	}

	// Staff-only by default. Service middleware rather than a decoration on
	// the handlers that looked sensitive: the route somebody forgets to guard
	// is always the one that needed it.
	svc.Use(c.gate)

	if err := c.registerRoutes(svc); err != nil {
		return err
	}

	log.Info().
		Strs("languages", localization.Supported()).
		Str("backend", clientConfig.BackendURL).
		Msg("console ready to serve")
	return nil
}

func (c *console) registerRoutes(svc *service.Service) error {
	static, err := web.StaticHandler(assets, "static")
	if err != nil {
		return err
	}

	// The setup wizard, on the maintenance port rather than this one: it is
	// never published, so reaching it needs the cluster. Registered only when
	// the door is open — absent rather than refusing, once provisioned.
	c.registerWizard(svc.MaintenanceMux())

	mux := svc.Mux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", static))

	// The flags the language selector draws, shared with the other web
	// service rather than copied into each one's assets.
	flags, err := web.FlagsHandler()
	if err != nil {
		return err
	}
	mux.Handle("GET /flags/", http.StripPrefix("/flags/", flags))

	// The shared palette, component styles and toggle, served from the theme
	// package so neither service keeps a copy.
	web.ThemeAssets(mux, c.assetVersion)
	// Linked after the static defaults, so it overrides only what a theme
	// declares and the built-ins remain the fallback.
	mux.Handle("GET /theme/tokens.css", c.overlay)
	mux.Handle("GET /theme/assets/{slot}", c.assets)

	// The tab icon, as a raster, at the three fixed addresses browsers ask for
	// without being told. See web.FaviconHandler: Safari takes the SVG link
	// and draws its own letter tile anyway.
	web.NewFaviconHandler(c.assets).Register(mux)
	mux.Handle("GET /theme/files/{name}/{path...}", c.files)

	// The three routes the handshake needs, and the only pages an
	// unauthenticated browser may reach.
	c.registerSignIn(mux)

	localized := func(h http.HandlerFunc) http.Handler {
		return c.localization.Middleware(h)
	}
	mux.Handle("GET /{$}", localized(c.queue))
	mux.Handle("GET /settings/people", localized(c.people))
	mux.Handle("POST /settings/people", localized(c.savePeople))
	mux.Handle("GET /audit", localized(c.audit))
	mux.Handle("GET /subjects", localized(c.subjects))
	mux.Handle("GET /subjects/{id}", localized(c.subject))

	c.registerThemeRoutes(mux)
	c.registerLLMRoutes(mux)
	c.registerSpamRoutes(mux)
	c.registerSnapshotRoutes(mux)
	c.registerQueueRoutes(mux)
	c.registerGroupRoutes(mux)
	return nil
}

// page is what every console template receives.
type page struct {
	web.Page

	// AssetVersion is appended to every /static URL, so a changed stylesheet
	// is fetched rather than served from cache.
	AssetVersion string

	// Groups is whether this deployment offers local action groups. The
	// console hides their curation entirely when it does not: a queue that can
	// never fill is a page that teaches curators to stop looking.
	Groups bool

	// Development shows the banner saying authentication is off.
	Development bool

	// Admin is whether this person may manage people, which decides whether
	// the people page is in the navigation at all.
	//
	// Read from the session's copy of the roles, like every other read — an
	// hour old at most. That is the right side to be wrong on here: the link
	// is only a link, and the page behind it re-reads the directory before it
	// changes anything. Under --development everybody holds every role, so the
	// local runner sees it.
	Admin bool
}

func (c *console) newPage(r *http.Request, titleKey string) page {
	return page{
		Page:         web.NewPage(r, c.localization, titleKey),
		AssetVersion: c.assetVersion,
		Development:  c.development,
		Groups:       c.groups,
		Admin:        c.mayAdminister(r),
	}
}

type queuePage struct {
	page
}

// subjectsPage is the vocabulary manager. Like the other live pages its
// contents are fetched by script, so a rename refreshes the list without
// reloading and losing the curator's place in it.
type subjectsPage struct {
	page
}

// subjectPage edits one subject. Its contents are fetched by script from the
// identifier in the URL, so an attachment refreshes in place.
type subjectPage struct {
	page
	SubjectID string
}

// auditRow is one recorded decision.
type auditRow struct {
	At     string
	Actor  string
	Action string
	Reason string
}

type auditPage struct {
	page
	Entries []auditRow
}

// themePage is the theme library manager. Its content is loaded by script
// from the backend, so the page itself carries only the shared data.
type themePage struct {
	page
}

// spamPage samples what the classifier dropped. Its contents are fetched by
// script, like the other live pages.
type spamPage struct {
	page
}

// llmPage configures the language-model service. Like the theme manager, its
// values are fetched by script rather than rendered in, so the API key never
// passes through a template.
type llmPage struct {
	page
}

func (c *console) queue(w http.ResponseWriter, r *http.Request) {
	// The queue itself is fetched by the page, so a decision refreshes the
	// list without reloading and losing the curator's place in it.
	c.renderer.Render(w, http.StatusOK, "queue", queuePage{page: c.newPage(r, "queue.title")})
}

func (c *console) subjects(w http.ResponseWriter, r *http.Request) {
	c.renderer.Render(w, http.StatusOK, "subjects", subjectsPage{page: c.newPage(r, "vocabulary.title")})
}

func (c *console) subject(w http.ResponseWriter, r *http.Request) {
	c.renderer.Render(w, http.StatusOK, "subject", subjectPage{
		page:      c.newPage(r, "vocabulary.title"),
		SubjectID: r.PathValue("id"),
	})
}

func (c *console) audit(w http.ResponseWriter, r *http.Request) {
	c.renderer.Render(w, http.StatusOK, "audit", auditPage{page: c.newPage(r, "audit.title")})
}
