package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/snapshot"
)

// ErrSnapshotNotFound is returned when no snapshot carries that tag.
var ErrSnapshotNotFound = errors.New("snapshot not found")

// SnapshotSource adapts the store to snapshot.Source.
//
// It is a distinct type rather than methods on *Store so that the export's
// reads are collected in one place and can be read as a list of exactly what
// leaves the database.
type SnapshotSource struct{ store *Store }

// SnapshotSource returns the export view of this store.
func (s *Store) SnapshotSource() *SnapshotSource { return &SnapshotSource{store: s} }

var _ snapshot.Source = (*SnapshotSource)(nil)

// PublishedMessages yields the public register: accepted messages, oldest
// first. Anything still in the pipeline, dropped or rejected is not the
// register and is deliberately absent.
func (s *SnapshotSource) PublishedMessages(ctx context.Context) ([]models.Message, error) {
	var messages []models.Message
	err := s.store.db.WithContext(ctx).
		Preload("Subjects").
		Where("status = ?", models.StatusAccepted).
		Order("created_at asc").
		Find(&messages).Error
	return messages, err
}

func (s *SnapshotSource) Subjects(ctx context.Context) ([]models.Subject, error) {
	var subjects []models.Subject
	err := s.store.db.WithContext(ctx).Order("slug asc").Find(&subjects).Error
	return subjects, err
}

func (s *SnapshotSource) SubjectAliases(ctx context.Context) ([]models.SubjectAlias, error) {
	var aliases []models.SubjectAlias
	err := s.store.db.WithContext(ctx).Order("match_key asc").Find(&aliases).Error
	return aliases, err
}

func (s *SnapshotSource) HistoricalTexts(ctx context.Context) ([]models.HistoricalText, error) {
	var texts []models.HistoricalText
	err := s.store.db.WithContext(ctx).
		Preload("Translations").
		Order("created_at asc").
		Find(&texts).Error
	return texts, err
}

// Groups yields the groups with their memberships, anonymised.
//
// The account reference is stripped from every membership on the way out. A
// membership row is the whole of what a snapshot should say about a person —
// somebody was in this group — and the identifier that says *which* person is
// exactly the join that must not travel. It is the same operation account
// deletion performs, applied to a copy rather than to the original.
func (s *SnapshotSource) Groups(ctx context.Context) ([]models.Group, error) {
	var groups []models.Group
	err := s.store.db.WithContext(ctx).
		Preload("Memberships").
		Order("created_at asc").
		Find(&groups).Error
	if err != nil {
		return nil, err
	}

	for i := range groups {
		for j := range groups[i].Memberships {
			groups[i].Memberships[j].AccountID = ""
		}
	}
	return groups, nil
}

// Actions yields the actions with their occurrences, intents anonymised for
// the same reason memberships are.
func (s *SnapshotSource) Actions(ctx context.Context) ([]models.Action, error) {
	var actions []models.Action
	err := s.store.db.WithContext(ctx).
		Preload("Occurrences").
		Preload("Occurrences.Intents").
		Order("created_at asc").
		Find(&actions).Error
	if err != nil {
		return nil, err
	}

	for i := range actions {
		for j := range actions[i].Occurrences {
			for k := range actions[i].Occurrences[j].Intents {
				actions[i].Occurrences[j].Intents[k].AccountID = ""
			}
		}
	}
	return actions, nil
}

func (s *SnapshotSource) AuditEntries(ctx context.Context) ([]models.AuditEntry, error) {
	var entries []models.AuditEntry
	err := s.store.db.WithContext(ctx).Order("created_at asc").Find(&entries).Error
	return entries, err
}

// Settings exports operator configuration with every credential replaced.
//
// The redaction happens here, at the boundary, rather than in the builder: the
// builder should never be handed a secret it has to remember not to write.
func (s *SnapshotSource) Settings(ctx context.Context) (map[string]any, error) {
	settings := map[string]any{}

	llm, err := s.store.LLMSettings(ctx)
	if err != nil {
		return nil, err
	}
	if llm.APIKey != "" {
		llm.APIKey = snapshot.RedactedSecret
	}
	settings["llm"] = llm

	curation, err := s.store.CurationSettings(ctx)
	if err != nil {
		return nil, err
	}
	settings["curation"] = curation

	return settings, nil
}

// ---------------------------------------------------------------------------
// The catalogue
// ---------------------------------------------------------------------------

// SaveSnapshot records a generated snapshot. The bytes live in storage; this
// row is how they are found again.
func (s *Store) SaveSnapshot(ctx context.Context, row models.Snapshot) error {
	return s.db.WithContext(ctx).Create(&row).Error
}

// ListSnapshots returns the catalogue, newest first. Passing a kind narrows it
// to that kind; the empty kind lists everything.
func (s *Store) ListSnapshots(ctx context.Context, kind models.SnapshotKind) ([]models.Snapshot, error) {
	query := s.db.WithContext(ctx).Order("generated_at desc")
	if kind != "" {
		query = query.Where("kind = ?", kind)
	}

	var rows []models.Snapshot
	err := query.Find(&rows).Error
	return rows, err
}

// GetSnapshot finds one snapshot by tag.
func (s *Store) GetSnapshot(ctx context.Context, tag string) (models.Snapshot, error) {
	var row models.Snapshot
	err := s.db.WithContext(ctx).Where("tag = ?", tag).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, ErrSnapshotNotFound
	}
	return row, err
}

// LatestSnapshot returns the most recent snapshot of a kind. It is what the
// public download page serves, so a visitor gets a file rather than a list.
func (s *Store) LatestSnapshot(ctx context.Context, kind models.SnapshotKind) (models.Snapshot, error) {
	var row models.Snapshot
	err := s.db.WithContext(ctx).
		Where("kind = ?", kind).
		Order("generated_at desc").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, ErrSnapshotNotFound
	}
	return row, err
}

// DeleteSnapshot removes one catalogue row and reports the storage key whose
// object the caller must now delete. The row goes first: an orphaned object
// wastes space, but an orphaned row is a download that 500s.
func (s *Store) DeleteSnapshot(ctx context.Context, tag string) (string, error) {
	row, err := s.GetSnapshot(ctx, tag)
	if err != nil {
		return "", err
	}
	if err := s.db.WithContext(ctx).Delete(&models.Snapshot{}, "tag = ?", tag).Error; err != nil {
		return "", err
	}
	return row.StorageKey, nil
}

// PruneSnapshots keeps the newest `keep` snapshots of a kind and returns the
// storage keys of those it removed, for the caller to delete. A keep of zero
// prunes nothing.
func (s *Store) PruneSnapshots(ctx context.Context, kind models.SnapshotKind, keep int) ([]string, error) {
	if keep <= 0 {
		return nil, nil
	}

	var rows []models.Snapshot
	err := s.db.WithContext(ctx).
		Where("kind = ?", kind).
		Order("generated_at desc").
		Offset(keep).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(rows))
	tags := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, row.StorageKey)
		tags = append(tags, row.Tag)
	}
	if err := s.db.WithContext(ctx).Delete(&models.Snapshot{}, "tag IN ?", tags).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

// EncodeCounts renders a manifest's tally for the catalogue row.
func EncodeCounts(counts map[string]int) string {
	data, err := json.Marshal(counts)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// DecodeCounts reads a catalogue row's tally back.
func DecodeCounts(encoded string) map[string]int {
	counts := map[string]int{}
	if encoded == "" {
		return counts
	}
	_ = json.Unmarshal([]byte(encoded), &counts)
	return counts
}

// ---------------------------------------------------------------------------
// Restore
// ---------------------------------------------------------------------------

// RestoreSnapshot writes a package back into the database, replacing what it
// covers.
//
// It is **kind-aware**, and that is the whole safety property: a contributions
// package replaces the register and nothing else, leaving groups, actions and
// participants untouched. Only a full package may replace those. Restoring a
// contributions export must never be a way to wipe the movement's data.
//
// Within its scope the restore is a full override — the tables it covers are
// emptied and rewritten — so the result is the register as the package
// recorded it, not a merge whose outcome depends on what was there before.
// The whole thing runs in one transaction: a half-restored register is worse
// than a failed restore.
func (s *Store) RestoreSnapshot(ctx context.Context, pkg *snapshot.Package) error {
	if pkg == nil {
		return errors.New("restore: no package")
	}
	if !pkg.Manifest.Kind.Valid() {
		return fmt.Errorf("restore: unknown package kind %q", pkg.Manifest.Kind)
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := restoreRegister(tx, pkg); err != nil {
			return err
		}
		if pkg.Manifest.Kind == snapshot.KindFull {
			return restoreOperator(tx, pkg)
		}
		return nil
	})
}

// restoreRegister replaces the parts every package carries.
func restoreRegister(tx *gorm.DB, pkg *snapshot.Package) error {
	// message_subjects is the join table; it has no model, so it is cleared
	// by name. Leaving it would attach old subjects to restored messages.
	for _, statement := range []string{
		"DELETE FROM message_subjects",
	} {
		if err := tx.Exec(statement).Error; err != nil {
			return fmt.Errorf("restore: clear join table: %w", err)
		}
	}
	if err := clearAll(tx, &models.Message{}, &models.HistoricalTranslation{},
		&models.HistoricalText{}, &models.SubjectAlias{}, &models.Subject{}); err != nil {
		return err
	}

	if err := createAll(tx, pkg.Subjects); err != nil {
		return fmt.Errorf("restore: subjects: %w", err)
	}

	// After the subjects, because an alias with no subject is a word that
	// resolves to nothing.
	if err := createAll(tx, pkg.Aliases); err != nil {
		return fmt.Errorf("restore: subject aliases: %w", err)
	}

	// Subjects arrive on a message as slugs; resolve them to the rows just
	// written rather than letting GORM create duplicates from the association.
	bySlug := map[string]models.Subject{}
	for _, subject := range pkg.Subjects {
		bySlug[subject.Slug] = subject
	}
	for i := range pkg.Messages {
		resolved := make([]models.Subject, 0, len(pkg.Messages[i].Subjects))
		for _, subject := range pkg.Messages[i].Subjects {
			if known, ok := bySlug[subject.Slug]; ok {
				resolved = append(resolved, known)
			}
		}
		pkg.Messages[i].Subjects = resolved
		// A restored message is published: it was published in the package,
		// and re-curating a register somebody already curated would be a way
		// to lose it.
		pkg.Messages[i].Status = models.StatusAccepted
	}
	if err := createAll(tx, pkg.Messages); err != nil {
		return fmt.Errorf("restore: messages: %w", err)
	}
	if err := createAll(tx, pkg.Historical); err != nil {
		return fmt.Errorf("restore: historical texts: %w", err)
	}
	return nil
}

// restoreOperator replaces the parts only a full package carries.
func restoreOperator(tx *gorm.DB, pkg *snapshot.Package) error {
	// Accounts are deliberately untouched. A restore replaces the register and
	// the movement around it; it does not replace who is signed in, because a
	// package carries nobody — and clearing the accounts of the installation
	// doing the restoring would sign out every person on it to import a file
	// that has nothing to put back.
	if err := clearAll(tx, &models.Intent{}, &models.ActionOccurrence{}, &models.Action{},
		&models.GroupMembership{}, &models.Group{}, &models.AuditEntry{}); err != nil {
		return err
	}

	if err := createAll(tx, pkg.Groups); err != nil {
		return fmt.Errorf("restore: groups: %w", err)
	}
	if err := createAll(tx, pkg.Actions); err != nil {
		return fmt.Errorf("restore: actions: %w", err)
	}
	if err := createAll(tx, pkg.AuditEntries); err != nil {
		return fmt.Errorf("restore: audit log: %w", err)
	}
	return nil
}

// clearAll empties tables in the order given, which must be children first so
// that foreign keys are satisfied on both engines.
func clearAll(tx *gorm.DB, targets ...any) error {
	for _, target := range targets {
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(target).Error; err != nil {
			return fmt.Errorf("restore: clear %T: %w", target, err)
		}
	}
	return nil
}

// createAll inserts a slice in batches, keeping the identifiers the package
// carries: a doléance's public identity is its id, and a restore that renamed
// everything would break every permalink ever issued.
func createAll[T any](tx *gorm.DB, rows []T) error {
	if len(rows) == 0 {
		return nil
	}
	return tx.Session(&gorm.Session{FullSaveAssociations: true}).
		Clauses(clause.OnConflict{DoNothing: true}).
		CreateInBatches(rows, 100).Error
}
