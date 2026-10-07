// Package service is the runtime every doleances service is built on: two
// HTTP listeners, a middleware chain, an OpenAPI document with its docs UI,
// and a graceful shutdown that stops taking traffic before it stops working.
package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/CoderSyndicate/doleances/internal/health"
	"github.com/CoderSyndicate/doleances/internal/logbuffer"
	"github.com/CoderSyndicate/doleances/internal/metrics"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/rs/zerolog/log"
)

const (
	// readHeaderTimeout bounds how long a client may take to send its headers,
	// which is what makes a slowloris attempt cheap to survive.
	readHeaderTimeout = 10 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 120 * time.Second

	// defaultDrainDelay is how long the service keeps serving after
	// withdrawing readiness, so load balancers notice before the door closes.
	defaultDrainDelay = 5 * time.Second

	// shutdownTimeout bounds how long in-flight requests have to finish.
	shutdownTimeout = 25 * time.Second
)

// Options configures a service.
type Options struct {
	// Name identifies the service in metrics, logs and the OpenAPI document.
	Name string

	// Title is the human-readable API name shown in the docs UI.
	Title string

	Version string
	Commit  string

	AppPort         int
	MaintenancePort int

	// LogBuffer backs the maintenance log stream. Optional.
	LogBuffer *logbuffer.Buffer

	// LogFilePath backs the maintenance log-file endpoint. Optional.
	LogFilePath string

	// Development records that authentication is disabled. It only ever
	// arrives here as true deliberately.
	Development bool

	// DrainDelay overrides how long readiness stays withdrawn before the
	// listeners stop accepting. Zero uses defaultDrainDelay.
	DrainDelay time.Duration
}

// Service is a running pair of HTTP listeners sharing one lifecycle.
type Service struct {
	opts    Options
	health  *health.State
	metrics *metrics.Metrics

	appMux *http.ServeMux

	// extra are middleware a service adds for itself, applied to the
	// application port only. The maintenance port is probes and metrics and
	// has no use for any of it.
	extra          []Middleware
	maintenanceMux *http.ServeMux
	api            huma.API

	// onShutdown are run after the listeners stop, in reverse registration
	// order, so dependencies close after the things that use them.
	onShutdown []func(context.Context) error

	// readinessCheck is consulted by the readiness probe, so a service whose
	// dependencies have gone away stops being sent traffic.
	readinessCheck func(context.Context) error
}

// New builds a service. It does not listen: register routes first, then Run.
func New(opts Options) (*Service, error) {
	if opts.Name == "" {
		return nil, errors.New("service name is required")
	}
	if opts.Title == "" {
		opts.Title = opts.Name
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.DrainDelay <= 0 {
		opts.DrainDelay = defaultDrainDelay
	}

	m, err := metrics.New(opts.Name, opts.Version)
	if err != nil {
		return nil, fmt.Errorf("set up metrics: %w", err)
	}

	s := &Service{
		opts:           opts,
		health:         health.New(),
		metrics:        m,
		appMux:         http.NewServeMux(),
		maintenanceMux: http.NewServeMux(),
	}

	config := huma.DefaultConfig(opts.Title, opts.Version)
	// OpenAPIPath is a prefix: huma derives /openapi.json, /openapi.yaml and
	// the 3.0 downgrades from it. /openapi on its own is not a route.
	config.OpenAPIPath = "/openapi"
	config.DocsPath = "/docs"
	config.DocsRenderer = huma.DocsRendererScalar
	s.api = humago.New(s.appMux, config)

	s.registerMaintenanceRoutes()
	return s, nil
}

// API is the OpenAPI-typed surface. Register operations with huma.Register.
func (s *Service) API() huma.API { return s.api }

// Mux is the application listener's router, for handlers that cannot be
// expressed as typed huma operations.
func (s *Service) Mux() *http.ServeMux { return s.appMux }

// Use adds middleware to the application port, outside the handlers and
// inside the logging and recovery that every service gets.
//
// It exists because some cross-cutting concerns are not common to all three
// binaries: the two that serve HTML set a Content-Security-Policy, and the
// backend — which serves an API and a documentation page that loads its
// renderer from a CDN — does not.
func (s *Service) Use(middleware ...Middleware) {
	s.extra = append(s.extra, middleware...)
}

// Version is the build this service was compiled from. It identifies the
// producer inside artefacts a service generates, such as a snapshot manifest.
func (s *Service) Version() string { return s.opts.Version }

// Health is the liveness and readiness state.
func (s *Service) Health() *health.State { return s.health }

// SetReadinessCheck registers a dependency check consulted on every readiness
// probe. A service with an unreachable dependency reports not-ready rather
// than accepting traffic it cannot serve.
func (s *Service) SetReadinessCheck(check func(context.Context) error) {
	s.readinessCheck = check
}

// OnShutdown registers a function to run once the listeners have stopped.
func (s *Service) OnShutdown(fn func(context.Context) error) {
	s.onShutdown = append(s.onShutdown, fn)
}

// Run starts both listeners and blocks until the context is cancelled or an
// interrupt arrives, then shuts down gracefully.
func (s *Service) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if s.opts.Development {
		log.Warn().Msg("DEVELOPMENT MODE: authentication is disabled — never run this in production")
	}

	app := s.newServer(s.opts.AppPort, Chain(s.appMux,
		append([]Middleware{
			RequestID(),
			Recover(),
			LogRequests(),
			// Inside LogRequests so a traced body carries the same request id,
			// and outside the handler so it sees the body exactly as it
			// arrived. It is inert unless the level is TRACE.
			TraceBodies(),
			s.Measure(),
		}, s.extra...)...,
	))
	maintenance := s.newServer(s.opts.MaintenancePort, Chain(s.maintenanceMux,
		RequestID(),
		Recover(),
	))

	errs := make(chan error, 2)
	go serve(app, "application", s.opts.AppPort, errs)
	go serve(maintenance, "maintenance", s.opts.MaintenancePort, errs)

	s.health.SetReady(true)
	log.Info().
		Str("service", s.opts.Name).
		Str("version", s.opts.Version).
		Str("commit", s.opts.Commit).
		Int("app_port", s.opts.AppPort).
		Int("maintenance_port", s.opts.MaintenancePort).
		Msg("service ready")

	var runErr error
	select {
	case runErr = <-errs:
	case <-ctx.Done():
		log.Info().Msg("shutdown signal received")
	}

	return errors.Join(runErr, s.shutdown(app, maintenance))
}

// shutdown withdraws readiness, waits for that to propagate, then closes the
// listeners and the registered dependencies.
func (s *Service) shutdown(servers ...*http.Server) error {
	s.health.SetReady(false)
	log.Info().Dur("drain_delay", s.opts.DrainDelay).Msg("readiness withdrawn, draining")

	// Give orchestrators and load balancers time to see the readiness change
	// before the listeners stop accepting.
	timer := time.NewTimer(s.opts.DrainDelay)
	defer timer.Stop()
	<-timer.C

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	var errs []error
	for _, srv := range servers {
		if err := srv.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown listener %s: %w", srv.Addr, err))
		}
	}

	// Reverse order: a dependency registered earlier is closed later.
	for i := len(s.onShutdown) - 1; i >= 0; i-- {
		if err := s.onShutdown[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if err := s.metrics.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("shutdown metrics: %w", err))
	}

	s.health.SetLive(false)
	log.Info().Msg("service stopped")
	return errors.Join(errs...)
}

func (s *Service) newServer(port int, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
}

func serve(srv *http.Server, name string, port int, errs chan<- error) {
	log.Info().Str("listener", name).Int("port", port).Msg("listening")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errs <- fmt.Errorf("%s listener on port %d: %w", name, port, err)
	}
}
