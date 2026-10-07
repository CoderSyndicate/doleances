package models

// Where a relation came from.
const (
	// RelationSourceCurator: a person asserted it while editing a subject.
	RelationSourceCurator = "curator"

	// RelationSourceWikidata: derived from a `subclass of` statement between
	// two entities the register already holds.
	RelationSourceWikidata = "wikidata"
)

// SubjectRelation is a broader/narrower link between two subjects.
//
// # Why the register wants a hierarchy rather than fewer subjects
//
// The first answer to "public transport and transport are the same idea" was to
// merge them and keep the broader word. That is wrong for this register:
// somebody complaining about buses means *public transport*, the classifier
// gets that right, and flattening it to `transport` throws away the precision
// that makes the subject worth filtering by.
//
// So both survive, and the relation carries the generality instead. A reader
// filtering by the broader subject can be given the narrower ones too; a
// reader filtering by the narrower one gets exactly what they asked for.
//
// # Many to many, deliberately
//
// `public transport` is a subclass of *both* `transport service` and `public
// service` in Wikidata, and for a register of grievances that is exactly
// right: a doléance about a bus route is a transport grievance and a
// public-services grievance at once. A tree would force a choice that the
// subject matter does not have.
//
// # Where they come from
//
// Either a curator asserting one while editing a subject, or a `subclass of`
// statement between two entities the register already holds. Measured on a
// real vocabulary, the second alone is not enough: 13 identified subjects
// produced 3 edges and left 8 of them unconnected, because their parents were
// concepts nobody had written a doléance about yet. That is why a curator can
// create a parent or a child from the entity search directly.
type SubjectRelation struct {
	Model

	// ChildID is the narrower subject, ParentID the broader one.
	//
	// The pair is unique: asserting the same link twice is one relation, and
	// the direction is part of the identity — reversing it is a different
	// claim, and the store refuses it as a cycle rather than storing both.
	ChildID  string `gorm:"index;size:36;uniqueIndex:idx_subject_relation" json:"child_id"`
	ParentID string `gorm:"index;size:36;uniqueIndex:idx_subject_relation" json:"parent_id"`

	// Source is how it was established — see the constants above. A curator
	// looking at a link that surprises them needs to know whether a person
	// asserted it or Wikidata did.
	Source string `gorm:"size:16" json:"source"`
}

// TableName keeps the plural consistent with the rest of the schema.
func (SubjectRelation) TableName() string { return "subject_relations" }
