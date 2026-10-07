package snapshot

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// MaxPackageBytes bounds a package being read back. An import is an untrusted
// upload like any other, and a zip is trivially a decompression bomb.
const MaxPackageBytes = 512 << 20 // 512 MiB

// ErrNotAPackage is returned when the bytes are not a snapshot at all.
var ErrNotAPackage = errors.New("snapshot: not a package")

// Package is a snapshot read back from its zip.
type Package struct {
	Manifest Manifest

	Subjects   []models.Subject
	Aliases    []models.SubjectAlias
	Messages   []models.Message
	Historical []models.HistoricalText

	// The rest is present only in a full package.
	Groups       []models.Group
	Actions      []models.Action
	AuditEntries []models.AuditEntry
	Settings     map[string]json.RawMessage
}

// Read parses a package. It is the writer in reverse: an export nobody can
// import is a backup with extra steps.
func Read(data []byte) (*Package, error) {
	if len(data) > MaxPackageBytes {
		return nil, fmt.Errorf("snapshot: package is larger than %d bytes", MaxPackageBytes)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotAPackage, err)
	}

	pkg := &Package{Settings: map[string]json.RawMessage{}}
	seenManifest := false

	for _, entry := range zr.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		// A zip entry names its own path, so an import is a path-traversal
		// vector unless every name is checked before it is used.
		name := path.Clean(entry.Name)
		if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "..") {
			return nil, fmt.Errorf("snapshot: refusing entry outside the package: %q", entry.Name)
		}

		content, err := readEntry(entry)
		if err != nil {
			return nil, err
		}

		switch {
		case name == FileManifest:
			if err := json.Unmarshal(content, &pkg.Manifest); err != nil {
				return nil, fmt.Errorf("snapshot: decode manifest: %w", err)
			}
			seenManifest = true
		case name == FileSubjects:
			if err := json.Unmarshal(content, &pkg.Subjects); err != nil {
				return nil, fmt.Errorf("snapshot: decode subjects: %w", err)
			}
		case name == FileAliases:
			if err := json.Unmarshal(content, &pkg.Aliases); err != nil {
				return nil, fmt.Errorf("snapshot: decode subject aliases: %w", err)
			}
		case name == FileReadme:
			// Documentation for whoever opens the zip; nothing to parse.
		case strings.HasPrefix(name, DirMessages+"/"):
			message, err := parseMessageDocument(content)
			if err != nil {
				return nil, fmt.Errorf("snapshot: %s: %w", name, err)
			}
			pkg.Messages = append(pkg.Messages, message)
		case strings.HasPrefix(name, DirHistorical+"/"):
			var text models.HistoricalText
			if err := json.Unmarshal(content, &text); err != nil {
				return nil, fmt.Errorf("snapshot: %s: %w", name, err)
			}
			pkg.Historical = append(pkg.Historical, text)
		case strings.HasPrefix(name, DirGroups+"/") && strings.Contains(name, "/actions/"):
			var action models.Action
			if err := json.Unmarshal(content, &action); err != nil {
				return nil, fmt.Errorf("snapshot: %s: %w", name, err)
			}
			pkg.Actions = append(pkg.Actions, action)
		case strings.HasPrefix(name, DirGroups+"/"):
			var group models.Group
			if err := json.Unmarshal(content, &group); err != nil {
				return nil, fmt.Errorf("snapshot: %s: %w", name, err)
			}
			pkg.Groups = append(pkg.Groups, group)
		case strings.HasPrefix(name, DirAudit+"/"):
			entries, err := parseAuditLines(content)
			if err != nil {
				return nil, fmt.Errorf("snapshot: %s: %w", name, err)
			}
			pkg.AuditEntries = append(pkg.AuditEntries, entries...)
		case strings.HasPrefix(name, DirSettings+"/"):
			key := strings.TrimSuffix(path.Base(name), ".json")
			pkg.Settings[key] = json.RawMessage(content)
		}
	}

	if !seenManifest {
		return nil, fmt.Errorf("%w: no %s", ErrNotAPackage, FileManifest)
	}
	if !pkg.Manifest.Kind.Valid() {
		return nil, fmt.Errorf("snapshot: unknown kind %q in manifest", pkg.Manifest.Kind)
	}
	if pkg.Manifest.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("snapshot: package schema version %d is newer than this build understands (%d)",
			pkg.Manifest.SchemaVersion, SchemaVersion)
	}
	return pkg, nil
}

func readEntry(entry *zip.File) ([]byte, error) {
	rc, err := entry.Open()
	if err != nil {
		return nil, fmt.Errorf("snapshot: open %s: %w", entry.Name, err)
	}
	defer rc.Close() //nolint:errcheck

	// Read one byte past the limit so an oversized entry is refused rather
	// than silently truncated into a record that decodes wrong.
	content, err := io.ReadAll(io.LimitReader(rc, MaxPackageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("snapshot: read %s: %w", entry.Name, err)
	}
	if len(content) > MaxPackageBytes {
		return nil, fmt.Errorf("snapshot: entry %s is too large", entry.Name)
	}
	return content, nil
}

func parseAuditLines(content []byte) ([]models.AuditEntry, error) {
	var entries []models.AuditEntry
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry models.AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("decode audit line: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// parseMessageDocument reads back the Markdown form written by
// messageDocument. It is a small hand-rolled parser rather than a YAML
// dependency because the front matter this writes is a known, closed shape.
func parseMessageDocument(content []byte) (models.Message, error) {
	var message models.Message

	text := string(content)
	if !strings.HasPrefix(text, "---\n") {
		return message, errors.New("no front matter")
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return message, errors.New("unterminated front matter")
	}
	front := text[4 : 4+end+1]
	message.Text = strings.TrimPrefix(text[4+end+len("\n---\n"):], "\n")

	var location models.Location
	var haveLocation bool
	var listKey string

	for _, line := range strings.Split(front, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Indented lines continue the previous key: a list item, or a field of
		// the location block.
		if strings.HasPrefix(line, " ") {
			trimmed := strings.TrimSpace(line)
			if item, ok := strings.CutPrefix(trimmed, "- "); ok && listKey == "subjects" {
				message.Subjects = append(message.Subjects,
					models.Subject{Slug: unquoteYAML(item)})
				continue
			}
			key, value, ok := strings.Cut(trimmed, ": ")
			if !ok {
				continue
			}
			value = unquoteYAML(value)
			switch key {
			case "latitude":
				location.Latitude, _ = strconv.ParseFloat(value, 64)
			case "longitude":
				location.Longitude, _ = strconv.ParseFloat(value, 64)
			case "label":
				location.Label = value
			case "country":
				location.CountryCode = value
			}
			continue
		}

		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = unquoteYAML(strings.TrimSpace(value))

		if value == "" {
			listKey = key
			if key == "location" {
				haveLocation = true
			}
			continue
		}
		listKey = ""

		switch key {
		case "id":
			message.ID = value
		case "date":
			message.CreatedAt, _ = time.Parse(time.RFC3339, value)
		case "published":
			if at, err := time.Parse(time.RFC3339, value); err == nil {
				message.PublishedAt = &at
			}
		case "nickname":
			message.Nickname = value
		case "birth_year":
			message.BirthYear, _ = strconv.Atoi(value)
		case "activity":
			message.Activity = value
		case "language":
			message.Language = value
		}
	}

	if haveLocation {
		message.Location = &location
	}
	if message.ID == "" {
		return message, errors.New("no id in front matter")
	}
	return message, nil
}

// unquoteYAML reverses yamlValue.
func unquoteYAML(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) {
		if unquoted, err := strconv.Unquote(s); err == nil {
			return unquoted
		}
	}
	return s
}
