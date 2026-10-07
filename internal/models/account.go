package models

import "time"

// Account is somebody who signs in, and nothing more than that.
//
// # There is deliberately no name, no address and no identifier a person chose
//
// This is the whole point of the change that introduced it. The register used
// to key a participant by their email address, which made that address the one
// piece of personal data it held about a reader — and made the separation of
// the two identity domains in *Notifications* a rule somebody had to keep
// rather than a fact about the schema.
//
// An account is a random handle and a set of credentials. It cannot be looked
// up by a person's name because it has none, it cannot be mailed because there
// is nowhere to mail, and a database in the wrong hands says only that
// somebody joined a group — never who.
//
// The cost is stated plainly rather than left to be discovered: anybody can
// mint one of these instantly, so the barrier that a working mailbox used to
// provide is gone and the curation queue is what is left.
type Account struct {
	Model

	// Handle is the WebAuthn user handle: random bytes the authenticator
	// stores beside the credential and hands back at sign-in.
	//
	// It is what makes a usernameless login possible — the browser offers the
	// credentials it holds for this site and returns this with the one chosen,
	// so nobody types anything. It must be opaque and carry no meaning: the
	// specification is explicit that a handle must not contain personal
	// information, because it is stored on the authenticator and may be shown
	// by a password manager.
	Handle []byte `gorm:"uniqueIndex;size:64" json:"-"`

	// Name is what this account calls itself inside a group, chosen freely and
	// never verified. It is the successor to the free-text name a participant
	// used to give beside their address.
	Name string `gorm:"size:128" json:"name,omitempty"`

	LastSeenAt time.Time `json:"last_seen_at"`

	Credentials   []Credential   `gorm:"constraint:OnDelete:CASCADE" json:"-"`
	RecoveryCodes []RecoveryCode `gorm:"constraint:OnDelete:CASCADE" json:"-"`
}

// Credential is one passkey.
//
// An account has several on purpose: a phone, a laptop, a hardware key. That
// is not a convenience, it is the recovery story — a passkey that exists in
// one place is an account one dropped phone away from being unreachable, and
// there is no address to mail a reset to.
type Credential struct {
	Model

	AccountID string `gorm:"index;size:36" json:"-"`

	// ID is the credential's own identifier, as the authenticator issued it.
	// Indexed and unique because a sign-in arrives carrying this and nothing
	// else: it is how an assertion finds the account it belongs to.
	CredentialID []byte `gorm:"uniqueIndex;size:255" json:"-"`

	// PublicKey verifies the signatures. There is no private key here and
	// there never can be: it does not leave the authenticator.
	PublicKey []byte `json:"-"`

	// AttestationType and AAGUID say what kind of authenticator this is.
	// Recorded rather than acted on: the register has no business refusing
	// somebody's security key because of who made it.
	AttestationType string `gorm:"size:32" json:"-"`
	AAGUID          []byte `gorm:"size:16" json:"-"`

	// SignCount is the authenticator's own counter.
	//
	// It is a clone detector: a counter that goes backwards means two things
	// are using one credential. Many authenticators — including every synced
	// passkey — always report zero, so a zero counter proves nothing and must
	// not be treated as suspicious.
	SignCount uint32 `json:"-"`

	// BackupEligible and BackedUp say whether this passkey is synced to a
	// cloud keychain. They decide what the account page can honestly tell
	// somebody: a synced passkey survives losing the device, a device-bound
	// one does not, and only one of those needs a second passkey urgently.
	BackupEligible bool `json:"backup_eligible"`
	BackedUp       bool `json:"backed_up"`

	// Transports is how the browser reached this authenticator — "internal",
	// "usb", "hybrid". Passed back on later sign-ins so the browser can
	// prompt for the right thing rather than offering every option.
	Transports string `gorm:"size:128" json:"transports,omitempty"`

	// Name is what the person calls this device in their own list. Defaulted
	// from the browser and the platform, and renameable, because "a passkey"
	// six times over is a list nobody can revoke safely from.
	Name string `gorm:"size:128" json:"name"`

	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// RecoveryCode is a way back in when every passkey is gone.
//
// Issued once at signup, hashed like every other credential in this project,
// and single-use. It exists because there is no address to send a reset to:
// without it, losing the only device is losing the account, and somebody who
// joined a group would simply cease to be that person.
type RecoveryCode struct {
	Model

	AccountID string `gorm:"index;size:36" json:"-"`

	// CodeHash is all that is kept. The codes are shown once, at signup, and
	// nothing can recover them afterwards — the same promise the doléance
	// deletion token makes, for the same reason.
	CodeHash string `gorm:"uniqueIndex;size:64" json:"-"`

	UsedAt *time.Time `json:"used_at,omitempty"`
}

// RecoveryCodeCount is how many are issued at signup.
//
// Ten, because they are used one at a time and each use is a bad day: too few
// and somebody runs out while still locked out, too many and the list stops
// being something a person will actually write down.
const RecoveryCodeCount = 10

// CeremonyLifetime is how long a half-finished WebAuthn exchange stands.
//
// A registration or a sign-in is two requests with a challenge between them,
// and the challenge is what stops an old signature being replayed. Two minutes
// is generous for somebody touching a fingerprint reader and mean for anybody
// holding a captured one.
const CeremonyLifetime = 2 * time.Minute

// WebAuthnCeremony is the half of an exchange that must survive between the
// two requests.
//
// It is kept here rather than handed to the browser because the challenge is
// the security property: a client that carried its own challenge could replay
// one, and "the server remembers what it asked" is the whole of why a
// signature means anything.
type WebAuthnCeremony struct {
	Model

	// AccountID is set for a registration by somebody already signed in —
	// adding a second passkey — and empty for a signup or a sign-in, where
	// there is nobody yet.
	AccountID string `gorm:"index;size:36" json:"-"`

	// Data is the library's own session state, as JSON. Opaque on purpose:
	// what it holds is the library's business and re-deriving its shape here
	// would be a second definition to keep in step.
	Data []byte `json:"-"`

	// Subject is what the ceremony is about, for the cases where that cannot
	// be looked up yet.
	//
	// A signup is the reason it exists: the account does not exist until the
	// second request, so the handle and the chosen name have to survive
	// between the two — and they cannot be carried by the client, because a
	// browser that chose its own handle could attach a passkey to somebody
	// else's account. Empty for every other purpose.
	Subject []byte `json:"-"`

	// Purpose is "signup", "signin", "add-passkey" or "link-device", so a
	// challenge issued for one cannot be finished as another.
	Purpose string `gorm:"size:16" json:"-"`

	ExpiresAt time.Time `gorm:"index" json:"-"`
}

// LinkLifetime is how long a device-linking QR code works.
//
// Short, because it is on a screen being photographed rather than in a
// mailbox: the two minutes are for walking the phone over, not for keeping.
const LinkLifetime = 2 * time.Minute

// LinkToken carries an account from a signed-in device to a new one.
//
// It exists because synced passkeys only cross devices inside one ecosystem,
// and the built-in cross-device QR needs Bluetooth proximity. Somebody with an
// Android phone and a Mac has neither, and without this they would need a
// second account.
//
// **Two things make it safe, and both are needed.** It is single-use and
// short-lived, so a photographed screen is worth little; and the *old* device
// confirms the new one before the credential is attached, which is what stops
// a relayed QR — somebody who tricks a person into scanning their code still
// has to get that person to approve a browser they do not recognise.
type LinkToken struct {
	Model

	AccountID string `gorm:"index;size:36" json:"-"`
	TokenHash string `gorm:"uniqueIndex;size:64" json:"-"`

	// Claimed records that a new device has presented the token and is waiting
	// to be approved, along with what it says it is. The label is the new
	// device's own claim about itself and is shown to the person approving —
	// it is a prompt, never a fact.
	ClaimedAt   *time.Time `json:"claimed_at,omitempty"`
	ClaimLabel  string     `gorm:"size:128" json:"claim_label,omitempty"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`

	ExpiresAt time.Time `gorm:"index" json:"expires_at"`
}
