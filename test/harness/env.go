package harness

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvFile is the local development environment, read from test/.env.
//
// It exists because some settings are secrets — the LLM API key above all —
// and a secret does not belong on a command line, where it is visible in `ps`
// output and in shell history, nor in the repository, where it is visible to
// everybody. The file is gitignored; test/.env.example is the committed
// template that says what belongs in it.
//
// This is a development convenience and nothing else. The services themselves
// have no notion of a .env file: a real deployment sets environment variables
// or mounts a config file, which is the same key namespace by a different
// route.
const EnvFile = ".env"

// LoadEnv reads test/.env into a list of KEY=VALUE strings.
//
// A missing file is the normal case and not an error: most local work needs no
// secret at all, and a runner that refused to start without one would be
// obstructive.
func LoadEnv(root string) ([]string, error) {
	path := filepath.Join(root, "test", EnvFile)

	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close() //nolint:errcheck

	var env []string
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		entry, err := parseEnvLine(scanner.Text())
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		if entry != "" {
			env = append(env, entry)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return env, nil
}

// parseEnvLine reads one KEY=VALUE line, returning "" for blanks and comments.
func parseEnvLine(line string) (string, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", nil
	}

	// "export FOO=bar" is what people paste out of shell notes.
	trimmed = strings.TrimPrefix(trimmed, "export ")

	key, value, found := strings.Cut(trimmed, "=")
	if !found {
		return "", fmt.Errorf("no '=' in %q", line)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("no name before '=' in %q", line)
	}

	value = strings.TrimSpace(value)
	// Quotes are stripped rather than passed through: a key wrapped in quotes
	// would otherwise be sent to the API with the quotes in it, and the
	// failure — a 401 from a key that looks correct on screen — is a horrible
	// one to debug.
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
	}
	return key + "=" + value, nil
}

// EnvNames lists the names in a loaded environment, for logging.
//
// Names only, never values: the whole reason this file exists is that the
// values are secret, and a runner that printed them to the terminal and into
// every run's transcript would defeat its own purpose.
func EnvNames(env []string) []string {
	names := make([]string, 0, len(env))
	for _, entry := range env {
		if key, _, found := strings.Cut(entry, "="); found {
			names = append(names, key)
		}
	}
	return names
}
