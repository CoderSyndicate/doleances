package service

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// maxTraceBody bounds what a single log line will carry. A doléance can be
// somebody's whole life, and a log that repeats it in full is both unreadable
// and a second copy of content the register is careful about.
const maxTraceBody = 8 << 10 // 8 KiB

// TraceBodies logs request and response bodies as JSON.
//
// It is TRACE, per the level semantics: this is a detailed data dump, not a
// state change, and it must never be on in production. Turn it on for the
// minutes you need it — the maintenance port's log-level endpoint changes the
// level at runtime, no restart.
//
// It costs nothing when the level is above TRACE: the check happens before
// anything is buffered, so an ordinary request is not copied through memory
// to be discarded.
func TraceBodies() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if zerolog.GlobalLevel() > zerolog.TraceLevel {
				next.ServeHTTP(w, r)
				return
			}

			entry := log.Trace().
				Str("request_id", RequestIDFrom(r.Context())).
				Str("method", r.Method).
				Str("path", r.URL.Path)
			if query := r.URL.RawQuery; query != "" {
				entry = entry.Str("query", query)
			}

			// The request body is read out and put back, because the handler
			// still has to see it.
			if r.Body != nil {
				body, err := io.ReadAll(io.LimitReader(r.Body, maxTraceBody+1))
				if err == nil {
					r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
					if len(body) > 0 {
						entry = entry.Str("request_body", renderBody(body))
					}
				}
			}
			entry.Msg("request received")

			recorder := &tracingWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(recorder, r)

			log.Trace().
				Str("request_id", RequestIDFrom(r.Context())).
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Int("status", recorder.status).
				Str("response_body", renderBody(recorder.body.Bytes())).
				Msg("response sent")
		})
	}
}

// tracingWriter keeps a copy of the response while still writing it through.
type tracingWriter struct {
	http.ResponseWriter

	status int
	body   bytes.Buffer
}

func (t *tracingWriter) WriteHeader(status int) {
	t.status = status
	t.ResponseWriter.WriteHeader(status)
}

func (t *tracingWriter) Write(b []byte) (int, error) {
	if t.body.Len() < maxTraceBody {
		t.body.Write(b[:min(len(b), maxTraceBody-t.body.Len())])
	}
	return t.ResponseWriter.Write(b)
}

// Flush keeps the log stream working: the SSE endpoint needs a real Flusher,
// and a wrapper that swallowed it would turn a live tail into silence.
func (t *tracingWriter) Flush() {
	if flusher, ok := t.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// secretFields are redacted wherever they appear in a body.
//
// Trace logging exists to show what crossed the wire, and one of the things
// that crosses the wire is the LLM API key on its way to being stored. A
// developer turning on TRACE to debug a map query must not thereby write a
// credential into a log file and a run transcript.
var secretFields = regexp.MustCompile(
	`(?i)"(api_key|password|token|token_hash|secret)"\s*:\s*"[^"]*"`)

// renderBody prepares a body for one log field: redacted, compacted when it is
// JSON, and truncated with a note rather than silently cut.
func renderBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}

	truncated := len(body) > maxTraceBody
	if truncated {
		body = body[:maxTraceBody]
	}

	rendered := string(body)
	if compact := compactJSON(body); compact != "" {
		rendered = compact
	}
	rendered = secretFields.ReplaceAllString(rendered, `"$1":"[redacted]"`)

	if truncated {
		rendered += " …[truncated]"
	}
	return rendered
}

// compactJSON collapses a JSON body onto one line, and returns "" for anything
// that is not JSON — a stylesheet or a zip has no business in a log field.
func compactJSON(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return ""
	}

	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(trimmed)); err != nil {
		// Truncation can cut valid JSON in half; the raw text is still worth
		// reading, so fall back to it rather than dropping the field.
		return trimmed
	}
	return buf.String()
}
