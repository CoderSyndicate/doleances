package models

import (
	"time"

	"gorm.io/gorm"

	"github.com/CoderSyndicate/doleances/internal/subjects"
)

// What raised a merge suggestion.
const (
	// MergeSourceWikidata: both labels resolved to the same Wikidata entity.
	MergeSourceWikidata = "wikidata"

	// MergeSourceEmbedding: the vectors were close enough to ask.
	MergeSourceEmbedding = "embedding"
)

// SubjectMerge is a question for a curator: are these two subjects one thing?
//
// Merging on a fair guess would quietly collapse two filters and change what
// the register appears to say; refusing to merge at all would let the
// vocabulary fill with synonyms. So the uncertain cases become a short list
// somebody settles. Deciding is a judgement about language, and it belongs to
// a person.
//
// # Two machines, one question
//
// A row carries whatever each signal said about the same pair, because they
// are independent and a curator reading both decides better than either:
//
//	Evidence   a shared Wikidata entity — an identity
//	Similarity a cosine between the two labels — a resemblance
//
// Agreement is the easy case and disagreement is the interesting one. Wikidata
// saying "same entity" while the vectors say 0.31 usually means the search
// landed on something absurd — English "pension" resolves to a guest house —
// and that is exactly the merge nobody would ever have noticed afterwards.
//
// # Why this is affordable
//
// A question only arises for a label the vocabulary has never seen, and new
// labels stop arriving after the first weeks: by then most of what a model
// proposes already exists and exits at layer one, for free. The human cost
// falls away on its own, while a wrong merge would not.
type SubjectMerge struct {
	Model

	// SubjectID is the newcomer; IntoID is the subject it resembles.
	SubjectID string `gorm:"index;size:36;uniqueIndex:idx_subject_merge" json:"subject_id"`
	IntoID    string `gorm:"index;size:36;uniqueIndex:idx_subject_merge" json:"into_id"`

	// MessageID is the doléance whose classification raised the question.
	//
	// It is what makes the question answerable. "Are Verkehr and transport the
	// same subject?" is a linguistics exam in the abstract and an easy call
	// next to the text that produced it, so the console shows the two beside
	// each other and a curator decides while reading.
	MessageID string `gorm:"index;size:36" json:"message_id,omitempty"`

	// Source says which signal raised the question. Both may have something to
	// say about the pair — see the type comment — but one of them is why the
	// row exists, and a curator triaging a list wants that first.
	Source string `gorm:"size:16" json:"source"`

	// Evidence is the shared Wikidata QID, empty when no identity was found.
	Evidence string `gorm:"size:64" json:"evidence,omitempty"`

	// Similarity is the cosine between the two labels, and zero when no
	// vector was available for the comparison.
	//
	// Recorded even on a Wikidata match, which is the point of asking two
	// systems: the number is the second opinion on the identity, and it is
	// also what lets the thresholds be tuned against real decisions rather
	// than against guesses.
	Similarity float64 `json:"similarity"`

	// CrossLanguage marks a pair written in two different languages.
	//
	// Those never merge automatically however close they score. "Wohnen" is a
	// verb and "logement" is a noun; a vector does not know that, and a
	// curator who reads one of the two languages does.
	CrossLanguage bool `json:"cross_language"`

	// ResolvedAt and Merged record what was decided. A dismissed suggestion is
	// kept rather than deleted: without it the same pair is proposed again on
	// the next submission, and a curator answers the same question for ever.
	ResolvedAt *time.Time `gorm:"index" json:"resolved_at,omitempty"`
	Merged     bool       `json:"merged"`
}

// BeforeSave derives a subject's matching keys from its label.
//
// Derived, never supplied — the same rule as the geohash, for the same reason.
// A subject written by a seed, a snapshot restore or a test must be findable by
// the deduplication layers, and a row whose keys do not match its own label is
// invisible to layer one: the next spelling of it becomes a second row, and the
// filter list grows a duplicate nobody can explain.
//
// A caller that set the keys deliberately keeps them, so a curator renaming a
// subject can decide whether the match key follows.
func (s *Subject) BeforeSave(*gorm.DB) error {
	if s.MatchKey == "" {
		s.MatchKey = subjects.MatchKey(s.Label)
	}
	if s.FoldKey == "" {
		s.FoldKey = subjects.FoldPlural(s.MatchKey)
	}
	if s.Slug == "" {
		s.Slug = subjects.Slug(s.Label)
	}
	return nil
}
