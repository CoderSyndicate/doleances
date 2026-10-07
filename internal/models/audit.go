package models

import "time"

// AuditAction is what a privileged human did.
type AuditAction string

const (
	AuditAccept       AuditAction = "accept"
	AuditReject       AuditAction = "reject"
	AuditRoleGrant    AuditAction = "role_grant"
	AuditRoleRevoke   AuditAction = "role_revoke"
	AuditDelete       AuditAction = "delete"
	AuditConfigChange AuditAction = "config_change"

	// AuditMergeSubjects and AuditKeepSubjects record the two answers to a
	// subject question. Both are audited, not only the merge: "these are not
	// the same" is the decision that stops the pair ever being asked about
	// again, so it needs a name against it as much as the merge does.
	AuditMergeSubjects AuditAction = "merge_subjects"
	AuditKeepSubjects  AuditAction = "keep_subjects"

	// AuditRenameSubject records a changed label. The entry carries the old
	// one, because the row no longer does and a rename is otherwise
	// untraceable — the filter people were using silently becomes a different
	// word.
	AuditRenameSubject AuditAction = "rename_subject"

	// AuditConfirmEntity and AuditRejectEntity record the two answers to a
	// Wikidata identity. Confirming is the only path that ever writes a QID,
	// so it is the one privileged action behind every translated label and
	// every identity-based merge the register makes.
	// AuditLinkSubjects and AuditUnlinkSubjects record a change to the
	// hierarchy. A relation decides which doléances a broader filter reaches,
	// so asserting one is a privileged act like any other.
	AuditLinkSubjects   AuditAction = "link_subjects"
	AuditUnlinkSubjects AuditAction = "unlink_subjects"

	AuditConfirmEntity AuditAction = "confirm_entity"
	AuditRejectEntity  AuditAction = "reject_entity"
)

// AuditEntry records one privileged console action.
//
// The log is append-only: never updated, never deleted. It is the counterweight
// to curators holding accept/reject power over other people's words, so a
// decision can always be traced back to a named human.
//
// It is a record about curators, not about contributors. It must never become
// a back door to contributor identity or to the text of a deleted message —
// which is why it holds a subject reference and a reason, and no content.
type AuditEntry struct {
	// No embedded Model: an audit entry is written once and never updated, so
	// it has a creation time and no modification time.
	ID        string    `gorm:"primaryKey;size:36" json:"id"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`

	// Actor is the curator's OIDC identity.
	Actor string `gorm:"index;size:256" json:"actor"`

	Action AuditAction `gorm:"index;size:32" json:"action"`

	// SubjectType and SubjectID say what was acted on: "message", "group",
	// "action", "participant".
	SubjectType string `gorm:"index;size:32" json:"subject_type"`
	SubjectID   string `gorm:"index;size:36" json:"subject_id"`

	// Reason is the curator's justification, in their words.
	Reason string `gorm:"type:text" json:"reason,omitempty"`
}

// TableName keeps the plural consistent with the other tables.
func (AuditEntry) TableName() string { return "audit_entries" }
