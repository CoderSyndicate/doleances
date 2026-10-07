package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CoderSyndicate/doleances/internal/logbuffer"
	"github.com/CoderSyndicate/doleances/internal/logging"
	"github.com/rs/zerolog/log"
)

func newTestService(t *testing.T, mutate ...func(*Options)) *Service {
	t.Helper()

	opts := Options{
		Name:            "test-service",
		Version:         "1.2.3",
		AppPort:         freePort(t),
		MaintenancePort: freePort(t),
		DrainDelay:      time.Millisecond,
	}
	for _, m := range mutate {
		m(&opts)
	}

	svc, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		svc.metrics.Shutdown(ctx) //nolint:errcheck
	})
	return svc
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer l.Close() //nolint:errcheck
	return l.Addr().(*net.TCPAddr).Port
}

func scrapeMetrics(t *testing.T, svc *Service) string {
	t.Helper()
	rec := httptest.NewRecorder()
	svc.maintenanceMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

func TestNewRequiresAName(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Error("expected New to reject a service without a name")
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	svc := newTestService(t, func(o *Options) {
		o.Version = ""
		o.Title = ""
		o.DrainDelay = 0
	})

	if svc.opts.Version != "dev" {
		t.Errorf("Version = %q, want dev", svc.opts.Version)
	}
	if svc.opts.Title != "test-service" {
		t.Errorf("Title = %q, want the service name", svc.opts.Title)
	}
	if svc.opts.DrainDelay != defaultDrainDelay {
		t.Errorf("DrainDelay = %v, want %v", svc.opts.DrainDelay, defaultDrainDelay)
	}
}

func TestNotReadyBeforeRun(t *testing.T) {
	svc := newTestService(t)
	if svc.Health().Ready() {
		t.Error("a service is ready only once Run has started it")
	}
	if !svc.Health().Live() {
		t.Error("a constructed service should be live")
	}
}

func TestOpenAPIAndDocsAreServedOnTheAppPort(t *testing.T) {
	svc := newTestService(t)

	for _, path := range []string{"/openapi.json", "/openapi.yaml", "/docs"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			svc.appMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
		})
	}
}

func TestOpenAPIPathAloneIsNotARoute(t *testing.T) {
	// OpenAPIPath is a prefix; huma derives the extensions from it.
	svc := newTestService(t)
	rec := httptest.NewRecorder()
	svc.appMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi", nil))

	if rec.Code == http.StatusOK {
		t.Error("/openapi resolved; the documented routes are /openapi.json and /openapi.yaml")
	}
}

func TestMaintenanceRoutesAreNotOnTheAppPort(t *testing.T) {
	// The whole point of the split: nothing operational is publicly exposed.
	svc := newTestService(t)

	for _, path := range []string{"/healthz/live", "/healthz/ready", "/metrics", "/logs", "/log-level"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			svc.appMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404 on the application port", rec.Code)
			}
		})
	}
}

func TestLivenessAndReadinessDiffer(t *testing.T) {
	svc := newTestService(t)

	// Live from construction, but not yet ready.
	assertHealth(t, svc, "/healthz/live", http.StatusOK, "alive")
	assertHealth(t, svc, "/healthz/ready", http.StatusServiceUnavailable, "not ready")

	svc.Health().SetReady(true)
	assertHealth(t, svc, "/healthz/ready", http.StatusOK, "ready")
}

func assertHealth(t *testing.T, svc *Service, path string, wantStatus int, wantState string) {
	t.Helper()
	rec := httptest.NewRecorder()
	svc.maintenanceMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

	if rec.Code != wantStatus {
		t.Errorf("%s status = %d, want %d", path, rec.Code, wantStatus)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s body is not JSON: %v", path, err)
	}
	if body["status"] != wantState {
		t.Errorf("%s status = %q, want %q", path, body["status"], wantState)
	}
}

func TestLogLevelRoundTrip(t *testing.T) {
	svc := newTestService(t)
	logging.SetLevel("info")

	rec := httptest.NewRecorder()
	svc.maintenanceMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/log-level", nil))
	if got := rec.Body.String(); !strings.Contains(got, `"level":"info"`) {
		t.Errorf("GET returned %q, want info", got)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/log-level", strings.NewReader(`{"level":"trace"}`))
	svc.maintenanceMux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200", rec.Code)
	}
	if logging.Level().String() != "trace" {
		t.Errorf("level = %s, want trace — the change must take effect immediately", logging.Level())
	}
	t.Cleanup(func() { logging.SetLevel("info") })
}

func TestLogLevelRejectsBadInput(t *testing.T) {
	svc := newTestService(t)

	tests := []struct {
		name   string
		method string
		body   string
		want   int
	}{
		{"unknown level", http.MethodPost, `{"level":"chatty"}`, http.StatusBadRequest},
		{"malformed json", http.MethodPost, `{`, http.StatusBadRequest},
		{"wrong method", http.MethodDelete, "", http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, "/log-level", strings.NewReader(tt.body))
			svc.maintenanceMux.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestLogStreamReplaysThenTails(t *testing.T) {
	buf := logbuffer.New(10)
	if _, err := logging.Init(logging.Options{Level: "info", Buffer: buf}); err != nil {
		t.Fatalf("init logging: %v", err)
	}
	log.Info().Msg("history")

	svc := newTestService(t, func(o *Options) { o.LogBuffer = buf })

	srv := httptest.NewServer(svc.maintenanceMux)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/logs", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect to stream: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	chunk := make([]byte, 4096)
	n, err := resp.Body.Read(chunk)
	if err != nil {
		t.Fatalf("read replayed history: %v", err)
	}
	if !strings.Contains(string(chunk[:n]), "history") {
		t.Errorf("stream did not replay the retained line: %s", chunk[:n])
	}

	// A line written after connecting must arrive on the same stream.
	go func() {
		time.Sleep(50 * time.Millisecond)
		log.Info().Msg("live-line")
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		n, err := resp.Body.Read(chunk)
		if err != nil {
			t.Fatalf("read live line: %v", err)
		}
		if strings.Contains(string(chunk[:n]), "live-line") {
			return
		}
	}
	t.Error("the live line never arrived on the stream")
}

func TestLogStreamWithoutBuffer(t *testing.T) {
	svc := newTestService(t)
	rec := httptest.NewRecorder()
	svc.maintenanceMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/logs", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when no buffer is wired", rec.Code)
	}
}

func TestLogFileServesTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	// Larger than the served window, so the response must be the tail only.
	big := strings.Repeat("x", logFileWindow)
	if err := os.WriteFile(path, []byte(big+"THE-TAIL"), 0o600); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	svc := newTestService(t, func(o *Options) { o.LogFilePath = path })

	rec := httptest.NewRecorder()
	svc.maintenanceMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/logs-file", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.HasSuffix(body, "THE-TAIL") {
		t.Error("response does not end with the tail of the file")
	}
	if rec.Body.Len() > logFileWindow {
		t.Errorf("served %d bytes, window is %d", rec.Body.Len(), logFileWindow)
	}
}

func TestLogFileWithoutPath(t *testing.T) {
	svc := newTestService(t)
	rec := httptest.NewRecorder()
	svc.maintenanceMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/logs-file", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when no log file is configured", rec.Code)
	}
}

func TestRunServesThenShutsDownGracefully(t *testing.T) {
	buf := logbuffer.New(50)
	if _, err := logging.Init(logging.Options{Level: "debug", Buffer: buf}); err != nil {
		t.Fatalf("init logging: %v", err)
	}

	svc := newTestService(t)
	svc.Mux().HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("pong")) //nolint:errcheck
	})

	var closed bool
	svc.OnShutdown(func(context.Context) error {
		closed = true
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	waitForReady(t, svc)

	body := get(t, fmt.Sprintf("http://127.0.0.1:%d/ping", svc.opts.AppPort))
	if body != "pong" {
		t.Errorf("app response = %q, want pong", body)
	}
	if got := get(t, fmt.Sprintf("http://127.0.0.1:%d/healthz/ready", svc.opts.MaintenancePort)); !strings.Contains(got, `"ready"`) {
		t.Errorf("readiness = %q, want ready", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	if !closed {
		t.Error("the registered shutdown hook never ran")
	}
	if svc.Health().Ready() {
		t.Error("service still reports ready after shutdown")
	}
	if svc.Health().Live() {
		t.Error("service still reports live after shutdown")
	}

	// Readiness has to be withdrawn before the listeners close, or traffic is
	// dropped on the floor mid-request.
	var withdrawnAt, stoppedAt int
	for i, entry := range buf.GetAll() {
		line := string(entry)
		if strings.Contains(line, "readiness withdrawn") {
			withdrawnAt = i
		}
		if strings.Contains(line, "service stopped") {
			stoppedAt = i
		}
	}
	if withdrawnAt == 0 || stoppedAt == 0 || withdrawnAt > stoppedAt {
		t.Error("shutdown did not withdraw readiness before stopping")
	}
}

func TestRunReportsPortInUse(t *testing.T) {
	// Bind the wildcard address the service itself uses: with SO_REUSEADDR,
	// a loopback-only listener would not collide with it on BSD systems.
	busy, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer busy.Close() //nolint:errcheck

	svc := newTestService(t, func(o *Options) {
		o.AppPort = busy.Addr().(*net.TCPAddr).Port
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := svc.Run(ctx); err == nil {
		t.Error("expected Run to report the port conflict")
	}
}

func waitForReady(t *testing.T, svc *Service) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if svc.Health().Ready() {
			resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz/ready", svc.opts.MaintenancePort))
			if err == nil {
				resp.Body.Close() //nolint:errcheck
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("service never became ready")
}

func get(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return string(body)
}
