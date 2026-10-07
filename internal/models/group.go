package models

import "time"

// GroupRole is what somebody may manage in a group. There is no creator role:
// whoever creates a group is simply its first admin, so a group never depends
// on one irreplaceable person.
type GroupRole string

const (
	// RoleAdmin may manage the group and its actions.
	RoleAdmin GroupRole = "admin"
	// RoleHost may manage actions only.
	RoleHost GroupRole = "host"
)

// Valid reports whether the role is one the model defines.
func (r GroupRole) Valid() bool {
	return r == RoleAdmin || r == RoleHost
}

// Group is a local action group.
type Group struct {
	Model

	Name        string `gorm:"size:256" json:"name"`
	Description string `gorm:"type:text" json:"description,omitempty"`

	// NameKey is the tolerant form of the name, and it is unique.
	//
	// Two groups cannot share a name. This is what replaces duplicate
	// detection for groups: the content guard that serves messages would
	// refuse the second village to write "Collectif citoyen — nous nous
	// réunissons le premier mardi", which is not a duplicate but two places
	// doing the same reasonable thing. A name is a different kind of claim,
	// and one a person can be asked to change while they are filling the form.
	//
	// Tolerant rather than literal, so "Collectif Citoyen" does not walk past
	// "collectif citoyen". Derived, never supplied — the same rule as the
	// geohash and the subject keys, for the same reason.
	//
	// The cost, stated plainly: uniqueness is global, so a group in Guéret and
	// one in Bayonne cannot both be "Collectif citoyen". The second picks
	// another name, and the register gains a vocabulary where a group can be
	// named without being pointed at.
	NameKey  string   `gorm:"uniqueIndex;size:256" json:"-"`
	Location Location `gorm:"embedded;embeddedPrefix:location_" json:"location"`

	Status     ReviewStatus `gorm:"size:16;index" json:"status"`
	Confidence int          `json:"confidence"`

	// AssessedAt, AssessedBy, AssessmentAttempts and AssessmentReason are the
	// same record a message keeps: which model ruled, when, how many times it
	// was tried, and the sentence it gave.
	//
	// AssessedAt doubles as the stale-claim clock — a group left mid-claim by
	// a crashed process is returned to the queue by the same sweep that
	// rescues doléances, and for the same reason: nothing may go missing
	// because a process died between claiming and recording.
	AssessedAt         *time.Time `gorm:"index" json:"assessed_at,omitempty"`
	AssessedBy         string     `gorm:"size:128" json:"assessed_by,omitempty"`
	AssessmentAttempts int        `json:"assessment_attempts"`
	AssessmentReason   string     `gorm:"size:512" json:"-"`

	// Visible is whether the group appears on the map. Inactivity clears it;
	// it never deletes the group, and the group page keeps resolving so an
	// admin can post an action and bring it back.
	Visible bool `gorm:"index" json:"visible"`

	// LastActivityAt drives visibility: no activity within the window and the
	// group stops being surfaced.
	LastActivityAt time.Time `gorm:"index" json:"last_activity_at"`

	Memberships []GroupMembership `gorm:"constraint:OnDelete:CASCADE" json:"-"`
	Actions     []Action          `gorm:"constraint:OnDelete:CASCADE" json:"-"`
}

// GroupRevision is an edit to a group awaiting review.
//
// Editing does not publish directly, or it would be a way around the review
// that creation required. While a revision waits, **the published group stays
// exactly as it is on the map** — that is the whole reason this is a separate
// row rather than an in-place update that sends the group back to pending. A
// group that vanished from the map every time somebody fixed a typo would
// teach people not to fix typos.
//
// A pending group has no published version to protect, so it is edited in
// place and simply re-enters assessment. Only an accepted group produces one
// of these.
type GroupRevision struct {
	Model

	// GroupID is unique: a second edit overwrites the pending one, because the
	// author's latest intent is the one worth reviewing.
	GroupID string `gorm:"uniqueIndex;size:36" json:"group_id"`

	Name        string `gorm:"size:256" json:"name"`
	Description string `gorm:"type:text" json:"description,omitempty"`

	// NameKey is the tolerant form of the proposed name, unique across
	// revisions so two pending edits cannot both claim one name. It is checked
	// against the live groups separately, and again when the revision is
	// applied: a name free when somebody proposed it may have gone by the time
	// a curator says yes.
	NameKey string `gorm:"uniqueIndex;size:256" json:"-"`

	// Location travels with the revision because a meeting place is part of
	// what a curator accepted. Moving a pin after the fact would otherwise be
	// the one change that skipped review.
	Location Location `gorm:"embedded;embeddedPrefix:location_" json:"location"`

	Status     ReviewStatus `gorm:"size:16;index" json:"status"`
	Confidence int          `json:"confidence"`

	// The same assessment record a group keeps, held separately so that a
	// revision under review never overwrites the verdict the published group
	// was accepted on.
	AssessedAt         *time.Time `gorm:"index" json:"assessed_at,omitempty"`
	AssessedBy         string     `gorm:"size:128" json:"assessed_by,omitempty"`
	AssessmentAttempts int        `json:"assessment_attempts"`
	AssessmentReason   string     `gorm:"size:512" json:"-"`
}

// GroupMembership ties an account to a group, with an optional role.
type GroupMembership struct {
	Model

	GroupID   string `gorm:"index;size:36;uniqueIndex:idx_membership" json:"group_id"`
	AccountID string `gorm:"index;size:36;uniqueIndex:idx_membership" json:"account_id"`

	// Role is empty for an ordinary user, who joins actions and nothing more.
	Role GroupRole `gorm:"size:16;index" json:"role,omitempty"`

	JoinedAt time.Time `json:"joined_at"`
}

// ActionType distinguishes a one-off event from a standing commitment. They
// expire in opposite ways: a one-time action retires once its date passes, a
// recurrent one has no date to expire on and so must be re-confirmed instead.
type ActionType string

const (
	// ActionOneTime happens once, on a date.
	ActionOneTime ActionType = "onetime"
	// ActionRecurrent repeats, e.g. a monthly meeting.
	ActionRecurrent ActionType = "recurrent"
)

// Valid reports whether the type is one the model defines.
func (t ActionType) Valid() bool {
	return t == ActionOneTime || t == ActionRecurrent
}

// Action is something a group does.
type Action struct {
	Model

	GroupID     string   `gorm:"index;size:36" json:"group_id"`
	Title       string   `gorm:"size:256" json:"title"`
	Description string   `gorm:"type:text" json:"description,omitempty"`
	Location    Location `gorm:"embedded;embeddedPrefix:location_" json:"location"`

	Type ActionType `gorm:"size:16;index" json:"type"`

	// StartsAt is when a one-time action happens — and, for a recurrent one,
	// **DTSTART**: when the series begins, what time of day it happens, and
	// what an interval is counted from. "Every second Thursday" is meaningless
	// without a Thursday to count from, and counting from today would give a
	// different answer every day it was asked.
	StartsAt *time.Time `json:"starts_at,omitempty"`

	// RecurrenceRule is the rhythm, as an RFC 5545 recurrence rule:
	// `FREQ=MONTHLY;BYDAY=4TH`. Nobody types it — a form assembles it — and
	// the standard is what lets this data leave the project intact, a calendar
	// subscription being a serialisation away rather than a rewrite.
	//
	// See internal/recur for the subset that is understood, and for why
	// anything outside it is refused rather than ignored.
	RecurrenceRule string `gorm:"size:256" json:"recurrence_rule,omitempty"`

	// RecurrenceNote is the nuance no rule carries — "except in August".
	//
	// It exists because the nuance always exists and never parses, and because
	// the alternative is people writing it into the title. Nothing reads it
	// but a person.
	RecurrenceNote string `gorm:"size:256" json:"recurrence_note,omitempty"`

	// NextOccurrenceAt is when this next happens, derived and stored.
	//
	// Derived, because a rhythm is not a date; stored, because a page that
	// evaluated the rule on every render could not order by it, could not
	// answer "what is on in the next thirty days", and would do the same
	// arithmetic for every visitor. For a one-time action it is simply
	// StartsAt, so one indexed column orders both kinds together.
	//
	// Nil means nothing is coming: a finished one-off, or a rule that yields
	// no date. A listing puts those last.
	NextOccurrenceAt *time.Time `gorm:"index" json:"next_occurrence_at,omitempty"`

	Status     ReviewStatus `gorm:"size:16;index" json:"status"`
	Confidence int          `json:"confidence"`

	// The same assessment record a group keeps: which model ruled, when, how
	// many times it was tried, and the sentence it gave. AssessedAt doubles as
	// the stale-claim clock.
	AssessedAt         *time.Time `gorm:"index" json:"assessed_at,omitempty"`
	AssessedBy         string     `gorm:"size:128" json:"assessed_by,omitempty"`
	AssessmentAttempts int        `json:"assessment_attempts"`
	AssessmentReason   string     `gorm:"size:512" json:"-"`

	// ConfirmedAt is the last time a contact vouched that a recurrent action
	// still happens. Unconfirmed past the window, the action is retired.
	ConfirmedAt time.Time `gorm:"index" json:"confirmed_at"`

	// Retired means the action has left the map. Retirement is not deletion:
	// the row and its audit trail stay.
	Retired bool `gorm:"index" json:"retired"`

	Occurrences []ActionOccurrence `gorm:"constraint:OnDelete:CASCADE" json:"occurrences,omitempty"`
}

// ActionOccurrence is one dated instance of an action. Intents attach here
// rather than to the action: "I'm at the meeting in March" is a different
// statement from "I attend this monthly meeting".
type ActionOccurrence struct {
	Model

	ActionID string    `gorm:"index;size:36" json:"action_id"`
	StartsAt time.Time `gorm:"index" json:"starts_at"`

	// Cancelled occurrences keep their row so participants can be told, and
	// so an intent history is not silently rewritten.
	Cancelled bool `gorm:"index" json:"cancelled"`

	Intents []Intent `gorm:"foreignKey:OccurrenceID;constraint:OnDelete:CASCADE" json:"-"`
}

// Intent is somebody saying they will be at one occurrence.
//
// A rescheduled occurrence resets its intents: an intent is a commitment to be
// somewhere at a time, and moving the time invalidates the commitment made.
type Intent struct {
	Model

	OccurrenceID string `gorm:"index;size:36;uniqueIndex:idx_intent" json:"occurrence_id"`
	AccountID    string `gorm:"index;size:36;uniqueIndex:idx_intent" json:"account_id"`

	DeclaredAt time.Time `json:"declared_at"`
}
