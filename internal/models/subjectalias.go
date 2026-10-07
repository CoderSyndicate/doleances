package models

import (
	"gorm.io/gorm"

	"github.com/CoderSyndicate/doleances/internal/subjects"
)

// Where an alias came from.
const (
	// AliasSourceWikidata: fetched from the entity's labels. The register did
	// not learn this spelling from a contributor — it was pre-filled, so that
	// the first German doléance about health is recognised rather than
	// becoming a curator's question.
	AliasSourceWikidata = "wikidata"

	// AliasSourceMerge: a label that used to be a subject of its own until a
	// curator folded it into this one. Kept so their decision keeps working:
	// the next arrival of that spelling resolves silently instead of
	// recreating the row they just removed.
	AliasSourceMerge = "merge"
)

// SubjectAlias is another spelling of a subject.
//
// # Why the vocabulary needs this
//
// A subject has one label and many names. "santé", "Gesundheit" and "health"
// are one thing, and a register organised by subject has to treat them as one
// or its filters fracture by language. Without aliases the second spelling is
// either a duplicate row or a question for a curator; with them it is an
// indexed lookup that costs nothing.
//
// # Where they come from
//
// Mostly from Wikidata, ahead of time. The moment a subject is given a QID,
// the entity already knows what that concept is called in every language the
// register speaks, so those names are written down immediately rather than
// discovered one contributor at a time. A curator's merge decision leaves one
// too, so the spelling they ruled on never has to be ruled on again.
//
// This is the mechanism by which curation questions become rare: each answered
// question, and each entity ever resolved, permanently teaches the vocabulary
// a set of spellings it will recognise for free from then on.
//
// # What an alias is not
//
// It is not a translation of the label for display, and it is not an identity.
// The subject's own slug stays the identity, and its label stays what a
// curator set. An alias only ever answers "have we seen this word before".
type SubjectAlias struct {
	Model

	// SubjectID is the subject this spelling belongs to.
	SubjectID string `gorm:"index;size:36" json:"subject_id"`

	// Label is the spelling as its source writes it, and Language is what
	// language that is.
	//
	// The language is not decoration here: it is half the key, and an alias
	// with no language is never consulted. See MatchKey.
	Label    string `gorm:"size:128" json:"label"`
	Language string `gorm:"size:16;index;uniqueIndex:idx_alias_key_language" json:"language,omitempty"`

	// MatchKey and FoldKey mirror the subject's own keys, so the alias layer
	// can search with the same lookup a subject uses.
	//
	// Unique **per language**, not globally, and that is the whole safety
	// property of this table. Across fifty languages the same string is
	// constantly two different words: "pain" is bread in French and suffering
	// in English, "Gift" is poison in German and a present in English, "salut"
	// is a greeting in French and health in Catalan — that last one is a real
	// label of Q12147. A globally unique key would let a Catalan name for
	// health answer a French doléance about greetings, and would silently drop
	// every later alias that collided.
	//
	// So an alias is only ever consulted for a doléance written in its own
	// language. See store.FindSubjectByAlias.
	MatchKey string `gorm:"uniqueIndex:idx_alias_key_language;size:128" json:"-"`
	FoldKey  string `gorm:"index;size:128" json:"-"`

	// Source is how this spelling was learned — see the constants above. A
	// curator looking at a subject that matched something unexpected needs to
	// know whether a person decided that or Wikidata did.
	Source string `gorm:"size:16" json:"source"`
}

// BeforeSave derives an alias's keys from its label, for the same reason the
// subject's own hook does: a row whose keys do not match its label is invisible
// to the lookup that is its entire purpose.
func (a *SubjectAlias) BeforeSave(*gorm.DB) error {
	if a.MatchKey == "" {
		a.MatchKey = subjects.MatchKey(a.Label)
	}
	if a.FoldKey == "" {
		a.FoldKey = subjects.FoldPlural(a.MatchKey)
	}
	return nil
}

// TableName keeps the plural consistent with the rest of the schema.
func (SubjectAlias) TableName() string { return "subject_aliases" }
