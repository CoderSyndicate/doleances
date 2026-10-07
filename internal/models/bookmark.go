package models

import "time"

// BookmarkKind says which register a kept text is in.
//
// Both, because the two are the same act performed twice — that is the claim
// this whole project rests on, and the pages already say so: a passage from
// 1789 carries the same controls as a doléance written this morning, on every
// page either appears. A keep button on one and not the other, side by side in
// the same list, would read as a bug and would quietly take a position the
// rest of the design refuses.
type BookmarkKind string

const (
	// BookmarkMessage is a doléance.
	BookmarkMessage BookmarkKind = "message"
	// BookmarkHistorical is a passage from the corpus.
	BookmarkHistorical BookmarkKind = "historical"
)

// Valid reports whether a kind is one of the two. Arriving from a request, so
// it is arbitrary text until proven otherwise.
func (k BookmarkKind) Valid() bool {
	return k == BookmarkMessage || k == BookmarkHistorical
}

// Bookmark is a text an account asked to keep.
//
// # Why this exists where a per-account like does not
//
// They look like the same row and they are opposite things. A like is a count
// of presses and has to be, because the register's readers are anonymous and
// there is nobody to attribute one to; storing (account, message) to
// deduplicate likes would *be* the record this project refuses to hold, and no
// hashing avoids it — a scheme the operator can compute, with the account list
// in the same database, is reversible by the operator.
//
// A bookmark inverts every part of that. The reader asked for it, in as many
// words. They are the only person it is for, they are shown the whole list,
// and they can take any of it back. It is a filing cabinet somebody chose, not
// a record kept about them — which is why it is allowed to name them and a
// like is not.
//
// It also delivers what "list the ones I kept" was wanted for, without the
// entanglement: the register still cannot say who liked anything, and this
// table says nothing about what anybody pressed.
//
// # It holds nothing but the fact
//
// No note, no tag, no folder. A note is a private text about a public one, and
// the moment it exists it is something the operator holds about a reader that
// the reader did not mean to publish. The list is the feature.
type Bookmark struct {
	Model

	AccountID string       `gorm:"index;size:36;uniqueIndex:idx_bookmark" json:"account_id"`
	Kind      BookmarkKind `gorm:"size:16;uniqueIndex:idx_bookmark" json:"kind"`

	// TargetID is the doléance or the passage. A plain identifier rather than
	// a relation: nothing here constrains the register's tables, and the two
	// places a text is deleted from clean up after themselves.
	TargetID string `gorm:"index;size:36;uniqueIndex:idx_bookmark" json:"target_id"`

	// KeptAt is when they kept it, which is what the list is ordered by —
	// most recently kept first, like every other listing in this project.
	KeptAt time.Time `json:"kept_at"`
}
