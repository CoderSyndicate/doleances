// Package models is the persisted domain: messages, groups, actions,
// participants and the audit log.
package models

import (
	"time"

	"gorm.io/gorm"
)

// ReviewStatus is where a submission stands in the curation pipeline. Every
// submittable type — message, group, action — travels the same states.
type ReviewStatus string

const (
	// StatusPending is awaiting LLM assessment.
	StatusPending ReviewStatus = "pending"
	// StatusAssessing is claimed by a worker and being scored right now.
	//
	// It is not a resting place: anything left here by a crash is returned to
	// pending by a sweep. A submission stuck in this state would be invisible
	// to both the classifier and the curation queue, which is the one place a
	// doléance could disappear without anybody deciding anything.
	StatusAssessing ReviewStatus = "assessing"

	// StatusCurating is awaiting a human decision.
	StatusCurating ReviewStatus = "curating"
	// StatusAccepted is published.
	StatusAccepted ReviewStatus = "accepted"
	// StatusRejected was refused by a curator. Rejected content is deleted,
	// so this state is short-lived; the audit log keeps the decision.
	StatusRejected ReviewStatus = "rejected"
	// StatusDropped was refused by the classifier without human involvement.
	StatusDropped ReviewStatus = "dropped"
)

// Valid reports whether the status is one the pipeline defines.
func (s ReviewStatus) Valid() bool {
	switch s {
	case StatusPending, StatusCurating, StatusAccepted, StatusRejected, StatusDropped:
		return true
	}
	return false
}

// Location is a point pinned on a map. Kept as plain coordinates rather than a
// PostGIS type so SQLite and PostgreSQL behave identically; proximity is a
// geohash prefix search refined by a distance check in Go.
type Location struct {
	Latitude  float64 `gorm:"index" json:"latitude"`
	Longitude float64 `gorm:"index" json:"longitude"`

	// Geohash is the coordinates as a prefix-searchable string, so "near
	// here" is an indexed string comparison that PostgreSQL and SQLite answer
	// identically. It is derived, never supplied: every write recomputes it
	// from the coordinates, because a geohash that disagrees with its own
	// latitude is a silent wrong answer rather than a visible error.
	Geohash string `gorm:"index;size:12" json:"geohash,omitempty"`

	// Label is what to show a human: a town, a district. Never used to search.
	Label string `json:"label,omitempty"`

	// CountryCode is the ISO 3166-1 alpha-2 code, for coarse filtering.
	CountryCode string `gorm:"size:2;index" json:"country_code,omitempty"`
}

// Model is the base every persisted type embeds. IDs are UUID strings so that
// a record's public identity never depends on insertion order.
type Model struct {
	ID        string    `gorm:"primaryKey;size:36" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// BeforeCreate assigns an identifier when one was not set explicitly.
func (m *Model) BeforeCreate(*gorm.DB) error {
	if m.ID == "" {
		m.ID = NewID()
	}
	return nil
}
