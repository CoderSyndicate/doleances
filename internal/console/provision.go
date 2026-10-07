package console

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/apiclient"
	"github.com/CoderSyndicate/doleances/internal/authentik"
	"github.com/CoderSyndicate/doleances/internal/console/auth"
	"github.com/CoderSyndicate/doleances/internal/models"
)

// names builds what this register is called in somebody else's directory, from
// the one name an operator gives at setup.
//
// All four derive from it so that two installations on one authentik — a test
// beside the real thing, say — cannot collide. Without the prefix the second
// would find the first's groups already there, join them, and quietly give its
// curators power over somebody else's register.
type names struct {
	app      string
	provider string
	admins   string
	curators string
}

func namesFor(app string) names {
	return names{
		app:      app,
		provider: app + " console",
		admins:   models.AdminGroupName(app),
		curators: models.CuratorGroupName(app),
	}
}

// Provisioning is what the wizard did, or why it could not.
type Provisioning struct {
	// Done says an identity provider is now configured.
	Done bool

	// Missing is what the instance lacks, in words an operator can act on.
	// Non-empty with Done false means nothing was created.
	Missing []string

	// AdminUsername is who was made the first admin: the person whose API
	// token ran the wizard. They are not asked to type it — the token already
	// says who they are, and asking would be a field somebody can get wrong.
	AdminUsername string

	// InstanceURL is where this console now sends its staff to sign in.
	InstanceURL string

	// AppName is what this register is called in that directory, and the
	// prefix its two group names are built from.
	AppName string

	// RecoveryReady says people can be given a way in. False means everything
	// is provisioned and nobody but the first admin can be onboarded yet,
	// which is a real state and has to be said rather than discovered.
	RecoveryReady bool

	// RecoveryFlow says what was done about the one flow this wizard does
	// build. See authentik.EnsureRecoveryFlow for why it is the exception.
	RecoveryFlow authentik.RecoveryFlow

	// ConsoleToken is the credential the console will keep, which is **not**
	// the one that was pasted into the wizard. See
	// authentik.EnsureConsoleToken.
	ConsoleToken authentik.ConsoleToken

	// TokenOwner is whose API token ran the wizard, which is not necessarily
	// who the register now answers to.
	TokenOwner string

	// AdminLink is a way in for the first admin.
	//
	// They can already sign in — they hold an authentik account, which is
	// where the token came from — so this is an offer rather than a repair.
	// It is made because this is the one moment it costs nothing, and because
	// it is how the first admin sets up a passkey instead of relying on
	// whatever password their directory account already had. Shown once, like
	// everybody else's.
	AdminLink string

	// Provisioned is what the backend is told to store. It carries the two
	// secrets, which is why it goes straight to the backend and never to a
	// template.
	Provisioned apiclient.ProvisionedAuth
}

// provision creates this register's own objects in somebody else's directory,
// and nothing else.
//
// # The order is deliberate
//
// The token is proved first, by asking who it belongs to. Then what the
// instance already has is checked. Only then is anything created — so an
// operator who typed a URL wrong, or made a token without enough permission,
// finds out before their directory has half a provider in it.
//
// # Everything is find-or-create
//
// A wizard that failed halfway must be runnable again. The second attempt
// finds what the first one made rather than colliding with it, which is the
// difference between "press it again" and "go and tidy up authentik by hand".
//
// # It creates no flows
//
// Flows are instance-wide and shared with every other application in the
// operator's directory. A tool asked to add itself has no business rewriting
// how everybody else signs in, so what is missing is reported rather than
// provisioned. See authentik.Check.
// firstAdmin is who this register answers to, as the wizard was told.
//
// # Why it is asked rather than assumed
//
// The token's owner is whoever could make an API token, which on a fresh
// authentik is `akadmin` — the bootstrap account, not a person. Making that
// the register's first admin means the identity in the audit log is a shared
// built-in nobody signs in as, and the human actually running this is never
// onboarded at all: they would invite themselves from the console afterwards,
// which is the same flow with an extra step and a wrong row left behind.
//
// Empty is still right for the operator who already has their own account on
// their own directory and used it to make the token.
type firstAdmin struct {
	Username string
	Name     string
	Email    string
}

func (f firstAdmin) named() bool { return strings.TrimSpace(f.Username) != "" }

// ensureFirstAdmin finds or creates the person the wizard named.
//
// Find-or-create, like everything else here: naming somebody who already has
// an account is the ordinary case, and it must adopt that account rather than
// refuse or duplicate it. Nothing on an adopted record is overwritten — a name
// or an address typed into the wizard does not rewrite what their directory
// already says about them. Only the two group memberships are added.
func ensureFirstAdmin(ctx context.Context, client *authentik.Client,
	first firstAdmin) (authentik.User, error) {
	username := strings.TrimSpace(first.Username)

	existing, err := client.UserByUsername(ctx, username)
	switch {
	case err == nil:
		log.Info().Str("username", username).
			Msg("the first admin already exists in the directory; adopting that account")
		return existing, nil
	case !errors.Is(err, authentik.ErrNotFound):
		return authentik.User{}, fmt.Errorf("look for %q: %w", username, err)
	}

	name := strings.TrimSpace(first.Name)
	if name == "" {
		name = username
	}
	made, err := client.CreateUser(ctx, authentik.UserSpec{
		Username: username, Name: name, Email: strings.TrimSpace(first.Email),
	})
	if err != nil {
		return authentik.User{}, fmt.Errorf("create the first admin %q: %w", username, err)
	}
	return made, nil
}

func provision(ctx context.Context, client *authentik.Client,
	consoleURL, appName string, first firstAdmin) (Provisioning, error) {
	var result Provisioning
	result.InstanceURL = client.InstanceURL()
	result.AppName = appName

	if !models.ValidAppName(appName) {
		return result, fmt.Errorf(
			"%q is not a usable name: lowercase letters, digits, and - or _ inside", appName)
	}
	named := namesFor(appName)

	// Who the token belongs to — proof it works, and the first admin.
	me, err := client.Me(ctx)
	if err != nil {
		return result, fmt.Errorf("the token was refused: %w", err)
	}
	if me.Username == "" {
		return result, fmt.Errorf("authentik did not say who this token belongs to")
	}
	result.AdminUsername = me.Username
	result.TokenOwner = me.Username

	// What the instance already has.
	found, err := client.Check(ctx)
	if err != nil {
		return result, fmt.Errorf("check what the instance has: %w", err)
	}
	result.Missing = found.Missing

	// The one flow this builds, and only when the instance has nothing to hand
	// somebody an account with. A directory that already has a recovery flow
	// bound to its brand has an operator's decision in it and is left alone.
	if found.HasRecoveryFlow {
		result.RecoveryReady = true
	} else {
		recovery, err := client.EnsureRecoveryFlow(ctx, appName)
		if err != nil {
			// Not fatal: everything else can still be provisioned, and an
			// operator who builds their own flow afterwards has a working
			// console. Said rather than swallowed.
			log.Error().Err(err).Msg("cannot build a recovery flow; nobody can be onboarded yet")
		} else {
			result.RecoveryFlow = recovery
			result.RecoveryReady = recovery.BoundToBrand
			if !recovery.BoundToBrand {
				// Built but not reachable: the brand already names a recovery
				// flow of somebody else's, which is theirs to keep. Said as
				// the one thing left to decide rather than as a failure.
				result.Missing = append(result.Missing,
					"the default brand to use a recovery flow — this register built "+
						recovery.Slug+", and the brand already names another one")
			}
		}
	}

	// What the ID token will be signed with, before anything is created.
	//
	// A provider with no signing key signs symmetrically with the client secret
	// and no OIDC library will verify it, so this is a prerequisite rather than
	// a nicety — and it is read here, beside the flows, so an instance that
	// lacks one is reported before this writes anything. That is not a
	// hypothetical: the first live run provisioned a complete, correct directory
	// and then failed every sign-in with `unexpected signature algorithm
	// "HS256"`.
	signingKey, err := client.SigningKey(ctx)
	switch {
	case errors.Is(err, authentik.ErrNotFound):
		found.Missing = append(found.Missing,
			"a certificate with a private key, which the provider needs to sign tokens")
		result.Missing = found.Missing
	case err != nil:
		return result, fmt.Errorf("find a signing certificate: %w", err)
	}

	if !found.Ready() || signingKey == "" {
		// Nothing created. The flows a provider needs are missing, and this
		// will not make them.
		log.Warn().Strs("missing", found.Missing).
			Msg("authentik is missing what a provider needs; nothing was created")
		return result, nil
	}

	// The claims the token carries. Not what authorisation depends on — roles
	// are read from the directory — so a scope an instance has renamed costs a
	// nicety rather than a login.
	scopes, err := client.ScopeMappings(ctx, "openid", "email", "profile")
	if err != nil {
		return result, fmt.Errorf("read the scope mappings: %w", err)
	}

	provider, err := client.EnsureProvider(ctx, authentik.ProviderSpec{
		Name:              named.provider,
		RedirectURI:       strings.TrimSuffix(consoleURL, "/") + auth.CallbackPath,
		AuthorizationFlow: found.AuthorizationFlow,
		InvalidationFlow:  found.InvalidationFlow,
		ScopeMappings:     scopes,
		SigningKey:        signingKey,
	})
	if err != nil {
		return result, err
	}

	if _, err := client.EnsureApplication(ctx, named.provider, named.app, provider.PK); err != nil {
		return result, err
	}

	admins, err := client.EnsureGroup(ctx, named.admins)
	if err != nil {
		return result, err
	}
	curators, err := client.EnsureGroup(ctx, named.curators)
	if err != nil {
		return result, err
	}

	// Who this register answers to: the person the wizard named, or the
	// token's owner when it named nobody.
	admin := me
	if first.named() {
		admin, err = ensureFirstAdmin(ctx, client, first)
		if err != nil {
			return result, err
		}
		result.AdminUsername = admin.Username
	}

	// The first admin gets **both** roles, not just admin.
	//
	// Without any of this the wizard finished with a console nobody can
	// administer, which is the same shape as a group with no admin. With only
	// admin, it finished with a register nobody can curate — a queue filling
	// up and one person who has to go and tick their own box to read it. The
	// two roles are independent by design, and that independence is about
	// *other* people: there is nobody else yet, so withholding one of them
	// from the only person here is a guess, and the wrong one.
	//
	// They can take curation off themselves afterwards, which is the ordinary
	// thing the people page is for.
	for _, group := range []struct {
		pk   string
		name string
	}{{admins.PK, named.admins}, {curators.PK, named.curators}} {
		if err := client.AddToGroup(ctx, group.pk, admin.PK); err != nil {
			return result, fmt.Errorf("put %s in %s: %w", admin.Username, group.name, err)
		}
	}

	// The credential the console keeps from here, which is **not** the one
	// that was pasted in. See authentik.EnsureConsoleToken: the pasted token
	// is a bootstrap credential and authentik expires it in thirty minutes by
	// default, which is right for what it is and wrong for what this console
	// was doing with it.
	keep, err := client.EnsureConsoleToken(ctx, appName, me.PK)
	if err != nil {
		return result, fmt.Errorf("mint this console's own token: %w", err)
	}
	result.ConsoleToken = keep

	// A way in for the first admin.
	//
	// For somebody this wizard just created it is the **only** way in, and
	// without it the register has an admin who cannot reach it. For the
	// token's owner it is an offer: they can already sign in, and this is how
	// they set up a passkey instead.
	if result.RecoveryReady {
		if link, err := client.RecoveryLink(ctx, admin.PK); err != nil {
			log.Error().Err(err).Str("username", admin.Username).
				Msg("no way in could be minted for the first admin")
			result.Missing = append(result.Missing,
				"a way in for "+admin.Username+" — the account exists and holds both roles, "+
					"and a link has to be minted from the people page before they can sign in")
		} else {
			result.AdminLink = link
		}
	}

	result.Done = true
	result.Provisioned = apiclient.ProvisionedAuth{
		AppName:       appName,
		InstanceURL:   client.InstanceURL(),
		ConsoleURL:    strings.TrimSuffix(consoleURL, "/"),
		ClientID:      provider.ClientID,
		ClientSecret:  provider.ClientSecret,
		AdminGroup:    admins.PK,
		CuratorGroup:  curators.PK,
		ProvisionedBy: admin.Username,
	}

	log.Warn().
		Str("instance", client.InstanceURL()).
		Str("first_admin", admin.Username).
		Str("provisioned_with", me.Username).
		Msg("authentik provisioned: this console's administration now answers to that directory")

	return result, nil
}
