package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"runtime"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Middleware wraps a handler. Cross-cutting concerns live here, never inside
// the handlers themselves.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware so that the first listed is the outermost — the
// order they are written is the order a request passes through them.
func Chain(h http.Handler, middleware ...Middleware) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		h = middleware[i](h)
	}
	return h
}

type contextKey int

const requestIDKey contextKey = iota

// RequestIDFrom returns the request id carried by a context, if any.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// RequestID attaches an identifier to every request so its log lines can be
// correlated, reusing an inbound X-Request-Id when the caller supplied one.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-Id")
			if id == "" {
				id = newRequestID()
			}
			w.Header().Set("X-Request-Id", id)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
		})
	}
}

// Recover turns a panic in a handler into a 500 instead of taking the process
// down. A panic on one request must never lose the other in-flight ones.
func Recover() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				// A client that gave up mid-write is not a server error.
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				log.Error().
					Str("request_id", RequestIDFrom(r.Context())).
					Str("method", r.Method).
					Str("path", r.URL.Path).
					Interface("panic", rec).
					Bytes("stack", stack()).
					Msg("handler panicked")
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// LogRequests records one line per request. Requests are high-frequency, so
// successful ones log at debug; only server errors are loud enough for the
// production default.
func LogRequests() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			level := zerolog.DebugLevel
			switch {
			case rec.status >= http.StatusInternalServerError:
				level = zerolog.ErrorLevel
			case rec.status >= http.StatusBadRequest:
				level = zerolog.WarnLevel
			}

			log.WithLevel(level).
				Str("request_id", RequestIDFrom(r.Context())).
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Int("status", rec.status).
				Int64("bytes", rec.written).
				Dur("duration", time.Since(started)).
				Msg("request handled")
		})
	}
}

// Measure records request metrics. Routes are reported by pattern rather than
// by path, so a per-message URL cannot explode the metric cardinality.
func (s *Service) Measure() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			done := s.metrics.RequestStarted(r.Context(), r.Method, s.routePattern(r))

			next.ServeHTTP(rec, r)
			done(rec.status)
		})
	}
}

// routePattern reports the route a request matches rather than its concrete
// path.
//
// The pattern has to be resolved by asking the mux: Request.Pattern is filled
// in by ServeMux on a copy of the request, which an enclosing middleware never
// sees.
func (s *Service) routePattern(r *http.Request) string {
	if _, pattern := s.appMux.Handler(r); pattern != "" {
		return pattern
	}
	return "unmatched"
}

// statusRecorder remembers what a handler wrote, for logging and metrics.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int64
	wrote   bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wrote {
		return
	}
	r.wrote = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	n, err := r.ResponseWriter.Write(b)
	r.written += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer, which the
// log stream needs in order to clear its write deadline.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unidentified"
	}
	return hex.EncodeToString(b[:])
}

func stack() []byte {
	buf := make([]byte, 8<<10)
	return buf[:runtime.Stack(buf, false)]
}
