// Package config resolves configuration from a file, the environment and
// command-line flags.
//
// Viper merges the three in that order of increasing priority
// (file < env < flag) on its own; nothing here reimplements that precedence.
package config

import (
	"fmt"
	"strings"

	"github.com/CoderSyndicate/doleances/internal/logging"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// EnvPrefix is the binary name, upper-cased: the key "log-level" is read from
// DOLEANCES_LOG_LEVEL. Naming it after the binary keeps two services on one
// host from reading each other's settings.
const EnvPrefix = "DOLEANCES"

// Configuration keys. The same string names a flag, an environment variable
// and a config-file field — there is never a second spelling for a setting.
const (
	KeyLogLevel        = "log-level"
	KeyLogFile         = "log-file"
	KeyLogPretty       = "log-pretty"
	KeyLogBufferSize   = "log-buffer-size"
	KeyAppPort         = "app-port"
	KeyMaintenancePort = "maintenance-port"
	KeyDevelopment     = "development"
)

// Common is the configuration every service shares.
type Common struct {
	LogLevel        string
	LogFile         string
	LogPretty       bool
	LogBufferSize   int
	AppPort         int
	MaintenancePort int

	// Development disables authentication. It must never default to true and
	// is logged loudly at every startup that enables it.
	Development bool
}

// Bootstrap wires Viper to a command: environment variables, an optional
// config file, and every registered flag. Call it from PersistentPreRun so
// each subcommand inherits the same resolved configuration.
func Bootstrap(cmd *cobra.Command) error {
	viper.SetEnvPrefix(EnvPrefix)
	// Without this, "log-level" would look for DOLEANCES_LOG-LEVEL, which is
	// not a legal shell identifier — the lookup would silently never match.
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	viper.AutomaticEnv()

	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	viper.AddConfigPath("/etc/doleances")
	if err := viper.ReadInConfig(); err != nil {
		// A missing config file is the normal case, not a failure.
		if _, notFound := err.(viper.ConfigFileNotFoundError); !notFound {
			return fmt.Errorf("read config file: %w", err)
		}
	}

	var bindErr error
	bindAll := func(flag *pflag.Flag) {
		if bindErr != nil {
			return
		}
		if err := viper.BindPFlag(flag.Name, flag); err != nil {
			bindErr = fmt.Errorf("bind flag %q: %w", flag.Name, err)
		}
	}
	cmd.PersistentFlags().VisitAll(bindAll)
	cmd.Flags().VisitAll(bindAll)
	return bindErr
}

// RegisterCommonFlags declares the flags every service accepts.
func RegisterCommonFlags(cmd *cobra.Command, defaultAppPort, defaultMaintenancePort int) {
	f := cmd.PersistentFlags()
	f.String(KeyLogLevel, "info", "log level (trace, debug, info, warn, error, fatal, panic)")
	f.String(KeyLogFile, "", "append raw JSON log events to this file")
	f.Bool(KeyLogPretty, false, "expand structured log field values across indented lines")
	f.Int(KeyLogBufferSize, logging.DefaultBufferSize, "number of recent log lines kept for streaming")
	f.Int(KeyAppPort, defaultAppPort, "port serving application features")
	f.Int(KeyMaintenancePort, defaultMaintenancePort, "port serving health, metrics and logs")
	f.Bool(KeyDevelopment, false, "DEVELOPMENT ONLY: disable authentication")
}

// LoadCommon reads the resolved values. Call it after Bootstrap.
func LoadCommon() (Common, error) {
	c := Common{
		LogLevel:        viper.GetString(KeyLogLevel),
		LogFile:         viper.GetString(KeyLogFile),
		LogPretty:       viper.GetBool(KeyLogPretty),
		LogBufferSize:   viper.GetInt(KeyLogBufferSize),
		AppPort:         viper.GetInt(KeyAppPort),
		MaintenancePort: viper.GetInt(KeyMaintenancePort),
		Development:     viper.GetBool(KeyDevelopment),
	}
	return c, c.Validate()
}

// Validate rejects a configuration that cannot work, at startup rather than at
// first use.
func (c Common) Validate() error {
	if _, err := logging.ParseLevel(c.LogLevel); err != nil {
		return fmt.Errorf("invalid %s %q: %w", KeyLogLevel, c.LogLevel, err)
	}
	if err := validatePort(KeyAppPort, c.AppPort); err != nil {
		return err
	}
	if err := validatePort(KeyMaintenancePort, c.MaintenancePort); err != nil {
		return err
	}
	if c.AppPort == c.MaintenancePort {
		return fmt.Errorf(
			"%s and %s are both %d: the maintenance port exists to stay separate from the application port",
			KeyAppPort, KeyMaintenancePort, c.AppPort)
	}
	if c.LogBufferSize < 1 {
		return fmt.Errorf("invalid %s %d: must be at least 1", KeyLogBufferSize, c.LogBufferSize)
	}
	return nil
}

func validatePort(key string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid %s %d: must be between 1 and 65535", key, port)
	}
	return nil
}

// Reset clears Viper's state. Tests only.
func Reset() {
	viper.Reset()
}
