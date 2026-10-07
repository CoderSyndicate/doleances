package backend

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/store"
)

// registerAuthRoutes declares the identity provider configuration.
//
// Three routes: read what is configured, read the two secrets the console
// cannot work without, and write the lot once.
//
// There is deliberately no endpoint that *clears* the configuration —
// reopening the setup door is a restart with `--superuser`, which is a thing
// somebody does on purpose at a terminal rather than a request anybody can
// make.
//
// The credentials route is the uncomfortable one and is commented as such
// where it is defined: it hands an exposed service two secrets, which is a
// departure from how the rest of this project is arranged.
func (a *API) registerAuthRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-auth-settings",
		Method:      http.MethodGet,
		Path:        "/v1/auth/settings",
		Summary:     "How the console reaches its identity provider",
		Description: "Carries no secrets. The OAuth client secret and the authentik API " +
			"token are never returned by this API and never reach a template; the caller " +
			"is told only whether a token is stored.",
		Tags: []string{"Auth"},
	}, a.getAuthSettings)

	huma.Register(api, huma.Operation{
		OperationID: "get-auth-credentials",
		Method:      http.MethodGet,
		Path:        "/v1/auth/credentials",
		Summary:     "The secrets the console needs to authenticate people",
		Description: "Carries the OAuth client secret and the authentik API token. The " +
			"backend is not publicly exposed, so this is reachable from the console and " +
			"the frontend and from nothing else — see the handler for why it exists at " +
			"all and what the stricter arrangement would be.",
		Tags: []string{"Auth"},
	}, a.getAuthCredentials)

	huma.Register(api, huma.Operation{
		OperationID: "save-auth-settings",
		Method:      http.MethodPut,
		Path:        "/v1/auth/settings",
		Summary:     "Record what the setup wizard provisioned",
		Description: "Refused once an installation is provisioned, unless the request says " +
			"plainly that it means to replace it. This is the one write that can point " +
			"this console's authentication at a different directory.",
		Tags: []string{"Auth"},
	}, a.saveAuthSettings)
}

// AuthSettingsItem is the configuration as anybody is allowed to see it.
type AuthSettingsItem struct {
	Provisioned bool   `json:"provisioned"`
	AppName     string `json:"app_name,omitempty"`
	InstanceURL string `json:"instance_url,omitempty"`
	ConsoleURL  string `json:"console_url,omitempty"`
	ClientID    string `json:"client_id,omitempty"`

	// HasToken is all that is ever said about the API token. Whether one is
	// stored is a fact a page needs; what it is, is not.
	HasToken bool `json:"has_token"`

	AdminGroup   string `json:"admin_group,omitempty"`
	CuratorGroup string `json:"curator_group,omitempty"`

	ProvisionedBy string `json:"provisioned_by,omitempty"`
	ProvisionedAt string `json:"provisioned_at,omitempty"`

	// The group names, so the console and the directory cannot drift on what
	// a role is called. They are constants here rather than configuration for
	// the reason models/auth.go gives.
	AdminGroupName   string `json:"admin_group_name,omitempty"`
	CuratorGroupName string `json:"curator_group_name,omitempty"`
}

// AuthSettingsOutput is the configuration.
type AuthSettingsOutput struct {
	Body AuthSettingsItem
}

func (a *API) getAuthSettings(ctx context.Context, _ *struct{}) (*AuthSettingsOutput, error) {
	settings, err := a.store.AuthSettings(ctx)
	if err != nil {
		log.Error().Err(err).Msg("cannot read the auth settings")
		return nil, huma.Error500InternalServerError("cannot read the auth settings")
	}

	out := &AuthSettingsOutput{Body: AuthSettingsItem{
		Provisioned:   settings.Provisioned,
		InstanceURL:   settings.InstanceURL,
		ConsoleURL:    settings.ConsoleURL,
		ClientID:      settings.ClientID,
		HasToken:      settings.HasToken(),
		AdminGroup:    settings.AdminGroup,
		CuratorGroup:  settings.CuratorGroup,
		ProvisionedBy: settings.ProvisionedBy,
		AppName:       settings.AppName,
	}}
	// Only once there is a name. An unprovisioned installation has none, and
	// saying its groups are called "_admin" would be answering a question
	// nobody has settled yet.
	if settings.AppName != "" {
		out.Body.AdminGroupName = models.AdminGroupName(settings.AppName)
		out.Body.CuratorGroupName = models.CuratorGroupName(settings.AppName)
	}
	if settings.ProvisionedAt != nil {
		out.Body.ProvisionedAt = settings.ProvisionedAt.Format("2006-01-02T15:04:05Z07:00")
	}
	return out, nil
}

// AuthCredentialsOutput carries the two secrets to the console.
type AuthCredentialsOutput struct {
	Body struct {
		ClientSecret string `json:"client_secret"`
		APIToken     string `json:"api_token"`
	}
}

// getAuthCredentials hands the console what it needs to authenticate people.
//
// # This is a departure from "exposed services hold no credentials", and it
// # should be read as one
//
// The console and the frontend are meant to talk to this backend over the API
// and hold no credentials to anything else, which is most of why those images
// can be distroless and read-only with so little to offer an attacker. This
// endpoint weakens that: a console that has called it holds, in memory, the
// OAuth client secret and an authentik token that can create users.
//
// The OAuth secret genuinely has to live there — the console is what exchanges
// an authorization code, because the console is what has the browser. The API
// token does not: a stricter arrangement would keep it in the backend and give
// the console endpoints for reading roles and managing people, so the thing
// that faces staff never holds a credential to the directory at all.
//
// That is the better shape and it is not built. What makes this tolerable
// meanwhile is that the backend is not publicly exposed — only the console and
// the frontend reach it — so this endpoint is reachable from exactly the two
// services that already hold its answers' consequences. It is written down
// here rather than discovered later.
func (a *API) getAuthCredentials(ctx context.Context, _ *struct{}) (*AuthCredentialsOutput, error) {
	settings, err := a.store.AuthSettings(ctx)
	if err != nil {
		log.Error().Err(err).Msg("cannot read the auth credentials")
		return nil, huma.Error500InternalServerError("cannot read the auth credentials")
	}
	if !settings.Provisioned {
		return nil, huma.Error404NotFound("no identity provider is configured")
	}

	out := &AuthCredentialsOutput{}
	out.Body.ClientSecret = settings.ClientSecret
	out.Body.APIToken = settings.APIToken
	return out, nil
}

// AuthSettingsInput is what the wizard reports having provisioned.
type AuthSettingsInput struct {
	Body struct {
		AppName      string `json:"app_name"`
		InstanceURL  string `json:"instance_url"`
		ConsoleURL   string `json:"console_url"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		APIToken     string `json:"api_token"`
		AdminGroup   string `json:"admin_group"`
		CuratorGroup string `json:"curator_group"`

		// ProvisionedBy is the authentik account whose token did it. The
		// wizard runs before anybody can be identified by OIDC, so this is
		// the nearest thing to a name on the one privileged action that
		// cannot be audited the way every later one is.
		ProvisionedBy string `json:"provisioned_by,omitempty"`

		// Reprovision says this replaces a configuration that already exists.
		// The console sends it only when it was started with the flag that
		// reopens the setup door.
		Reprovision bool `json:"reprovision,omitempty"`
	}
}

func (a *API) saveAuthSettings(ctx context.Context, in *AuthSettingsInput) (*AuthSettingsOutput, error) {
	for name, value := range map[string]string{
		"app_name":      in.Body.AppName,
		"instance_url":  in.Body.InstanceURL,
		"console_url":   in.Body.ConsoleURL,
		"client_id":     in.Body.ClientID,
		"client_secret": in.Body.ClientSecret,
		"api_token":     in.Body.APIToken,
		"admin_group":   in.Body.AdminGroup,
		"curator_group": in.Body.CuratorGroup,
	} {
		if value == "" {
			return nil, huma.Error422UnprocessableEntity(name + " is required")
		}
	}

	settings := models.AuthSettings{
		Provisioned:   true,
		AppName:       in.Body.AppName,
		InstanceURL:   in.Body.InstanceURL,
		ConsoleURL:    in.Body.ConsoleURL,
		ClientID:      in.Body.ClientID,
		ClientSecret:  in.Body.ClientSecret,
		APIToken:      in.Body.APIToken,
		AdminGroup:    in.Body.AdminGroup,
		CuratorGroup:  in.Body.CuratorGroup,
		ProvisionedBy: in.Body.ProvisionedBy,
	}

	err := a.store.SaveAuthSettings(ctx, settings, in.Body.Reprovision)
	if errors.Is(err, store.ErrAlreadyProvisioned) {
		return nil, huma.Error409Conflict(
			"this console already has an identity provider; restart it with the setup flag to replace one")
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot save the auth settings")
		return nil, huma.Error500InternalServerError("cannot save the auth settings")
	}

	// WARN, and it earns it. This is the moment a register decides which
	// directory may administer it, and a line in bulk here is somebody trying
	// repeatedly to repoint a console that is already configured.
	log.Warn().
		Str("instance", in.Body.InstanceURL).
		Str("by", in.Body.ProvisionedBy).
		Bool("replacing", in.Body.Reprovision).
		Msg("identity provider configured: this console's administration now answers to it")

	return a.getAuthSettings(ctx, nil)
}
