package models

import "time"

// SubjectEntity is a proposed Wikidata identity for a subject, awaiting a
// human.
//
// # Why nothing attributes a QID by itself
//
// A QID decides two things that are very hard to undo: what the subject is
// called in every language the register speaks, and which other subjects are
// treated as the same as it. Getting it wrong is not a cosmetic error —
// measured on real traffic, an unconfirmed pipeline attributed *spaceflight*
// to "transports", *solitary confinement* to "isolement", and a Belgian
// magazine to "laïcité", then wrote each of those into thirty languages and
// into the matching path, where a German doléance about prison isolation would
// have resolved to rural loneliness.
//
// So the rule is absolute: **no QID is attributed without a person confirming
// it.** Subject.QID stays empty until then, and every layer that reads it —
// deduplication, aliases, translations — simply does not see the subject.
//
// # What the model is for
//
// Making the question easy, not answering it. The pick and its confidence turn
// a curator's job from investigating an entity into recognising one, and a
// curator shown "transport en commun — moyen de transporter plusieurs
// personnes ensemble" for *transports* answers in a second where one shown
// *spaceflight* has to go and look.
//
// The confidence is recorded so that a later policy could let the clearest
// cases through unattended. That needs a measured false-positive rate per
// band, which does not exist yet, so nothing acts on it today.
type SubjectEntity struct {
	Model

	// SubjectID is the subject this entity was proposed for. One open
	// proposal per subject: asking twice about the same word is how a queue
	// stops being read.
	SubjectID string `gorm:"index;size:36;uniqueIndex:idx_subject_entity_open" json:"subject_id"`

	// QID, Label and Description are the proposed entity as Wikidata
	// describes it, stored rather than re-fetched: a curator must see what the
	// model saw, and the entity's description can change under us.
	QID         string `gorm:"size:32;index" json:"qid"`
	Label       string `gorm:"size:256" json:"label"`
	Description string `gorm:"size:512" json:"description,omitempty"`

	// MatchType is how the search found the entity — "label", "alias" or
	// "description". An alias match is the shape of the worst errors and a
	// curator should see it.
	MatchType string `gorm:"size:16" json:"match_type,omitempty"`

	// Confidence is the model's, 0-100, and Reason is its one sentence.
	Confidence int    `json:"confidence"`
	Reason     string `gorm:"size:512" json:"reason,omitempty"`

	// MessageID is the doléance whose classification raised the proposal, so
	// the question can be read next to the text that prompted it.
	MessageID string `gorm:"index;size:36" json:"message_id,omitempty"`

	// ResolvedAt and Accepted record the decision. A rejected proposal is kept
	// rather than deleted: without it the same entity is proposed again on the
	// next doléance carrying the subject, and a curator answers the same
	// question for ever.
	//
	// ResolvedAt is part of the unique index so that a subject may have one
	// *open* proposal and any number of settled ones.
	ResolvedAt *time.Time `gorm:"index;uniqueIndex:idx_subject_entity_open" json:"resolved_at,omitempty"`
	Accepted   bool       `json:"accepted"`
}

// TableName keeps the plural consistent with the rest of the schema.
func (SubjectEntity) TableName() string { return "subject_entities" }
