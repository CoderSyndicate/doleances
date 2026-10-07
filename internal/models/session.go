package models

import "time"

// Session is one signed-in device.
//
// Tokens are per device because a person on a phone and a laptop holds two
// independent sessions, each individually revocable: signing one device out
// must not sign the others out.
//
// A session is proved by a passkey rather than by a link in a mailbox, which
// is the change that removed the last piece of personal data this register
// held about a reader. What is stored is a hash, as everywhere else: the site
// cannot act as somebody, and cannot be made to.
type Session struct {
	Model

	AccountID string `gorm:"index;size:36" json:"account_id"`

	// TokenHash is all that is kept of the session token.
	TokenHash string `gorm:"uniqueIndex;size:64" json:"-"`

	// DeviceLabel is how somebody recognises this device in their own list.
	DeviceLabel string `gorm:"size:128" json:"device_label,omitempty"`

	ExpiresAt  time.Time `gorm:"index" json:"expires_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}
