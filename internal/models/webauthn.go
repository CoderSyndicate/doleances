package models

import (
	"strings"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// The transport list is stored as one comma-separated column rather than a
// table of its own: it is three short words that are always read together and
// never queried on.
func splitList(value string) []string { return strings.Split(value, ",") }
func joinList(values []string) string { return strings.Join(values, ",") }

// The methods below are what go-webauthn asks of a user.
//
// They live on Account rather than on a wrapper type so that there is one
// answer to "what credentials does this account have?" — a second type that
// had to be kept in step would drift, and the thing that drifted would be the
// list passed as excludeCredentials, whose whole job is to be complete.

// WebAuthnID is the user handle. Opaque, random, and carrying nothing about
// the person: it is stored on the authenticator and may be shown by a
// password manager.
func (a *Account) WebAuthnID() []byte { return a.Handle }

// WebAuthnName and WebAuthnDisplayName are what a password manager shows
// beside the site's name when somebody picks a passkey.
//
// There is no username here to return, and inventing one would put a made-up
// identifier in somebody's keychain for ever. The account's chosen name is
// used when there is one; otherwise the site speaks for itself, which is
// honest — the entry means "an account on this register" and nothing more.
func (a *Account) WebAuthnName() string {
	if a.Name != "" {
		return a.Name
	}
	return "doléances"
}

// WebAuthnDisplayName is the same, for the same reason.
func (a *Account) WebAuthnDisplayName() string { return a.WebAuthnName() }

// WebAuthnCredentials is every passkey this account has.
//
// Complete, always. It is passed as `excludeCredentials` at registration,
// where its job is to stop an authenticator that already holds a passkey for
// this account from silently making a second one — a list missing an entry
// produces exactly that, and the duplicate is invisible until somebody
// wonders why their device list has two of everything.
func (a *Account) WebAuthnCredentials() []webauthn.Credential {
	credentials := make([]webauthn.Credential, 0, len(a.Credentials))
	for _, stored := range a.Credentials {
		credentials = append(credentials, stored.ToWebAuthn())
	}
	return credentials
}

// ToWebAuthn turns a stored row back into what the library verifies against.
func (c Credential) ToWebAuthn() webauthn.Credential {
	return webauthn.Credential{
		ID:              c.CredentialID,
		PublicKey:       c.PublicKey,
		AttestationType: c.AttestationType,
		Transport:       c.transports(),
		Flags: webauthn.CredentialFlags{
			BackupEligible: c.BackupEligible,
			BackupState:    c.BackedUp,
		},
		Authenticator: webauthn.Authenticator{
			AAGUID:    c.AAGUID,
			SignCount: c.SignCount,
		},
	}
}

// transports splits the stored list back into what the protocol expects.
func (c Credential) transports() []protocol.AuthenticatorTransport {
	if c.Transports == "" {
		return nil
	}

	var out []protocol.AuthenticatorTransport
	for _, name := range splitList(c.Transports) {
		out = append(out, protocol.AuthenticatorTransport(name))
	}
	return out
}

// FromWebAuthn fills a row from a freshly registered credential.
//
// The account, the name and the timestamps are the caller's to set: this knows
// about the protocol and nothing about why somebody is registering.
func FromWebAuthn(credential webauthn.Credential) Credential {
	stored := Credential{
		CredentialID:    credential.ID,
		PublicKey:       credential.PublicKey,
		AttestationType: credential.AttestationType,
		AAGUID:          credential.Authenticator.AAGUID,
		SignCount:       credential.Authenticator.SignCount,
		BackupEligible:  credential.Flags.BackupEligible,
		BackedUp:        credential.Flags.BackupState,
	}

	names := make([]string, 0, len(credential.Transport))
	for _, transport := range credential.Transport {
		names = append(names, string(transport))
	}
	stored.Transports = joinList(names)
	return stored
}
