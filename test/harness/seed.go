package harness

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoSuchRun is returned when the run named as a starting state is not there.
var ErrNoSuchRun = errors.New("no such run")

// ResolveRun finds a previous run directory from what somebody typed.
//
// It is deliberately forgiving about the form, because the useful thing to
// paste is whatever is on screen: the path the launcher printed, the timestamp
// out of that path, or "latest". Refusing three of those four would be
// pedantry about a development tool.
func ResolveRun(root, name string) (string, error) {
	base := filepath.Join(root, "test", "run")
	name = strings.TrimSuffix(strings.TrimSpace(name), string(filepath.Separator))
	if name == "" {
		return "", fmt.Errorf("%w: no run named", ErrNoSuchRun)
	}

	candidates := []string{
		name,                      // as typed, relative to the working directory
		filepath.Join(root, name), // relative to the module root
		filepath.Join(base, name), // a bare timestamp, or "latest"
		filepath.Join(root, "test", name),
	}

	for _, candidate := range candidates {
		resolved, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		// "latest" is a symlink; follow it so the copy reads the real run and
		// the message names the run rather than the link.
		if target, err := filepath.EvalSymlinks(resolved); err == nil {
			resolved = target
		}
		if info, err := os.Stat(resolved); err == nil && info.IsDir() {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrNoSuchRun, name)
}
