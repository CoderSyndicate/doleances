package console

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/apiclient"
)

// SetupDoor is whether this console offers the provisioning wizard, and why.
//
// A struct rather than a bool because the answer has two halves that must not
// be confused. "There is nothing configured yet" is the ordinary first run and
// needs no flag. "There is something configured and an operator deliberately
// came back to replace it" is an entirely different act, and the only one that
// can take a working register away from the directory that administers it.
type SetupDoor struct {
	// Open says the wizard route should be registered at all.
	//
	// When it is false the route is not registered — absent rather than
	// refusing. The project already does this for groups: *"switched off means
	// invisible, not disabled"*, because a feature that is still on screen and
	// refuses to work reads as a broken one.
	Open bool

	// Reprovision says an identity provider is already configured and this
	// console means to replace it. It is what the backend is told, and the
	// backend refuses the write without it.
	Reprovision bool
}

// How long to keep asking the backend at startup, and how often.
//
// Both services come up together on a first run, and whichever wins the race
// is not something either of them controls. Without this the console asks once
// into a closed port, fails closed — correctly — and a genuine first
// installation never sees the wizard it exists to show. Measured in seconds
// because that is what a cold start costs; a backend still absent after this
// is absent rather than slow.
const (
	setupAskTimeout  = 30 * time.Second
	setupAskInterval = time.Second
)

// askWithRetry gets the answer, waiting out a cold start.
//
// It retries only at startup, which is the whole point: the question is asked
// once, so a backend that has a bad second an hour later cannot reopen
// anything. What this absorbs is the one failure that is expected and
// harmless — the backend not yet listening when the console looked.
//
// **Only a connection that could not be made is waited on.** A backend that
// answers at all is up: a 500 from it is a backend that is broken rather than
// starting, and thirty seconds of asking it again changes nothing except how
// long the console takes to fail. The distinction is also what keeps this
// honest — "wait for a cold start" is a narrow thing, and a retry loop that
// swallowed every error would be a console that took half a minute to notice
// its backend was refusing it.
func askWithRetry(ctx context.Context, backend *apiclient.Client) (apiclient.AuthSettings, error) {
	deadline := time.Now().Add(setupAskTimeout)

	var (
		settings apiclient.AuthSettings
		err      error
	)
	for attempt := 1; ; attempt++ {
		settings, err = backend.AuthSettings(ctx)
		if err == nil {
			return settings, nil
		}
		if !starting(err) || time.Now().After(deadline) {
			return settings, err
		}
		if attempt == 1 {
			log.Info().Msg("waiting for the backend before deciding whether to offer setup")
		}
		select {
		case <-ctx.Done():
			return settings, err
		case <-time.After(setupAskInterval):
		}
	}
}

// starting reports whether an error is a backend that is not listening yet,
// as opposed to one that answered badly.
func starting(err error) bool {
	var dial *net.OpError
	return errors.As(err, &dial)
}

// decideSetupDoor works out whether the wizard belongs on this console.
//
// # Why the backend is asked rather than a local file
//
// The console holds no database, so "is this installation provisioned?" is a
// question only the backend can answer. Asking it at startup rather than per
// request is deliberate: the answer decides whether a route exists, and a
// route that appeared and disappeared with the backend's availability would
// be a setup wizard that opened itself whenever the backend had a bad second.
//
// # A backend that cannot be reached keeps the door shut
//
// The safe failure is the locked one. If this cannot find out whether an
// identity provider is configured, it assumes one is: an unreachable backend
// must never be a way to make a provisioned console offer its setup page
// again. The console then has no wizard and says why in the log, which is a
// problem an operator can see and fix, rather than a door nobody meant to open.
func decideSetupDoor(ctx context.Context, backend *apiclient.Client, superuser bool) SetupDoor {
	settings, err := askWithRetry(ctx, backend)
	if err != nil {
		if superuser {
			// Deliberate, and the operator is standing at a terminal having
			// typed the flag. Opening it is what they asked for, and the
			// backend still refuses an overwrite it does not like.
			log.Warn().Err(err).Msg(
				"cannot ask the backend whether an identity provider is configured; " +
					"--superuser was given, so the setup wizard is open anyway")
			return SetupDoor{Open: true, Reprovision: true}
		}
		log.Error().Err(err).Msg(
			"cannot ask the backend whether an identity provider is configured; " +
				"assuming one is and leaving the setup wizard closed")
		return SetupDoor{}
	}

	switch {
	case !settings.Provisioned:
		log.Warn().Msg(
			"no identity provider is configured: the setup wizard is open on the " +
				"maintenance port, which is not published — reach it through the cluster")
		return SetupDoor{Open: true}

	case superuser:
		log.Warn().Str("instance", settings.InstanceURL).Msg(
			"SUPERUSER MODE: an identity provider is already configured and the setup " +
				"wizard is open to replace it")
		return SetupDoor{Open: true, Reprovision: true}

	default:
		log.Info().Str("instance", settings.InstanceURL).
			Msg("identity provider configured; the setup wizard is not registered")
		return SetupDoor{}
	}
}
