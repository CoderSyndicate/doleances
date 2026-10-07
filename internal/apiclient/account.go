package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Account is a reader as they see themselves.
//
// There is almost nothing in it, and that is the point: an account is a random
// handle, a set of public keys, and a name somebody chose and nobody verified.
// `Participant.Email` was the only personal data this register held about a
// reader, and it is gone.
type Account struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`

	Passkeys int `json:"passkeys"`

	// RecoveryCodesLeft is how many are unspent. A sheet down to its last
	// code is worth replacing, and nobody can be told that without the count.
	RecoveryCodesLeft int64 `json:"recovery_codes_left"`

	Unread int64 `json:"unread"`

	// PushConfigured says whether this installation can send a notification at
	// all, so a page can offer permission honestly rather than asking for
	// something it could never use.
	PushConfigured bool `json:"push_configured"`

	LastSeenAt time.Time `json:"last_seen_at"`
}

// Passkey is one of an account's credentials.
type Passkey struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	// Synced says whether this passkey survives losing the device. It decides
	// whether somebody urgently needs a second one, which is the only reason
	// the account page mentions it.
	Synced bool `json:"synced"`

	AddedAt    time.Time  `json:"added_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// Used renders when a passkey was last used, or nothing.
func (p Passkey) Used() string {
	if p.LastUsedAt == nil {
		return ""
	}
	return p.LastUsedAt.Format("2 January 2006")
}

// DeviceSession is one signed-in device.
type DeviceSession struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`

	// Current marks the device asking, so nobody signs themselves out while
	// trying to remove a laptop they left at the office.
	Current bool `json:"current"`

	SignedInAt time.Time `json:"signed_in_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// Seen renders when a device was last used.
func (d DeviceSession) Seen() string { return d.LastSeenAt.Format("2 January 2006 15:04") }

// NotifiedDevice is one browser that has agreed to be told things.
type NotifiedDevice struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`

	AddedAt    time.Time  `json:"added_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// Devices is the three lists an account page shows.
//
// Three, not one. A synced passkey signs somebody in on a device that has never
// been asked for notification permission, so "has a passkey here", "is signed
// in here" and "can be notified here" are different facts about different
// devices — and a single merged list would be wrong about all three.
type Devices struct {
	Passkeys      []Passkey        `json:"passkeys"`
	Sessions      []DeviceSession  `json:"sessions"`
	Subscriptions []NotifiedDevice `json:"subscriptions"`
}

// Me reads the reader's own account.
func (c *Client) Me(ctx context.Context) (Account, error) {
	var account Account
	if err := c.get(ctx, "/v1/accounts/me", &account); err != nil {
		return Account{}, err
	}
	return account, nil
}

// Devices reads the reader's passkeys, sessions and notified devices.
func (c *Client) Devices(ctx context.Context) (Devices, error) {
	var devices Devices
	if err := c.get(ctx, "/v1/accounts/me/devices", &devices); err != nil {
		return Devices{}, err
	}
	return devices, nil
}

// RenameAccount changes the name an account goes by inside its groups.
func (c *Client) RenameAccount(ctx context.Context, name string) (Account, error) {
	body, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return Account{}, fmt.Errorf("encode name: %w", err)
	}

	var account Account
	if err := c.sendJSON(ctx, http.MethodPatch, "/v1/accounts/me", body, &account); err != nil {
		return Account{}, err
	}
	return account, nil
}

// DeleteAccount erases the reader.
//
// Real deletion of everything that could point back at them. What stays is
// their participation with the reference removed, so a past attendance reads as
// anonymous and a group's history stays accurate.
func (c *Client) DeleteAccount(ctx context.Context) error {
	var ignored json.RawMessage
	return c.sendJSON(ctx, http.MethodDelete, "/v1/accounts/me", []byte("{}"), &ignored)
}

// SignOut signs the calling device out.
func (c *Client) SignOut(ctx context.Context) error {
	var ignored json.RawMessage
	return c.postJSON(ctx, "/v1/accounts/me/signout", []byte("{}"), &ignored)
}

// RenamePasskey changes what a device is called in its owner's list.
func (c *Client) RenamePasskey(ctx context.Context, id, name string) (Devices, error) {
	body, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return Devices{}, fmt.Errorf("encode name: %w", err)
	}

	var devices Devices
	path := "/v1/accounts/me/passkeys/" + url.PathEscape(id)
	if err := c.sendJSON(ctx, http.MethodPatch, path, body, &devices); err != nil {
		return Devices{}, err
	}
	return devices, nil
}

// DeletePasskey removes one, never the last.
func (c *Client) DeletePasskey(ctx context.Context, id string) (Devices, error) {
	var devices Devices
	path := "/v1/accounts/me/passkeys/" + url.PathEscape(id)
	if err := c.sendJSON(ctx, http.MethodDelete, path, []byte("{}"), &devices); err != nil {
		return Devices{}, err
	}
	return devices, nil
}

// RevokeSession signs another device out.
func (c *Client) RevokeSession(ctx context.Context, id string) (Devices, error) {
	var devices Devices
	path := "/v1/accounts/me/sessions/" + url.PathEscape(id)
	if err := c.sendJSON(ctx, http.MethodDelete, path, []byte("{}"), &devices); err != nil {
		return Devices{}, err
	}
	return devices, nil
}

// Unsubscribe stops notifying one device.
func (c *Client) Unsubscribe(ctx context.Context, id string) error {
	var ignored json.RawMessage
	path := "/v1/notifications/subscriptions/" + url.PathEscape(id)
	return c.sendJSON(ctx, http.MethodDelete, path, []byte("{}"), &ignored)
}

// ReissueRecoveryCodes replaces the set and returns it once.
//
// Once is all there is: only the hashes are kept, so nothing here or anywhere
// else can show them again.
func (c *Client) ReissueRecoveryCodes(ctx context.Context) ([]string, error) {
	var answer struct {
		Codes []string `json:"recovery_codes"`
	}
	if err := c.postJSON(ctx, "/v1/accounts/me/recovery-codes", []byte("{}"), &answer); err != nil {
		return nil, err
	}
	return answer.Codes, nil
}

// Notification is one thing somebody was told.
type Notification struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`

	Title string `json:"title"`
	Body  string `json:"body,omitempty"`

	// SubjectType and SubjectID are what it is about, so an item can link
	// somewhere rather than being a sentence with no door.
	SubjectType string `json:"subject_type,omitempty"`
	SubjectID   string `json:"subject_id,omitempty"`

	Read      bool      `json:"read"`
	CreatedAt time.Time `json:"created_at"`
}

// When renders the moment a notification was made.
func (n Notification) When() string { return n.CreatedAt.Format("2 January 2006 15:04") }

// Href is where the item leads, or empty when it leads nowhere.
//
// Only the kinds this frontend has a page for produce a link. An item with no
// door is still worth showing — it is a record of something that was said —
// and a link to a page that does not exist would be worse than none.
func (n Notification) Href() string {
	if n.SubjectType == "group" && n.SubjectID != "" {
		return "/groups/" + n.SubjectID
	}
	return ""
}

// Notifications is the reader's own list.
type Notifications struct {
	Items  []Notification `json:"items"`
	Total  int64          `json:"total"`
	Unread int64          `json:"unread"`
}

// ListNotifications reads the list.
func (c *Client) ListNotifications(ctx context.Context, limit, offset int) (Notifications, error) {
	path := fmt.Sprintf("/v1/notifications?limit=%d&offset=%d", limit, offset)

	var list Notifications
	if err := c.get(ctx, path, &list); err != nil {
		return Notifications{}, err
	}
	return list, nil
}

// ReadNotification marks one read.
func (c *Client) ReadNotification(ctx context.Context, id string) error {
	var ignored json.RawMessage
	path := "/v1/notifications/" + url.PathEscape(id) + "/read"
	return c.postJSON(ctx, path, []byte("{}"), &ignored)
}

// ReadAllNotifications clears the bell.
func (c *Client) ReadAllNotifications(ctx context.Context) error {
	var ignored json.RawMessage
	return c.postJSON(ctx, "/v1/notifications/read", []byte("{}"), &ignored)
}

// DeleteNotification removes one from the list.
func (c *Client) DeleteNotification(ctx context.Context, id string) error {
	var ignored json.RawMessage
	path := "/v1/notifications/" + url.PathEscape(id)
	return c.sendJSON(ctx, http.MethodDelete, path, []byte("{}"), &ignored)
}

// PushKey is the VAPID public key a browser needs in order to subscribe, and
// whether this installation can send at all.
func (c *Client) PushKey(ctx context.Context) (key string, configured bool, err error) {
	var answer struct {
		PublicKey  string `json:"public_key"`
		Configured bool   `json:"configured"`
	}
	if err := c.get(ctx, "/v1/notifications/key", &answer); err != nil {
		return "", false, err
	}
	return answer.PublicKey, answer.Configured, nil
}

// PendingLink is a device waiting to be approved on an account.
type PendingLink struct {
	Waiting bool   `json:"waiting"`
	ID      string `json:"id,omitempty"`

	// Label is what the new device says it is. Shown to whoever approves it,
	// and never believed: a browser can claim anything.
	Label string `json:"label,omitempty"`

	ClaimedAt *time.Time `json:"claimed_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// ReadPendingLink reads what is waiting to be approved.
func (c *Client) ReadPendingLink(ctx context.Context) (PendingLink, error) {
	var waiting PendingLink
	if err := c.get(ctx, "/v1/accounts/me/link", &waiting); err != nil {
		return PendingLink{}, err
	}
	return waiting, nil
}
