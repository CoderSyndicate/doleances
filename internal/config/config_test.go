package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// newCommand builds a command wired exactly as a service's root command is.
func newCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	Reset()
	t.Cleanup(Reset)

	cmd := &cobra.Command{Use: "test", RunE: func(*cobra.Command, []string) error { return nil }}
	RegisterCommonFlags(cmd, 8080, 8081)
	cmd.SetArgs(args)
	cmd.SetOut(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if err := Bootstrap(cmd); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return cmd
}

func TestDefaults(t *testing.T) {
	newCommand(t)

	got, err := LoadCommon()
	if err != nil {
		t.Fatalf("LoadCommon: %v", err)
	}
	if got.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", got.LogLevel)
	}
	if got.AppPort != 8080 || got.MaintenancePort != 8081 {
		t.Errorf("ports = %d/%d, want 8080/8081", got.AppPort, got.MaintenancePort)
	}
	if got.Development {
		t.Error("Development defaulted to true; it must never default on")
	}
}

func TestEnvVarIsRead(t *testing.T) {
	t.Setenv("DOLEANCES_LOG_LEVEL", "warn")
	newCommand(t)

	got, err := LoadCommon()
	if err != nil {
		t.Fatalf("LoadCommon: %v", err)
	}
	if got.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want warn — the env key replacer is not mapping log-level", got.LogLevel)
	}
}

func TestFlagBeatsEnvVar(t *testing.T) {
	// The whole reason for Cobra+Viper: precedence without override logic.
	t.Setenv("DOLEANCES_LOG_LEVEL", "warn")
	newCommand(t, "--log-level", "trace")

	got, err := LoadCommon()
	if err != nil {
		t.Fatalf("LoadCommon: %v", err)
	}
	if got.LogLevel != "trace" {
		t.Errorf("LogLevel = %q, want trace — the flag must outrank the env var", got.LogLevel)
	}
}

func TestEnvVarBeatsConfigFile(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "log-level: error\napp-port: 1111\n")
	chdir(t, dir)

	t.Setenv("DOLEANCES_LOG_LEVEL", "debug")
	newCommand(t)

	got, err := LoadCommon()
	if err != nil {
		t.Fatalf("LoadCommon: %v", err)
	}
	if got.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug — the env var must outrank the file", got.LogLevel)
	}
	if got.AppPort != 1111 {
		t.Errorf("AppPort = %d, want 1111 — the file value should survive where nothing overrides it", got.AppPort)
	}
}

func TestFlagBeatsConfigFile(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "app-port: 1111\n")
	chdir(t, dir)

	newCommand(t, "--app-port", "2222")

	got, err := LoadCommon()
	if err != nil {
		t.Fatalf("LoadCommon: %v", err)
	}
	if got.AppPort != 2222 {
		t.Errorf("AppPort = %d, want 2222", got.AppPort)
	}
}

func TestMissingConfigFileIsNotAnError(t *testing.T) {
	chdir(t, t.TempDir())
	newCommand(t)

	if _, err := LoadCommon(); err != nil {
		t.Errorf("LoadCommon: %v — an absent config file is the normal case", err)
	}
}

func TestMalformedConfigFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "log-level: [unclosed\n")
	chdir(t, dir)

	Reset()
	t.Cleanup(Reset)
	cmd := &cobra.Command{Use: "test"}
	RegisterCommonFlags(cmd, 8080, 8081)

	if err := Bootstrap(cmd); err == nil {
		t.Error("expected an error for an unparseable config file")
	}
}

func TestValidate(t *testing.T) {
	base := Common{LogLevel: "info", LogBufferSize: 10, AppPort: 8080, MaintenancePort: 8081}

	tests := []struct {
		name    string
		mutate  func(*Common)
		wantErr string
	}{
		{"valid", func(*Common) {}, ""},
		{"unknown level", func(c *Common) { c.LogLevel = "chatty" }, "log-level"},
		{"app port zero", func(c *Common) { c.AppPort = 0 }, "app-port"},
		{"app port too high", func(c *Common) { c.AppPort = 70000 }, "app-port"},
		{"maintenance port invalid", func(c *Common) { c.MaintenancePort = -1 }, "maintenance-port"},
		{"ports collide", func(c *Common) { c.MaintenancePort = c.AppPort }, "stay separate"},
		{"buffer size zero", func(c *Common) { c.LogBufferSize = 0 }, "log-buffer-size"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base
			tt.mutate(&c)

			err := c.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Validate: %v", err)
			case tt.wantErr == "":
				return
			case err == nil:
				t.Fatalf("expected an error mentioning %q", tt.wantErr)
			case !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("error = %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadCommonRejectsInvalidValues(t *testing.T) {
	newCommand(t, "--app-port", "0")

	if _, err := LoadCommon(); err == nil {
		t.Error("expected LoadCommon to fail validation at startup")
	}
}

func writeConfig(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(old) }) //nolint:errcheck
}

// TestDefaultRegisterLanguagesFitOneRequest: Wikidata refuses the 51st
// language code with "toomanyvalues" and takes the whole request with it, so
// the default list is sized to their limit rather than to a round number.
func TestDefaultRegisterLanguagesFitOneRequest(t *testing.T) {
	if len(defaultRegisterLanguages) > MaxLabelLanguages {
		t.Errorf("%d default languages, and Wikidata accepts %d per request",
			len(defaultRegisterLanguages), MaxLabelLanguages)
	}

	seen := map[string]bool{}
	for _, language := range defaultRegisterLanguages {
		if len(language) != 2 {
			// A bad code is answered with an error for the whole request, so
			// one typo costs every language in the list.
			t.Errorf("%q is not a two-letter code", language)
		}
		if seen[language] {
			t.Errorf("%q is listed twice, wasting one of the fifty", language)
		}
		seen[language] = true
	}

	// The 24 official languages of the EU are in full, deliberately: a
	// register organising European grievances that cannot read Maltese is
	// making a statement it does not mean to make.
	for _, official := range []string{
		"bg", "cs", "da", "de", "el", "en", "es", "et", "fi", "fr",
		"ga", "hr", "hu", "it", "lt", "lv", "mt", "nl", "pl", "pt",
		"ro", "sk", "sl", "sv",
	} {
		if !seen[official] {
			t.Errorf("%q is an official EU language and is missing", official)
		}
	}

	// And the peripheries this register exists to hear from.
	for _, regional := range []string{"br", "ca", "cy", "eu", "oc"} {
		if !seen[regional] {
			t.Errorf("%q is missing; the project's own i18n rule names Occitan as the case to handle", regional)
		}
	}
}
