// Package cli assembles a service binary: flags, configuration, logging and
// the service runtime.
//
// It exists so that each cmd/<service>/main.go only declares what makes its
// service different and hands over — no application logic under cmd/.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/CoderSyndicate/doleances/internal/config"
	"github.com/CoderSyndicate/doleances/internal/logbuffer"
	"github.com/CoderSyndicate/doleances/internal/logging"
	"github.com/CoderSyndicate/doleances/internal/service"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// Definition describes one service binary.
type Definition struct {
	// Name is the binary and service name, e.g. "backend".
	Name string

	// Title is the human-readable API name for the docs UI.
	Title string

	// Short is the one-line command description.
	Short string

	// Long is the command's extended help.
	Long string

	DefaultAppPort         int
	DefaultMaintenancePort int

	// RegisterFlags declares flags this service needs beyond the common ones.
	// Only a service that owns data registers database flags, for instance.
	RegisterFlags func(*cobra.Command)

	// Setup registers the service's own routes and dependencies. It runs
	// after logging and configuration are in place and before the listeners
	// start.
	Setup func(*service.Service, config.Common) error
}

// Build returns the root command for a service.
func Build(def Definition, version, commit string) *cobra.Command {
	cmd := &cobra.Command{
		Use:           def.Name,
		Short:         def.Short,
		Long:          def.Long,
		Version:       fmt.Sprintf("%s (commit: %s)", version, commit),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return config.Bootstrap(cmd)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd.Context(), def, version, commit)
		},
	}
	config.RegisterCommonFlags(cmd, def.DefaultAppPort, def.DefaultMaintenancePort)
	if def.RegisterFlags != nil {
		def.RegisterFlags(cmd)
	}
	return cmd
}

// Execute builds and runs a service, reporting the process exit code.
//
// Errors are returned up to here rather than ending the process where they
// happen, so a failure still unwinds through the service's graceful shutdown
// instead of killing in-flight work.
func Execute(def Definition, version, commit string) int {
	if err := Build(def, version, commit).ExecuteContext(context.Background()); err != nil {
		// The logger may not exist yet if configuration itself failed.
		log.Error().Err(err).Str("service", def.Name).Msg("service failed")
		fmt.Fprintf(os.Stderr, "%s: %v\n", def.Name, err)
		return 1
	}
	return 0
}

func run(ctx context.Context, def Definition, version, commit string) error {
	cfg, err := config.LoadCommon()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	// The buffer has to exist before the logger, or the first lines are lost.
	buffer := logbuffer.New(cfg.LogBufferSize)
	logFile, logErr := logging.Init(logging.Options{
		Level:       cfg.LogLevel,
		FilePath:    cfg.LogFile,
		PrettyPrint: cfg.LogPretty,
		Buffer:      buffer,
	})
	if logErr != nil {
		// Reported once the logger exists: a log file we cannot open is worth
		// knowing about but never worth refusing to start over.
		log.Warn().Err(logErr).Msg("continuing without a log file")
	}

	svc, err := service.New(service.Options{
		Name:            def.Name,
		Title:           def.Title,
		Version:         version,
		Commit:          commit,
		AppPort:         cfg.AppPort,
		MaintenancePort: cfg.MaintenancePort,
		LogBuffer:       buffer,
		LogFilePath:     logFile,
		Development:     cfg.Development,
	})
	if err != nil {
		return fmt.Errorf("build service: %w", err)
	}

	if def.Setup != nil {
		if err := def.Setup(svc, cfg); err != nil {
			return fmt.Errorf("set up %s: %w", def.Name, err)
		}
	}

	if err := svc.Run(ctx); err != nil {
		return errors.Join(fmt.Errorf("run %s", def.Name), err)
	}
	return nil
}
