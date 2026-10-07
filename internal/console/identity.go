package console

import (
	"context"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/authentik"
	"github.com/CoderSyndicate/doleances/internal/console/auth"
	"github.com/CoderSyndicate/doleances/internal/models"
)

// connectIdentityProvider builds everything the console needs to sign people
// in, from what the wizard stored.
//
// # It is allowed to fail without stopping the console
//
// A console that cannot reach its directory should still start, say so, and
// serve its setup wizard if that is what is wanted. Refusing to boot would
// turn a directory outage into an outage of the very thing an operator uses to
// look at the problem — and the gate already refuses everybody when there is
// no authenticator, so failing to connect is a locked console rather than an
// open one.
func (c *console) connectIdentityProvider(ctx context.Context) error {
	settings, err := c.backend.AuthSettings(ctx)
	if err != nil {
		return fmt.Errorf("read the auth settings: %w", err)
	}
	if !settings.Provisioned {
		log.Warn().Msg("no identity provider configured: nobody can sign in to this console yet")
		return nil
	}

	// The console asks the backend for the credentials it is allowed to have.
	// The OAuth client secret and the API token never come back over this API
	// — which is deliberate and is also why this call exists at all.
	credentials, err := c.backend.AuthCredentials(ctx)
	if err != nil {
		return fmt.Errorf("read the auth credentials: %w", err)
	}

	directory, err := authentik.New(settings.InstanceURL, credentials.APIToken)
	if err != nil {
		return fmt.Errorf("build the directory client: %w", err)
	}

	// The issuer is the application's own, which is how authentik scopes a
	// discovery document to one provider.
	issuer := strings.TrimSuffix(settings.InstanceURL, "/") +
		"/application/o/" + settings.AppName + "/"

	authenticator, err := auth.New(ctx, auth.Config{
		IssuerURL:    issuer,
		ClientID:     settings.ClientID,
		ClientSecret: credentials.ClientSecret,
		// The console's own address, as the wizard registered it — not
		// --site-url, which is the public register's.
		RedirectURL: strings.TrimSuffix(settings.ConsoleURL, "/") + auth.CallbackPath,
	}, directoryRoles{
		client:       directory,
		adminGroup:   settings.AdminGroup,
		curatorGroup: settings.CuratorGroup,
	})
	if err != nil {
		return err
	}

	c.authenticator = authenticator
	c.directory = directory
	c.adminGroup = settings.AdminGroup
	c.curatorGroup = settings.CuratorGroup
	c.adminGroupName = models.AdminGroupName(settings.AppName)
	c.curatorGroupName = models.CuratorGroupName(settings.AppName)

	log.Info().Str("instance", settings.InstanceURL).Str("issuer", issuer).
		Msg("identity provider connected; staff sign in through it")
	return nil
}
