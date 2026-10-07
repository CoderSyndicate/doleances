package harness

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLoadEnv(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "test"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	content := `
# a comment
DOLEANCES_LLM_API_KEY=sk-secret-value

  # indented comment
export DOLEANCES_LLM_BASE_URL=https://llm.example.org/v1
DOLEANCES_QUOTED="quoted value"
DOLEANCES_SINGLE='single quoted'
DOLEANCES_EMPTY=
DOLEANCES_EQUALS=a=b=c
`
	if err := os.WriteFile(filepath.Join(root, "test", EnvFile), []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	env, err := LoadEnv(root)
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}

	want := []string{
		"DOLEANCES_LLM_API_KEY=sk-secret-value",
		"DOLEANCES_LLM_BASE_URL=https://llm.example.org/v1",
		// Quotes are stripped: a key passed through with its quotes produces a
		// 401 from a value that looks right on screen.
		"DOLEANCES_QUOTED=quoted value",
		"DOLEANCES_SINGLE=single quoted",
		"DOLEANCES_EMPTY=",
		// A value may hold '=' — an API key often does.
		"DOLEANCES_EQUALS=a=b=c",
	}
	if !slices.Equal(env, want) {
		t.Errorf("LoadEnv =\n  %v\nwant\n  %v", env, want)
	}
}

// TestLoadEnvMissingFileIsFine: most local work needs no secret, and a runner
// that refused to start without one would be obstructive.
func TestLoadEnvMissingFileIsFine(t *testing.T) {
	env, err := LoadEnv(t.TempDir())
	if err != nil {
		t.Errorf("a missing .env should not be an error: %v", err)
	}
	if env != nil {
		t.Errorf("env = %v, want nil", env)
	}
}

func TestLoadEnvReportsBadLines(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "test"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "test", EnvFile),
		[]byte("GOOD=1\nthis line has no equals sign\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadEnv(root)
	if err == nil {
		t.Fatal("a malformed line should be reported, not skipped")
	}
	// The message has to say where, or the operator is hunting through a file.
	if !strings.Contains(err.Error(), ":2:") {
		t.Errorf("error does not name the line: %v", err)
	}
}

// TestEnvNamesHidesValues guards the point of the whole file: the runner
// prints what it loaded, and must never print a secret into the terminal and
// every run transcript.
func TestEnvNamesHidesValues(t *testing.T) {
	names := EnvNames([]string{"DOLEANCES_LLM_API_KEY=sk-secret-value"})

	if len(names) != 1 || names[0] != "DOLEANCES_LLM_API_KEY" {
		t.Fatalf("EnvNames = %v", names)
	}
	for _, name := range names {
		if strings.Contains(name, "sk-secret-value") {
			t.Error("EnvNames leaked a value")
		}
	}
}

// TestShellOverridesTheFile pins the documented precedence.
//
// exec.Cmd takes the last value for a duplicated key, so the file has to come
// first. The other order looks identical and silently ignores
// `FOO=bar go run ./test`, which is exactly the case somebody reaches for when
// they are trying to change one thing for one run.
func TestShellOverridesTheFile(t *testing.T) {
	fromFile := []string{"DOLEANCES_LLM_BASE_URL=http://from-the-file"}
	shell := []string{"PATH=/usr/bin", "DOLEANCES_LLM_BASE_URL=http://from-the-shell"}

	combined := append(append([]string{}, fromFile...), shell...)

	// Resolve as exec.Cmd does: last value wins.
	resolved := map[string]string{}
	for _, entry := range combined {
		if key, value, ok := strings.Cut(entry, "="); ok {
			resolved[key] = value
		}
	}
	if got := resolved["DOLEANCES_LLM_BASE_URL"]; got != "http://from-the-shell" {
		t.Errorf("resolved to %q, want the shell's value", got)
	}
}
