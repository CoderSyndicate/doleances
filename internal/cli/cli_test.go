package cli

import (
	"strings"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/config"
	"github.com/spf13/cobra"
)

func testDefinition() Definition {
	return Definition{
		Name:                   "test-service",
		Title:                  "Test API",
		Short:                  "a test service",
		DefaultAppPort:         9100,
		DefaultMaintenancePort: 9101,
	}
}

func TestBuildRegistersCommonFlags(t *testing.T) {
	cmd := Build(testDefinition(), "1.0.0", "abc1234")

	for _, name := range []string{
		config.KeyLogLevel, config.KeyLogFile, config.KeyLogPretty,
		config.KeyAppPort, config.KeyMaintenancePort, config.KeyDevelopment,
	} {
		if cmd.PersistentFlags().Lookup(name) == nil {
			t.Errorf("flag %q was not registered", name)
		}
	}
}

func TestBuildAppliesDefaultPorts(t *testing.T) {
	cmd := Build(testDefinition(), "1.0.0", "abc1234")

	if got := cmd.PersistentFlags().Lookup(config.KeyAppPort).DefValue; got != "9100" {
		t.Errorf("app port default = %q, want 9100", got)
	}
	if got := cmd.PersistentFlags().Lookup(config.KeyMaintenancePort).DefValue; got != "9101" {
		t.Errorf("maintenance port default = %q, want 9101", got)
	}
}

func TestDevelopmentNeverDefaultsOn(t *testing.T) {
	// An auth-disabling flag reaches production exactly once.
	cmd := Build(testDefinition(), "1.0.0", "abc1234")

	if got := cmd.PersistentFlags().Lookup(config.KeyDevelopment).DefValue; got != "false" {
		t.Errorf("development default = %q, want false", got)
	}
}

func TestBuildIncludesVersionAndCommit(t *testing.T) {
	cmd := Build(testDefinition(), "1.2.3", "abc1234")

	if !strings.Contains(cmd.Version, "1.2.3") || !strings.Contains(cmd.Version, "abc1234") {
		t.Errorf("version = %q, want it to carry both version and commit", cmd.Version)
	}
}

func TestRegisterFlagsHookRuns(t *testing.T) {
	def := testDefinition()
	def.RegisterFlags = func(cmd *cobra.Command) {
		cmd.PersistentFlags().String("service-specific", "", "")
	}

	if Build(def, "1.0.0", "abc").PersistentFlags().Lookup("service-specific") == nil {
		t.Error("the service's own flags were not registered")
	}
}

func TestRunReportsInvalidConfiguration(t *testing.T) {
	config.Reset()
	t.Cleanup(config.Reset)

	cmd := Build(testDefinition(), "1.0.0", "abc")
	// Identical ports: the maintenance port exists to stay separate.
	cmd.SetArgs([]string{"--app-port", "9100", "--maintenance-port", "9100"})
	cmd.SetOut(nil)
	cmd.SetErr(nil)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected the service to refuse to start")
	}
	if !strings.Contains(err.Error(), "configuration") {
		t.Errorf("error = %q, want it to name the configuration as the cause", err)
	}
}
