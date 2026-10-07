package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/logbuffer"
	"github.com/CoderSyndicate/doleances/internal/logging"
)

func TestChainOrdersOutermostFirst(t *testing.T) {
	var order []string
	mark := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}

	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		order = append(order, "handler")
	}), mark("first"), mark("second"))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if got := strings.Join(order, ","); got != "first,second,handler" {
		t.Errorf("order = %q, want first,second,handler", got)
	}
}

func TestRequestIDGeneratesAndEchoes(t *testing.T) {
	var seen string
	h := Chain(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = RequestIDFrom(r.Context())
	}), RequestID())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if seen == "" {
		t.Fatal("no request id in the handler context")
	}
	if got := rec.Header().Get("X-Request-Id"); got != seen {
		t.Errorf("header id = %q, context id = %q; they must match", got, seen)
	}
}

func TestRequestIDReusesInboundHeader(t *testing.T) {
	var seen string
	h := Chain(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = RequestIDFrom(r.Context())
	}), RequestID())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "caller-supplied")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen != "caller-supplied" {
		t.Errorf("request id = %q, want the caller's own id", seen)
	}
}

func TestRequestIDFromEmptyContext(t *testing.T) {
	if got := RequestIDFrom(httptest.NewRequest(http.MethodGet, "/", nil).Context()); got != "" {
		t.Errorf("RequestIDFrom = %q, want empty", got)
	}
}

func TestRecoverTurnsPanicIntoFiveHundred(t *testing.T) {
	buf := logbuffer.New(10)
	if _, err := logging.Init(logging.Options{Level: "debug", Buffer: buf}); err != nil {
		t.Fatalf("init logging: %v", err)
	}

	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("something went wrong")
	}), RequestID(), Recover())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}

	var logged bool
	for _, entry := range buf.GetAll() {
		if strings.Contains(string(entry), "handler panicked") {
			logged = true
			if !strings.Contains(string(entry), "stack") {
				t.Error("panic was logged without a stack trace")
			}
		}
	}
	if !logged {
		t.Error("the panic was swallowed without a log line")
	}
}

func TestRecoverRepanicsOnAbortHandler(t *testing.T) {
	// ErrAbortHandler means a client gave up; it is not a server error and
	// net/http expects it to travel on.
	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler {
			t.Errorf("recovered %v, want ErrAbortHandler to propagate", rec)
		}
	}()

	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}), Recover())
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestLogRequestsLevelsByStatus(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		wantLevel string
	}{
		// Requests are high-frequency, so a successful one is debug.
		{"success is debug", http.StatusOK, `"level":"debug"`},
		{"client error is warn", http.StatusBadRequest, `"level":"warn"`},
		{"server error is error", http.StatusInternalServerError, `"level":"error"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := logbuffer.New(10)
			if _, err := logging.Init(logging.Options{Level: "trace", Buffer: buf}); err != nil {
				t.Fatalf("init logging: %v", err)
			}

			h := Chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}), LogRequests())
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

			entries := buf.GetAll()
			if len(entries) != 1 {
				t.Fatalf("logged %d lines, want 1", len(entries))
			}
			if !strings.Contains(string(entries[0]), tt.wantLevel) {
				t.Errorf("line = %s, want level %s", entries[0], tt.wantLevel)
			}
		})
	}
}

func TestStatusRecorderDefaultsToOK(t *testing.T) {
	// A handler that only writes a body never calls WriteHeader.
	buf := logbuffer.New(10)
	if _, err := logging.Init(logging.Options{Level: "trace", Buffer: buf}); err != nil {
		t.Fatalf("init logging: %v", err)
	}

	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("body")) //nolint:errcheck
	}), LogRequests())
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	line := string(buf.GetAll()[0])
	if !strings.Contains(line, `"status":200`) {
		t.Errorf("line = %s, want status 200", line)
	}
	if !strings.Contains(line, `"bytes":4`) {
		t.Errorf("line = %s, want 4 bytes written", line)
	}
}

func TestStatusRecorderKeepsFirstStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	r := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}

	r.WriteHeader(http.StatusTeapot)
	r.WriteHeader(http.StatusInternalServerError)

	if r.status != http.StatusTeapot {
		t.Errorf("status = %d, want the first one written (418)", r.status)
	}
}

func TestRoutePatternFallsBackToUnmatched(t *testing.T) {
	svc := newTestService(t)
	req := httptest.NewRequest(http.MethodGet, "/nothing-registered-here", nil)

	if got := svc.routePattern(req); got != "unmatched" {
		t.Errorf("routePattern = %q, want unmatched", got)
	}
}

func TestMeasureUsesRoutePattern(t *testing.T) {
	// Metrics must never be labelled with a concrete path, or a per-message
	// URL would create unbounded cardinality.
	svc := newTestService(t)
	svc.Mux().HandleFunc("GET /messages/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	h := Chain(svc.Mux(), svc.Measure())
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/messages/abc123", nil))

	body := scrapeMetrics(t, svc)
	if !strings.Contains(body, `/messages/{id}`) {
		t.Errorf("metrics do not carry the route pattern:\n%s", body)
	}
	if strings.Contains(body, "abc123") {
		t.Error("metrics carry the concrete path — that is unbounded cardinality")
	}
}
