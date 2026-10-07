package models

import (
	"github.com/CoderSyndicate/doleances/internal/subjects"

	"github.com/CoderSyndicate/doleances/internal/content"
	"gorm.io/gorm"

	"github.com/CoderSyndicate/doleances/internal/geo"
)

// Refresh recomputes the derived geohash from the coordinates.
//
// Nothing outside this file should ever set Geohash. It is derived data, and
// the one failure mode worth designing against is a geohash that no longer
// matches the point it claims to describe: the row would then be invisible to
// a search of its own neighbourhood and would surface in somebody else's,
// with nothing to indicate anything was wrong.
func (l *Location) Refresh() {
	if l == nil {
		return
	}
	if l.Latitude == 0 && l.Longitude == 0 {
		// Null Island is not a place anybody pinned; treat it as "no location"
		// rather than encoding a point in the Gulf of Guinea.
		l.Geohash = ""
		return
	}
	l.Geohash = geo.Encode(l.Latitude, l.Longitude)
}

// The hooks below are what make "derived, never supplied" true. Every write
// path — the API, a seed, a snapshot restore — goes through GORM, so putting
// the recomputation here means no caller can forget it and no future endpoint
// can introduce a stale hash.

// BeforeSave keeps a message's geohash in step with its coordinates.
func (m *Message) BeforeSave(*gorm.DB) error {
	m.Location.Refresh()
	// Derived here rather than at the call site so that a seed, a snapshot
	// restore or a test produces a row the duplicate check can see. A message
	// whose hash does not match its own text is invisible to that check, and
	// nothing about the row looks wrong.
	m.ContentHash = content.Hash(m.Text)
	// And the card-sized version, for the same reason again: a row whose
	// excerpt disagrees with its own text is a card quoting something the page
	// it links to does not say, and nothing about the row would look wrong.
	m.Excerpt, m.Truncated = content.Excerpt(m.Text, ExcerptRunes)
	return nil
}

// ExcerptRunes is how much of a doléance its stored excerpt keeps.
//
// A package variable rather than a constant, because it is an operator's
// judgement about a shape on a screen; and a variable rather than something
// threaded through every call, because the hook that uses it is a GORM
// callback with nowhere to receive an argument.
//
// **Set it once, at startup, before anything writes.** Changing it later is a
// configuration change like any other and needs the stored excerpts
// re-derived — which the backend does on its next start.
var ExcerptRunes = content.DefaultExcerptRunes

// SetExcerptRunes fixes the length for this process. A value of zero or less
// is a setting that was never written, not a chosen one, and is ignored.
func SetExcerptRunes(runes int) {
	if runes > 0 {
		ExcerptRunes = runes
	}
}

// BeforeSave keeps a pending revision's geohash in step.
func (r *MessageRevision) BeforeSave(*gorm.DB) error {
	r.Location.Refresh()
	return nil
}

// BeforeSave keeps a group's geohash and its name key in step.
func (g *Group) BeforeSave(*gorm.DB) error {
	g.Location.Refresh()
	// Derived here so that a seed, a snapshot restore or a test cannot produce
	// a group whose key disagrees with its name — which would be a second
	// group under a name the form promised was taken.
	if g.NameKey == "" {
		g.NameKey = subjects.MatchKey(g.Name)
	}
	return nil
}

// BeforeSave keeps a group revision's geohash and its name key in step, on the
// same terms as the group it proposes to change.
func (r *GroupRevision) BeforeSave(*gorm.DB) error {
	r.Location.Refresh()
	if r.NameKey == "" {
		r.NameKey = subjects.MatchKey(r.Name)
	}
	return nil
}

// BeforeSave keeps an action's geohash in step.
func (a *Action) BeforeSave(*gorm.DB) error {
	a.Location.Refresh()
	return nil
}

// BeforeSave keeps a historical passage's geohash in step.
//
// A passage is seeded from a file rather than submitted by anybody, which is
// exactly why the hook matters: the one write path that never goes near a
// form is the one most likely to be given coordinates and no hash.
func (h *HistoricalText) BeforeSave(*gorm.DB) error {
	h.Location.Refresh()
	return nil
}
