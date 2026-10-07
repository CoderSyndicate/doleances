package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/CoderSyndicate/doleances/internal/logging"
	"github.com/rs/zerolog/log"
)

// logFileWindow is how much of the log file the maintenance endpoint serves:
// enough for context beyond the in-memory ring, bounded so a large file cannot
// be dumped in one response.
const logFileWindow = 1 << 20 // 1 MiB

// Maintenance routes. Everything here is operational, never application
// surface, and lives on the maintenance port so it is never exposed publicly.
const (
	RouteLive     = "GET /healthz/live"
	RouteReady    = "GET /healthz/ready"
	RouteMetrics  = "GET /metrics"
	RouteLogs     = "GET /logs"
	RouteLogFile  = "GET /logs-file"
	RouteLogLevel = "/log-level"
)

func (s *Service) registerMaintenanceRoutes() {
	s.maintenanceMux.HandleFunc(RouteLive, s.handleLive)
	s.maintenanceMux.HandleFunc(RouteReady, s.handleReady)
	s.maintenanceMux.Handle(RouteMetrics, s.metrics.Handler())
	s.maintenanceMux.HandleFunc(RouteLogs, s.handleLogStream)
	s.maintenanceMux.HandleFunc(RouteLogFile, s.handleLogFile)
	s.maintenanceMux.HandleFunc(RouteLogLevel, s.handleLogLevel)
}

// MaintenanceMux exposes the maintenance router, for services that need to add
// operational routes of their own.
func (s *Service) MaintenanceMux() *http.ServeMux { return s.maintenanceMux }

func (s *Service) handleLive(w http.ResponseWriter, _ *http.Request) {
	writeHealth(w, s.health.Live(), "alive")
}

func (s *Service) handleReady(w http.ResponseWriter, r *http.Request) {
	ready := s.health.Ready()
	if ready && s.readinessCheck != nil {
		if err := s.readinessCheck(r.Context()); err != nil {
			log.Warn().Err(err).Msg("readiness check failed")
			ready = false
		}
	}
	writeHealth(w, ready, "ready")
}

func writeHealth(w http.ResponseWriter, ok bool, state string) {
	status := http.StatusOK
	if !ok {
		status = http.StatusServiceUnavailable
		state = "not " + state
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"status": state}) //nolint:errcheck
}

// handleLogStream tails the log over Server-Sent Events: the retained lines
// first, then each new one as it is written.
func (s *Service) handleLogStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	// The server's WriteTimeout would otherwise kill this long-lived stream.
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{})
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	if s.opts.LogBuffer == nil {
		http.Error(w, "log streaming not configured", http.StatusNotFound)
		return
	}

	for _, entry := range s.opts.LogBuffer.GetAll() {
		// A reader who has gone is how one of these ends, not a fault.
		_, _ = fmt.Fprintf(w, "data: %s\n\n", entry)
	}
	flusher.Flush()

	id, lines := s.opts.LogBuffer.Subscribe()
	defer s.opts.LogBuffer.Unsubscribe(id)

	for {
		select {
		case <-r.Context().Done():
			return
		case line, open := <-lines:
			if !open {
				return
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		}
	}
}

// handleLogFile serves the tail of the log file, for history beyond the ring.
func (s *Service) handleLogFile(w http.ResponseWriter, _ *http.Request) {
	if s.opts.LogFilePath == "" {
		http.Error(w, "no log file configured", http.StatusNotFound)
		return
	}

	f, err := os.Open(s.opts.LogFilePath)
	if err != nil {
		log.Error().Err(err).Str("path", s.opts.LogFilePath).Msg("cannot open log file")
		http.Error(w, "cannot open log file", http.StatusInternalServerError)
		return
	}
	defer f.Close() //nolint:errcheck

	info, err := f.Stat()
	if err != nil {
		http.Error(w, "cannot stat log file", http.StatusInternalServerError)
		return
	}
	if offset := info.Size() - logFileWindow; offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			http.Error(w, "cannot seek log file", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.Copy(w, f) //nolint:errcheck
}

// handleLogLevel reads and changes the global log level at runtime. It is what
// makes debug and trace usable in production: raise the level for the minutes
// it is needed, then lower it again, without a restart.
func (s *Service) handleLogLevel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		writeLevel(w, logging.Level().String())

	case http.MethodPost:
		var body struct {
			Level string `json:"level"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if _, err := logging.ParseLevel(body.Level); err != nil {
			http.Error(w, "unknown level: "+body.Level, http.StatusBadRequest)
			return
		}
		level := logging.SetLevel(body.Level)
		log.Info().Str("level", level.String()).Msg("log level changed")
		writeLevel(w, level.String())

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeLevel(w http.ResponseWriter, level string) {
	json.NewEncoder(w).Encode(map[string]string{"level": level}) //nolint:errcheck
}
