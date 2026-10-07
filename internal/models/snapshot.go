package models

import "time"

// SnapshotKind mirrors snapshot.Kind. It is duplicated as a string here rather
// than imported so that the models package stays free of dependencies on the
// packages that consume it.
type SnapshotKind string

const (
	SnapshotContributions SnapshotKind = "contributions"
	SnapshotFull          SnapshotKind = "full"
)

// Snapshot is a generated export held in storage.
//
// The row is the catalogue; the bytes live in the storage bucket under
// StorageKey. Keeping them apart is what lets a large register be exported
// without the database growing by the size of its own contents.
type Snapshot struct {
	Model

	// Tag names the snapshot for a human: "before-the-march", "weekly-2026-09".
	// Unique, because it is how a snapshot is addressed in a URL.
	Tag string `gorm:"uniqueIndex;size:128" json:"tag"`

	Kind SnapshotKind `gorm:"index;size:32" json:"kind"`

	// StorageKey is the object name inside the storage bucket.
	StorageKey string `gorm:"size:512" json:"-"`

	// SizeBytes is the packaged size, so the console can show what a download
	// will cost before somebody starts it.
	SizeBytes int64 `json:"size_bytes"`

	// Counts is the manifest's record tally, stored as JSON so that listing
	// snapshots does not mean opening every archive.
	Counts string `gorm:"type:text" json:"-"`

	// Notes is why this snapshot was taken, in the operator's words.
	Notes string `gorm:"type:text" json:"notes,omitempty"`

	// GeneratedAt is when the data was read, which is what the manifest says
	// and is not necessarily when the row was written.
	GeneratedAt time.Time `gorm:"index" json:"generated_at"`
}

// Public reports whether this snapshot may be served to anybody who asks.
// Only the contributions kind is free of personal data.
func (s Snapshot) Public() bool { return s.Kind == SnapshotContributions }
