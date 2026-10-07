package models

import "time"

// PushSubscription is one browser on one device that has agreed to be told
// things.
//
// It is the successor to an email address and it is a better one: an endpoint
// issued by a push service is a way to reach a browser, not a way to identify
// a person. It cannot be searched for, written to by anybody but this server,
// or correlated with anything outside this register — which is why it is
// compatible with an account that holds nothing else.
//
// One account has several, and they are **not** the same set as its passkeys:
// a synced passkey signs somebody in on a device that has never been asked for
// permission. "Signed in here" and "notified here" are different facts, and
// the account page shows them as two lists for that reason.
type PushSubscription struct {
	Model

	AccountID string `gorm:"index;size:36" json:"-"`

	// Endpoint is the push service's address for this browser. Unique: a
	// browser that re-subscribes gets the same endpoint back, and two rows for
	// it would send everything twice to one screen.
	Endpoint string `gorm:"uniqueIndex;size:512" json:"-"`

	// P256dh and Auth are the browser's own keys. The payload is encrypted to
	// them, so the push service carries the message without being able to read
	// it — which is what keeps a notification about somebody's group private
	// from the company delivering it.
	P256dh string `gorm:"size:255" json:"-"`
	Auth   string `gorm:"size:64" json:"-"`

	// Label is how the person recognises this device in their own list.
	Label string `gorm:"size:128" json:"label"`

	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// NotificationKind is what happened.
type NotificationKind string

const (
	// NotifyGroupMessage is somebody writing to a group.
	NotifyGroupMessage NotificationKind = "group_message"

	// NotifyActionConfirm asks whether a recurrent action still happens.
	NotifyActionConfirm NotificationKind = "action_confirm"

	// NotifyOccurrenceMoved says a meeting an intent was declared for has
	// changed, so the commitment made is no longer the one on offer.
	NotifyOccurrenceMoved NotificationKind = "occurrence_moved"

	// NotifyGroupDecision is a curator's verdict on a group somebody runs.
	NotifyGroupDecision NotificationKind = "group_decision"
)

// Notification is one thing worth telling somebody, held so that it survives
// whether or not a push reached them.
//
// **The list is the channel; the push is a tap on the shoulder.** An iPhone
// that has not installed the site receives no push at all, a permission can be
// refused, and an endpoint can die between one week and the next — so nothing
// may exist only as a push. Everything is written here first and delivered
// second, and somebody who never allows a notification still finds out by
// coming back.
type Notification struct {
	Model

	AccountID string           `gorm:"index;size:36" json:"-"`
	Kind      NotificationKind `gorm:"size:32;index" json:"kind"`

	// Subject is what it is about — a group, an action, an occurrence — so the
	// item can link somewhere rather than being a sentence with no door.
	SubjectType string `gorm:"size:32" json:"subject_type,omitempty"`
	SubjectID   string `gorm:"size:36;index" json:"subject_id,omitempty"`

	// Title and Body are stored rendered, in the language the reader had when
	// it was made.
	//
	// Rendering at read time would be tidier and would also mean a
	// notification silently changing its words when a catalogue is edited, or
	// losing them when a key is renamed. A notification is a record of
	// something that was said at a moment; it is written down.
	Title string `gorm:"size:256" json:"title"`
	Body  string `gorm:"size:512" json:"body,omitempty"`

	ReadAt *time.Time `gorm:"index" json:"read_at,omitempty"`
}

// GroupMessage is somebody writing to a group.
//
// This is what replaced the contact address: the register holds no way to
// reach anybody, so reaching a group happens inside it. A signed-in person
// writes, the group's admins are notified, and neither side learns anything
// about the other beyond a chosen name.
//
// It is the first private text this project has carried, and that is worth
// naming. Everything else here is written to be read by everybody, which is
// what makes the classifier and the curation queue the right shape. A message
// to a group is read by two or three people, so the same guards are applied at
// the door — sanitation, executable content refused — and the group's own
// admins are the moderators of what reaches them.
type GroupMessage struct {
	Model

	GroupID string `gorm:"index;size:36" json:"group_id"`

	// FromAccountID is who wrote it. Nothing about them is published beyond
	// the name they chose; an account carries neither address nor real name.
	FromAccountID string `gorm:"index;size:36" json:"-"`
	FromName      string `gorm:"size:128" json:"from_name,omitempty"`

	Text string `gorm:"type:text" json:"text"`

	ReadAt *time.Time `json:"read_at,omitempty"`
}
