package snapshot

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// Source supplies the records a package is built from.
//
// It is an interface rather than a *store.Store so that the builder can be
// tested against a fixture holding every value that must never be exported —
// which is the only way to prove the exclusions hold.
type Source interface {
	// PublishedMessages yields every message in the public register, oldest
	// first. Anything unpublished — pending, dropped, rejected — is not the
	// register and must not be yielded.
	PublishedMessages(context.Context) ([]models.Message, error)
	Subjects(context.Context) ([]models.Subject, error)

	// SubjectAliases yields the other spellings each subject is known by.
	// They are not personal data and they are not decoration: they carry every
	// curator's merge decision and every name learned from Wikidata, so a
	// package without them restores a register that has forgotten all of it
	// and starts asking the same questions again.
	SubjectAliases(context.Context) ([]models.SubjectAlias, error)
	HistoricalTexts(context.Context) ([]models.HistoricalText, error)

	// The rest is the movement rather than the register, and is read only for
	// KindFull.
	//
	// **There is deliberately no source of accounts.** A snapshot is a copy of
	// the register, and an account is a credential store: a handle, a set of
	// public keys and a sheet of recovery hashes. Exporting those would let
	// whoever holds the package authenticate this register's members on a
	// rehosted copy, which is the same objection that keeps deletion-token
	// hashes out. What memberships and intents carry instead is nothing —
	// their account reference is stripped on the way out, so a restored
	// package shows how many people came and never who.
	Groups(context.Context) ([]models.Group, error)
	Actions(context.Context) ([]models.Action, error)
	AuditEntries(context.Context) ([]models.AuditEntry, error)
	Settings(context.Context) (map[string]any, error)
}

// Options control one build.
type Options struct {
	Kind Kind

	// Now is the moment recorded in the manifest. Injected so a test can
	// assert on a stable filename.
	Now time.Time

	// Generator identifies the build that produced the package, so a reader
	// in five years knows what wrote it.
	Generator string
}

// Build writes a package to w.
//
// It streams: entries are written to the zip as they are produced rather than
// assembled in memory first, so the size of the register does not decide how
// much memory the backend needs.
func Build(ctx context.Context, w io.Writer, src Source, opts Options) (*Manifest, error) {
	if !opts.Kind.Valid() {
		return nil, fmt.Errorf("snapshot: %w", fmt.Errorf("unknown kind %q", opts.Kind))
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.Generator == "" {
		opts.Generator = "doleances"
	}

	zw := zip.NewWriter(w)
	counts := map[string]int{}

	if err := writeRegister(ctx, zw, src, counts); err != nil {
		return nil, err
	}
	if opts.Kind == KindFull {
		if err := writeOperator(ctx, zw, src, counts); err != nil {
			return nil, err
		}
	}

	manifest := &Manifest{
		Kind:          opts.Kind,
		SchemaVersion: SchemaVersion,
		GeneratedAt:   opts.Now.UTC(),
		Generator:     opts.Generator,
		Counts:        counts,
		Notice:        noticeFor(opts.Kind),
	}
	if err := writeJSON(zw, FileManifest, manifest); err != nil {
		return nil, err
	}
	if err := writeFile(zw, FileReadme, []byte(readme(manifest))); err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("snapshot: close archive: %w", err)
	}
	return manifest, nil
}

// writeRegister writes the part of the package every kind carries. Nothing
// here is personal data.
func writeRegister(ctx context.Context, zw *zip.Writer, src Source, counts map[string]int) error {
	subjects, err := src.Subjects(ctx)
	if err != nil {
		return fmt.Errorf("snapshot: read subjects: %w", err)
	}
	if err := writeJSON(zw, FileSubjects, subjects); err != nil {
		return err
	}
	counts["subjects"] = len(subjects)

	aliases, err := src.SubjectAliases(ctx)
	if err != nil {
		return fmt.Errorf("snapshot: read subject aliases: %w", err)
	}
	if err := writeJSON(zw, FileAliases, aliases); err != nil {
		return err
	}
	counts["subject_aliases"] = len(aliases)

	messages, err := src.PublishedMessages(ctx)
	if err != nil {
		return fmt.Errorf("snapshot: read messages: %w", err)
	}
	for _, message := range messages {
		name := path.Join(DirMessages, message.ID+".md")
		if err := writeFile(zw, name, messageDocument(message)); err != nil {
			return err
		}
	}
	counts["messages"] = len(messages)

	texts, err := src.HistoricalTexts(ctx)
	if err != nil {
		return fmt.Errorf("snapshot: read historical texts: %w", err)
	}
	for _, text := range texts {
		name := path.Join(DirHistorical, text.ID+".json")
		if err := writeJSON(zw, name, text); err != nil {
			return err
		}
	}
	counts["historical"] = len(texts)
	return nil
}

// writeOperator writes the part only an admin may hold.
func writeOperator(ctx context.Context, zw *zip.Writer, src Source, counts map[string]int) error {
	groups, err := src.Groups(ctx)
	if err != nil {
		return fmt.Errorf("snapshot: read groups: %w", err)
	}
	for _, group := range groups {
		if err := writeJSON(zw, path.Join(DirGroups, group.ID+".json"), group); err != nil {
			return err
		}
	}
	counts["groups"] = len(groups)

	actions, err := src.Actions(ctx)
	if err != nil {
		return fmt.Errorf("snapshot: read actions: %w", err)
	}
	for _, action := range actions {
		// Nested under the group so that a package is navigable by hand.
		name := path.Join(DirGroups, action.GroupID, "actions", action.ID+".json")
		if err := writeJSON(zw, name, action); err != nil {
			return err
		}
	}
	counts["actions"] = len(actions)

	entries, err := src.AuditEntries(ctx)
	if err != nil {
		return fmt.Errorf("snapshot: read audit log: %w", err)
	}
	if err := writeAudit(zw, entries); err != nil {
		return err
	}
	counts["audit_entries"] = len(entries)

	settings, err := src.Settings(ctx)
	if err != nil {
		return fmt.Errorf("snapshot: read settings: %w", err)
	}
	names := make([]string, 0, len(settings))
	for name := range settings {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := writeJSON(zw, path.Join(DirSettings, name+".json"), settings[name]); err != nil {
			return err
		}
	}
	counts["settings"] = len(settings)
	return nil
}

// writeAudit groups the log by month, one JSON object per line. The log is
// append-only and read chronologically, so a line-delimited file per month is
// the shape that stays usable as it grows.
func writeAudit(zw *zip.Writer, entries []models.AuditEntry) error {
	byMonth := map[string][]models.AuditEntry{}
	for _, entry := range entries {
		month := entry.CreatedAt.UTC().Format("2006-01")
		byMonth[month] = append(byMonth[month], entry)
	}

	months := make([]string, 0, len(byMonth))
	for month := range byMonth {
		months = append(months, month)
	}
	sort.Strings(months)

	for _, month := range months {
		var buf strings.Builder
		encoder := json.NewEncoder(&buf)
		for _, entry := range byMonth[month] {
			if err := encoder.Encode(entry); err != nil {
				return fmt.Errorf("snapshot: encode audit entry: %w", err)
			}
		}
		name := path.Join(DirAudit, month+".jsonl")
		if err := writeFile(zw, name, []byte(buf.String())); err != nil {
			return err
		}
	}
	return nil
}

func writeJSON(zw *zip.Writer, name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("snapshot: encode %s: %w", name, err)
	}
	return writeFile(zw, name, append(data, '\n'))
}

func writeFile(zw *zip.Writer, name string, data []byte) error {
	f, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("snapshot: create %s: %w", name, err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("snapshot: write %s: %w", name, err)
	}
	return nil
}
