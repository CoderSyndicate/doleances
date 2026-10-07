package logging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/logbuffer"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func TestInitFansOutToBuffer(t *testing.T) {
	buf := logbuffer.New(10)
	if _, err := Init(Options{Level: "debug", Buffer: buf}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	log.Info().Str("field", "value").Msg("hello")

	entries := buf.GetAll()
	if len(entries) != 1 {
		t.Fatalf("buffer holds %d entries, want 1", len(entries))
	}

	var event map[string]any
	if err := json.Unmarshal(entries[0], &event); err != nil {
		t.Fatalf("buffered line is not JSON: %v (%s)", err, entries[0])
	}
	if event["message"] != "hello" {
		t.Errorf("message = %v, want hello", event["message"])
	}
	if event["field"] != "value" {
		t.Errorf("field = %v, want value", event["field"])
	}
	if _, ok := event["time"]; !ok {
		t.Error("event carries no timestamp")
	}
	if caller, ok := event["caller"].(string); !ok || !strings.Contains(caller, "logging_test.go:") {
		t.Errorf("caller = %v, want a shortened logging_test.go reference", event["caller"])
	}
}

func TestInitWritesToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	got, err := Init(Options{Level: "info", FilePath: path})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got != path {
		t.Fatalf("resolved path = %q, want %q", got, path)
	}

	log.Info().Msg("to disk")

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(content), "to disk") {
		t.Errorf("log file does not contain the event: %s", content)
	}
}

func TestInitSurvivesUnusableLogFile(t *testing.T) {
	// A log file we cannot open is reported, never fatal — the service must
	// still start and still log everywhere else.
	buf := logbuffer.New(10)
	path := filepath.Join(t.TempDir(), "no-such-dir", "service.log")

	resolved, err := Init(Options{Level: "info", FilePath: path, Buffer: buf})
	if err == nil {
		t.Error("expected an error describing the unusable log file")
	}
	if resolved != "" {
		t.Errorf("resolved path = %q, want empty", resolved)
	}

	log.Info().Msg("still logging")
	if len(buf.GetAll()) != 1 {
		t.Error("logging stopped working after the log file failed to open")
	}
}

func TestSetLevel(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want zerolog.Level
	}{
		{"explicit", "warn", zerolog.WarnLevel},
		{"mixed case", "TRACE", zerolog.TraceLevel},
		{"empty falls back", "", zerolog.InfoLevel},
		{"unknown falls back", "chatty", zerolog.InfoLevel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SetLevel(tt.in); got != tt.want {
				t.Errorf("SetLevel(%q) = %v, want %v", tt.in, got, tt.want)
			}
			if Level() != tt.want {
				t.Errorf("Level() = %v, want %v", Level(), tt.want)
			}
		})
	}
}

func TestLevelFiltersEvents(t *testing.T) {
	buf := logbuffer.New(10)
	if _, err := Init(Options{Level: "warn", Buffer: buf}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	log.Debug().Msg("noise")
	log.Warn().Msg("signal")

	entries := buf.GetAll()
	if len(entries) != 1 {
		t.Fatalf("buffer holds %d entries, want 1", len(entries))
	}
	if !strings.Contains(string(entries[0]), "signal") {
		t.Errorf("retained the wrong event: %s", entries[0])
	}
}

func TestParseLevelRejectsUnknown(t *testing.T) {
	if _, err := ParseLevel("nonsense"); err == nil {
		t.Error("expected an error for an unknown level name")
	}
	if lvl, err := ParseLevel("Error"); err != nil || lvl != zerolog.ErrorLevel {
		t.Errorf("ParseLevel(Error) = %v, %v; want error level", lvl, err)
	}
}

func TestPrettyFieldValue(t *testing.T) {
	tests := []struct {
		name     string
		in       any
		contains string
	}{
		{"json string is expanded", `{"a":1}`, "\n  {\n    \"a\": 1"},
		{"plain string is untouched", "hello", "hello"},
		{"map is expanded", map[string]any{"a": 1}, "\n  {\n    \"a\": 1"},
		{"other types are printed", 42, "42"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prettyFieldValue(tt.in); !strings.Contains(got, tt.contains) {
				t.Errorf("prettyFieldValue(%v) = %q, want it to contain %q", tt.in, got, tt.contains)
			}
		})
	}
}
