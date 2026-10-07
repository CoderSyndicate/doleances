package models

import (
	"regexp"
	"testing"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewIDFormat(t *testing.T) {
	if got := NewID(); !uuidV4.MatchString(got) {
		t.Errorf("NewID() = %q, want a canonical UUIDv4", got)
	}
}

func TestNewIDIsUnique(t *testing.T) {
	// Identifiers appear in permalinks, so one must not be derivable from
	// another.
	seen := make(map[string]bool, 10000)
	for range 10000 {
		id := NewID()
		if seen[id] {
			t.Fatalf("NewID returned %q twice", id)
		}
		seen[id] = true
	}
}

func TestBeforeCreateAssignsOnlyWhenEmpty(t *testing.T) {
	var m Model
	if err := m.BeforeCreate(nil); err != nil {
		t.Fatalf("BeforeCreate: %v", err)
	}
	if m.ID == "" {
		t.Fatal("no identifier was assigned")
	}

	assigned := m.ID
	if err := m.BeforeCreate(nil); err != nil {
		t.Fatalf("BeforeCreate: %v", err)
	}
	if m.ID != assigned {
		t.Error("an explicitly set identifier was overwritten")
	}
}
