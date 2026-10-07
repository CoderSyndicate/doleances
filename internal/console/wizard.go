package console

import (
	_ "embed"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/authentik"
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/web"
)

// WizardPath is where the setup page lives on the maintenance port.
const WizardPath = "/setup"

//go:embed templates/setup.html
var wizardHTML string

// wizardTemplate is parsed once at package level, like every other template
// here. It is deliberately standalone rather than part of the console's
// layout: this page runs before anything is configured, on a port where the
// theme overlay may not be reachable and where the console's navigation would
// link to pages nobody can open yet.
var wizardTemplate = template.Must(template.New("setup").Parse(wizardHTML))

// wizardPage is what the setup page shows.
type wizardPage struct {
	// Reprovision says this console already has an identity provider and is
	// about to replace it, which the page says in the plainest words it has.
	Reprovision bool

	// Error is what went wrong with the last attempt.
	Error string

	// Done, and what was provisioned.
	Done          bool
	AdminUsername string
	InstanceURL   string
	RecoveryReady bool
	Missing       []string

	// OwnToken is what the console kept, named so an operator can find and
	// revoke it. Empty means it kept the pasted one.
	OwnToken string

	// TokenOwner is whose token ran the wizard, said alongside the first admin
	// when they are not the same person.
	TokenOwner string

	// AdminName and AdminEmail come back on a refusal, so a form that was
	// filled in and rejected does not have to be filled in again.
	AdminName  string
	AdminEmail string

	// AdminLink is the first admin's own way in, shown once.
	AdminLink string

	// ConsoleURL is where the redirect URI points, shown so an operator can
	// see what was registered rather than having to look it up in authentik.
	ConsoleURL string

	// InstanceDefault prefills the instance field from --oidc-issuer, for a
	// deployment that already knows its own authentik. Empty leaves the field
	// empty: an address nobody typed is one nobody checks.
	InstanceDefault string

	// AppName is what this register will be called in the directory, and the
	// prefix its two groups are named with. Shown back so an operator sees the
	// names before they exist rather than afterwards.
	AppName string
}

// registerWizard puts the setup page on the **maintenance** port.
//
// # Why there and not on the console itself
//
// The maintenance port is never published — only the application port is, and
// the images are built that way. So reaching this
// needs the cluster: a VPN and a port forward. A secret can be copied out of a
// log; a network boundary cannot.
//
// That also means it is not registered at all once an installation is
// provisioned, unless `--superuser` says otherwise. Absent rather than
// refusing, the same line the project draws for `--disable-groups`.
//
// # It wraps itself in the security headers
//
// Service middleware — the Content-Security-Policy, the rate limiter — is
// applied to the application mux only:
//
//	maintenance := s.newServer(..., Chain(s.maintenanceMux, RequestID(), Recover()))
//
// That is right for `/metrics` and wrong for a page that renders HTML and
// takes a pasted credential. Without this line it would be the one HTML page
// in the project with no policy.
func (c *console) registerWizard(mux *http.ServeMux) {
	if !c.setupDoor.Open {
		return
	}

	handler := web.SecurityHeaders()(http.HandlerFunc(c.wizard))
	mux.Handle("GET "+WizardPath, handler)
	mux.Handle("POST "+WizardPath, handler)

	log.Warn().Str("path", WizardPath).Bool("replacing", c.setupDoor.Reprovision).
		Msg("setup wizard registered on the maintenance port")
}

func (c *console) wizard(w http.ResponseWriter, r *http.Request) {
	// # This request outlives the server's write deadline, on purpose
	//
	// Provisioning is twenty-odd round trips to somebody else's authentik: a
	// provider, an application, two groups, a recovery flow built out of
	// prompts and stages, a token, a recovery link. Against a remote instance
	// that is **forty seconds**, and the server's WriteTimeout is thirty.
	//
	// What that cost is worth spelling out, because it looks like nothing. The
	// deadline runs from when the request headers were read, so the connection
	// was already closed before the handler wrote a byte — and the handler
	// carried on and finished. Every object was created, the settings were
	// stored, the log said "setup complete", and the browser said the server
	// dropped the connection. **The page that is shown once, carrying the
	// first admin's only way in, was written to a socket nobody was holding.**
	//
	// So the deadline is cleared, the same way the log stream clears it. The
	// request is bounded by its own context rather than by a clock that was
	// chosen for pages which do not call out to anything.
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{})
	}

	page := wizardPage{
		Reprovision:     c.setupDoor.Reprovision,
		ConsoleURL:      c.consoleURL,
		AppName:         models.DefaultAppName,
		InstanceDefault: c.oidcIssuer,
	}

	if r.Method == http.MethodGet {
		c.renderWizard(w, http.StatusOK, page)
		return
	}

	if err := r.ParseForm(); err != nil {
		page.Error = "that form could not be read"
		c.renderWizard(w, http.StatusBadRequest, page)
		return
	}

	instance := strings.TrimSpace(r.PostFormValue("instance_url"))
	if instance != "" {
		// What they typed, so a refused form comes back with it rather than
		// snapping to the configured default and losing a correction.
		page.InstanceDefault = instance
	}
	token := strings.TrimSpace(r.PostFormValue("token"))
	consoleURL := strings.TrimSpace(r.PostFormValue("console_url"))
	if consoleURL == "" {
		consoleURL = c.consoleURL
	}
	page.ConsoleURL = consoleURL

	appName := strings.TrimSpace(r.PostFormValue("app_name"))
	if appName == "" {
		appName = models.DefaultAppName
	}
	page.AppName = appName

	if !models.ValidAppName(appName) {
		page.Error = "the name must be lowercase letters and digits, with - or _ inside"
		c.renderWizard(w, http.StatusUnprocessableEntity, page)
		return
	}

	client, err := authentik.New(instance, token)
	if err != nil {
		page.Error = err.Error()
		c.renderWizard(w, http.StatusUnprocessableEntity, page)
		return
	}

	// Who the register answers to. Blank means the token's owner, which on a
	// fresh authentik is `akadmin` — a bootstrap account rather than a person.
	first := firstAdmin{
		Username: strings.TrimSpace(r.PostFormValue("admin_username")),
		Name:     strings.TrimSpace(r.PostFormValue("admin_name")),
		Email:    strings.TrimSpace(r.PostFormValue("admin_email")),
	}
	page.AdminUsername = first.Username
	page.AdminName = first.Name
	page.AdminEmail = first.Email

	result, err := provision(r.Context(), client, consoleURL, appName, first)
	if err != nil {
		// The message is authentik's own where there is one — "slug: this
		// field must be unique" tells an operator what to do, where a status
		// code tells them to go and read logs.
		log.Error().Err(err).Str("instance", instance).Msg("provisioning failed")
		page.Error = err.Error()
		page.Missing = result.Missing
		c.renderWizard(w, http.StatusBadGateway, page)
		return
	}

	page.AdminUsername = result.AdminUsername
	page.InstanceURL = result.InstanceURL
	page.RecoveryReady = result.RecoveryReady
	page.OwnToken = result.ConsoleToken.Identifier
	page.TokenOwner = result.TokenOwner
	page.AdminLink = result.AdminLink
	page.Missing = result.Missing

	if !result.Done {
		// The instance is missing something this will not create. Nothing was
		// made, so there is nothing to undo and the page says what to add.
		c.renderWizard(w, http.StatusOK, page)
		return
	}

	// Only now does anything leave this process. The two secrets go straight
	// to the backend and never near a template.
	toSave := result.Provisioned
	toSave.ConsoleURL = strings.TrimSuffix(consoleURL, "/")

	// **The console's own token, not the one that was pasted.** The pasted one
	// is a bootstrap credential — authentik expires it in thirty minutes by
	// default — and keeping it is what made a working installation refuse
	// everybody half an hour later. See authentik.EnsureConsoleToken.
	toSave.APIToken = result.ConsoleToken.Key
	if toSave.APIToken == "" {
		// Nothing was minted, so the setup token is all there is. It works,
		// and it may well expire; the page says so rather than storing
		// something that cannot read a role.
		toSave.APIToken = token
	}
	toSave.Reprovision = c.setupDoor.Reprovision

	if _, err := c.backend.SaveAuthSettings(r.Context(), toSave); err != nil {
		log.Error().Err(err).Msg("cannot store what was provisioned")
		page.Error = "authentik was provisioned but the backend would not store it: " + err.Error()
		c.renderWizard(w, http.StatusBadGateway, page)
		return
	}

	// Connected now, not on the next restart. The console discovered its
	// provider at startup and there was none, so without this an operator
	// finishes the wizard and still cannot sign in — with nothing on the page
	// to say that a restart is what they are missing.
	if err := c.connectIdentityProvider(r.Context()); err != nil {
		log.Error().Err(err).Msg("provisioned, but the console cannot reach the provider yet")
		page.Error = "authentik was provisioned and stored, but this console could not " +
			"connect to it: " + err.Error()
	}

	// The door closes behind it. The route stays registered until this process
	// restarts — removing a route from a live mux is not something net/http
	// offers — so the handler refuses from here, and the restart is what makes
	// it absent.
	c.setupDoor = SetupDoor{}
	page.Done = true

	log.Warn().Str("instance", result.InstanceURL).Str("admin", result.AdminUsername).
		Msg("setup complete: restart the console without --superuser to close the setup door")

	c.renderWizard(w, http.StatusOK, page)
}

func (c *console) renderWizard(w http.ResponseWriter, status int, page wizardPage) {
	// Refused once this console has been provisioned in this process, which is
	// what closes the door between the write and the restart.
	if !c.setupDoor.Open && !page.Done {
		http.NotFound(w, &http.Request{})
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := wizardTemplate.Execute(w, page); err != nil {
		log.Error().Err(err).Msg("cannot render the setup page")
	}
}
