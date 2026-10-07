// Package logging configures the global zerolog logger.
//
// Every log event is written as JSON to several destinations at once: a
// human-readable console, an in-memory ring buffer the maintenance API streams,
// and — when configured — a file on disk.
package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/CoderSyndicate/doleances/internal/logbuffer"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// DefaultBufferSize is how many recent log lines stay available for streaming.
const DefaultBufferSize = 500

// Options configures the logging pipeline.
type Options struct {
	// Level is a zerolog level name. An empty or unparseable value falls back
	// to info rather than failing startup.
	Level string

	// FilePath, when set, appends raw JSON events to that file. A file that
	// cannot be opened is reported but never fatal: losing the log file must
	// not take the service down with it.
	FilePath string

	// PrettyPrint expands JSON-valued fields across indented lines on the
	// console. A development aid; too verbose for production.
	PrettyPrint bool

	// Buffer receives every event for live streaming. Optional.
	Buffer *logbuffer.Buffer
}

// Init configures the global logger and reports the log file actually in use,
// which is empty when no file was configured or the configured one could not
// be opened.
func Init(opts Options) (string, error) {
	writers := []io.Writer{consoleWriter(opts.PrettyPrint)}
	if opts.Buffer != nil {
		writers = append(writers, opts.Buffer)
	}

	zerolog.CallerMarshalFunc = func(_ uintptr, file string, line int) string {
		return filepath.Base(file) + ":" + strconv.Itoa(line)
	}
	SetLevel(opts.Level)

	var (
		path    string
		fileErr error
	)
	if opts.FilePath != "" {
		f, err := os.OpenFile(opts.FilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fileErr = fmt.Errorf("open log file %q: %w", opts.FilePath, err)
		} else {
			writers = append(writers, f)
			path = opts.FilePath
		}
	}

	// MultiLevelWriter rather than io.MultiWriter: it preserves the event's
	// level for writers that implement zerolog.LevelWriter.
	log.Logger = zerolog.New(zerolog.MultiLevelWriter(writers...)).
		With().Timestamp().Caller().Logger()

	return path, fileErr
}

// SetLevel applies a zerolog level by name, falling back to info when the name
// is empty or unknown. It reports the level actually in force.
func SetLevel(name string) zerolog.Level {
	level := zerolog.InfoLevel
	if name != "" {
		if parsed, err := zerolog.ParseLevel(strings.ToLower(name)); err == nil {
			level = parsed
		}
	}
	zerolog.SetGlobalLevel(level)
	return level
}

// ParseLevel validates a level name without applying it.
func ParseLevel(name string) (zerolog.Level, error) {
	return zerolog.ParseLevel(strings.ToLower(name))
}

// Level reports the global level currently in force.
func Level() zerolog.Level {
	return zerolog.GlobalLevel()
}

func consoleWriter(pretty bool) zerolog.ConsoleWriter {
	w := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.RFC3339,
		// Colour helps a terminal and corrupts a log collector, so it follows
		// whether stdout is actually a terminal.
		NoColor: !isTerminal(os.Stdout),
	}
	w.FormatLevel = func(i any) string {
		return strings.ToUpper(fmt.Sprintf("| %-6s|", i))
	}
	if pretty {
		w.FormatFieldValue = prettyFieldValue
	}
	return w
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// prettyFieldValue re-indents values that are themselves structured, so a
// nested payload is readable instead of being one long escaped line.
func prettyFieldValue(i any) string {
	switch v := i.(type) {
	case string:
		var raw json.RawMessage
		if err := json.Unmarshal([]byte(v), &raw); err == nil {
			if b, err := json.MarshalIndent(raw, "  ", "  "); err == nil {
				return "\n  " + string(b)
			}
		}
		return v
	case map[string]any, []any:
		if b, err := json.MarshalIndent(v, "  ", "  "); err == nil {
			return "\n  " + string(b)
		}
		return fmt.Sprint(v)
	default:
		return fmt.Sprint(v)
	}
}
