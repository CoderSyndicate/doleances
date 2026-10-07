package console

import (
	"context"
	"errors"
	"fmt"

	"github.com/CoderSyndicate/doleances/internal/authentik"
	"github.com/CoderSyndicate/doleances/internal/console/auth"
)

// directoryRoles answers what somebody may do by asking authentik, which is
// the only thing that knows.
//
// The plan's rule, kept where it is easiest to break: **authentik is
// authoritative.** Nothing here caches membership beyond the session lifetime
// the authenticator enforces, and nothing reads roles out of a token — a token
// is a snapshot of the moment somebody signed in, and a curator removed from
// the directory an hour ago would still be carrying a claim that says they are
// one.
type directoryRoles struct {
	client *authentik.Client

	// adminGroup and curatorGroup are the identifiers the wizard recorded.
	// Compared rather than the names, because a group renamed in authentik is
	// the same group and should keep working — the names are the contract for
	// humans, the identifiers are the handle.
	adminGroup   string
	curatorGroup string
}

// RolesOf reads somebody's roles from the directory.
//
// A person authentik does not know, or no longer knows, holds nothing — which
// is not an error. Somebody deleted from the directory between signing in and
// pressing accept should be refused, and refused the same way as somebody who
// was simply never given a role.
func (d directoryRoles) RolesOf(ctx context.Context, _, username string) (map[auth.Role]bool, error) {
	if username == "" {
		return nil, errors.New("cannot read roles without a username")
	}

	person, err := d.client.UserByUsername(ctx, username)
	if errors.Is(err, authentik.ErrNotFound) {
		return map[auth.Role]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ask authentik about %q: %w", username, err)
	}

	// An inactive account holds nothing, whatever groups it is still in.
	// Deactivating somebody is how an admin removes them without deleting a
	// record other systems in the operator's directory may depend on.
	if !person.IsActive {
		return map[auth.Role]bool{}, nil
	}

	roles := map[auth.Role]bool{}
	for _, group := range person.Groups {
		switch group {
		case d.adminGroup:
			roles[auth.RoleAdmin] = true
		case d.curatorGroup:
			roles[auth.RoleCurator] = true
		}
	}
	return roles, nil
}
