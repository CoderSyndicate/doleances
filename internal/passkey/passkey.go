// Package passkey is this register's WebAuthn relying party.
//
// It wraps `go-webauthn` rather than being used directly from the handlers,
// for the reason the rest of this project keeps its boundaries in packages of
// their own: a ceremony is two requests with a server-held challenge between
// them, and the rules about what may be carried between those requests are
// the security property. Spreading them across handlers is how one of them
// ends up trusting something the client sent.
//
// It is named for the thing rather than the library. WebAuthn is one
// implementation of "somebody proves they hold a key"; the handlers ask this
// package, not a vendor.
package passkey

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// ErrRefused means the ceremony did not prove what it had to.
//
// One error for a wrong signature, an expired challenge, a mismatched origin
// and a malformed answer. The caller has nothing useful to do with the
// difference, and telling a client which check it failed is telling an
// attacker which one to work on. The underlying reason is logged; it is never
// answered.
var ErrRefused = errors.New("passkey: the ceremony was refused")

// Options is where this register lives, as the browser sees it.
type Options struct {
	// ID is the relying party identifier: the registrable domain, with no
	// scheme and no port.
	//
	// It is the single most consequential string here, because a passkey is
	// bound to it for ever: change it and every credential in the database
	// stops working, with no migration possible — the private halves are on
	// people's devices and cannot be re-keyed. It has to be the domain the
	// register will still be on in five years, not the host it happens to be
	// served from today.
	ID string

	// DisplayName is what a password manager shows beside the entry.
	DisplayName string

	// Origins are the full origins a ceremony may come from — scheme, host and
	// port. A browser reports the page's own origin in the signed data, and a
	// mismatch is refused: this is what stops a passkey for this register
	// being usable by a site that merely looks like it.
	Origins []string
}

// Service is the relying party.
type Service struct {
	web *webauthn.WebAuthn
	id  string
}

// New builds the relying party, refusing a configuration that could not work.
//
// Failing here rather than at the first sign-in is deliberate: a bad RPID
// produces a browser error nobody on the server sees, so the only symptom of
// the misconfiguration would be that nobody can sign in and nothing is logged.
func New(opts Options) (*Service, error) {
	id := strings.TrimSpace(opts.ID)
	if id == "" {
		return nil, errors.New("passkey: no relying party id configured")
	}
	if len(opts.Origins) == 0 {
		return nil, errors.New("passkey: no origins configured")
	}

	name := strings.TrimSpace(opts.DisplayName)
	if name == "" {
		name = "doléances"
	}

	web, err := webauthn.New(&webauthn.Config{
		RPID:          id,
		RPDisplayName: name,
		RPOrigins:     opts.Origins,
	})
	if err != nil {
		return nil, fmt.Errorf("passkey: %w", err)
	}
	return &Service{web: web, id: id}, nil
}

// ID is the relying party identifier, for the logs and the account page.
func (s *Service) ID() string { return s.id }

// BeginRegistration asks the browser to make a passkey.
//
// Returns what to hand the browser and the challenge state to keep. The state
// is JSON because it is stored in a row and read back by the second request;
// it is the library's own structure, kept opaque so there is not a second
// definition of it here to fall out of step.
func (s *Service) BeginRegistration(account *models.Account) (creation *protocol.CredentialCreation, state []byte, err error) {
	// A discoverable credential — a "resident key" — is what makes a
	// usernameless sign-in possible at all: the authenticator stores the
	// handle beside the key, so the browser can offer the right entry with
	// nobody typing an identifier the register does not have.
	//
	// Required rather than preferred, because a passkey that is not
	// discoverable cannot be used here: there would be no way to name the
	// account it belongs to.
	opts := []webauthn.RegistrationOption{
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementRequired,
			// Preferred, not required: a hardware key with no PIN and no
			// fingerprint reader is still a perfectly good second passkey for
			// somebody whose only other one is on a phone they might drop.
			UserVerification: protocol.VerificationPreferred,
		}),
		// Every passkey the account already has, so an authenticator holding
		// one does not silently make a second. A list missing an entry is how
		// a device list ends up with two of everything.
		webauthn.WithExclusions(webauthn.Credentials(account.WebAuthnCredentials()).CredentialDescriptors()),
	}

	creation, session, err := s.web.BeginRegistration(account, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("passkey: begin registration: %w", err)
	}

	state, err = json.Marshal(session)
	if err != nil {
		return nil, nil, fmt.Errorf("passkey: keep the challenge: %w", err)
	}
	return creation, state, nil
}

// FinishRegistration verifies the browser's answer and returns the row to
// store.
func (s *Service) FinishRegistration(account *models.Account, state, answer []byte) (models.Credential, error) {
	session, err := decodeState(state)
	if err != nil {
		return models.Credential{}, err
	}

	parsed, err := protocol.ParseCredentialCreationResponseBytes(answer)
	if err != nil {
		return models.Credential{}, fmt.Errorf("%w: %s", ErrRefused, err)
	}

	credential, err := s.web.CreateCredential(account, session, parsed)
	if err != nil {
		return models.Credential{}, fmt.Errorf("%w: %s", ErrRefused, err)
	}
	return models.FromWebAuthn(*credential), nil
}

// BeginLogin asks the browser for any passkey it holds for this register.
//
// Discoverable, so nothing is typed: the browser offers what it has, and the
// assertion comes back carrying the handle of whichever was chosen. This is
// the whole reason the account has no username — there is nothing to ask for
// before the challenge can be issued.
func (s *Service) BeginLogin() (assertion *protocol.CredentialAssertion, state []byte, err error) {
	assertion, session, err := s.web.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationPreferred),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("passkey: begin login: %w", err)
	}

	state, err = json.Marshal(session)
	if err != nil {
		return nil, nil, fmt.Errorf("passkey: keep the challenge: %w", err)
	}
	return assertion, state, nil
}

// Lookup finds the account an assertion belongs to.
//
// It is a function rather than a store dependency so this package stays
// ignorant of persistence: what it needs is "given these bytes, whose account
// is this", and there is more than one road to that answer.
type Lookup func(rawID, handle []byte) (*models.Account, error)

// Assertion is what a verified sign-in establishes.
type Assertion struct {
	Account *models.Account

	// CredentialID is which of the account's passkeys was used, so its
	// last-used time and counter can be recorded.
	CredentialID []byte

	// SignCount and BackedUp are what the authenticator reported this time.
	// Recorded rather than enforced — see models.Credential.SignCount.
	SignCount uint32
	BackedUp  bool
}

// FinishLogin verifies the browser's answer and says who signed in.
func (s *Service) FinishLogin(state, answer []byte, lookup Lookup) (Assertion, error) {
	session, err := decodeState(state)
	if err != nil {
		return Assertion{}, err
	}

	parsed, err := protocol.ParseCredentialRequestResponseBytes(answer)
	if err != nil {
		return Assertion{}, fmt.Errorf("%w: %s", ErrRefused, err)
	}

	var found *models.Account
	handler := func(rawID, handle []byte) (webauthn.User, error) {
		account, err := lookup(rawID, handle)
		if err != nil {
			return nil, err
		}
		found = account
		return account, nil
	}

	credential, err := s.web.ValidateDiscoverableLogin(handler, session, parsed)
	if err != nil {
		return Assertion{}, fmt.Errorf("%w: %s", ErrRefused, err)
	}
	if found == nil {
		return Assertion{}, ErrRefused
	}

	return Assertion{
		Account:      found,
		CredentialID: credential.ID,
		SignCount:    credential.Authenticator.SignCount,
		BackedUp:     credential.Flags.BackupState,
	}, nil
}

func decodeState(state []byte) (webauthn.SessionData, error) {
	var session webauthn.SessionData
	if err := json.Unmarshal(state, &session); err != nil {
		// A challenge this server stored and cannot read back is a bug here,
		// not a client's fault — but the client still only learns that the
		// ceremony was refused.
		return webauthn.SessionData{}, fmt.Errorf("%w: unreadable challenge: %s", ErrRefused, err)
	}
	return session, nil
}
