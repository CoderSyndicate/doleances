package backend

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/content"
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/passkey"
	"github.com/CoderSyndicate/doleances/internal/store"
	"github.com/CoderSyndicate/doleances/internal/token"
)

// handleBytes is how much randomness a user handle carries.
//
// The specification recommends the whole 64 bytes and says the handle must not
// contain personal information. Both matter here: it is stored on the
// authenticator and may be shown by a password manager, so it is the one
// identifier this register hands to somebody else's software — and it says
// nothing.
const handleBytes = 32

// maxAccountNameRunes bounds the name an account goes by.
const maxAccountNameRunes = 60

// maxDeviceLabelRunes bounds what a device calls itself.
//
// The label is the browser's own claim, sent by the page, and it is shown to a
// person deciding whether to approve a linked device — so it is a prompt, not
// a fact, and it is cut rather than trusted.
const maxDeviceLabelRunes = 60

func (a *API) registerAccountRoutes(api huma.API) {
	huma.Register(api, bounded(huma.Operation{
		OperationID: "begin-signup",
		Method:      http.MethodPost,
		Path:        "/v1/accounts/register/begin",
		Summary:     "Start making an account",
		Description: "Returns what to hand `navigator.credentials.create()`, and an identifier " +
			"for the challenge this server is now waiting for. Nothing is created yet: an " +
			"abandoned ceremony leaves a row that expires in two minutes and no account.",
		Tags: []string{"Accounts"},
	}), a.beginSignup)

	huma.Register(api, huma.Operation{
		OperationID: "finish-signup",
		Method:      http.MethodPost,
		Path:        "/v1/accounts/register/finish",
		Summary:     "Finish making an account",
		Description: "Verifies the passkey, creates the account and signs the device in. The " +
			"recovery codes come back **once** and cannot be recovered afterwards — the " +
			"same promise the doléance deletion token makes, for the same reason.",
		Tags: []string{"Accounts"},
	}, a.finishSignup)

	huma.Register(api, bounded(huma.Operation{
		OperationID: "begin-signin",
		Method:      http.MethodPost,
		Path:        "/v1/accounts/login/begin",
		Summary:     "Start signing in",
		Description: "Usernameless: nothing is asked for, because there is nothing to ask for. " +
			"The browser offers whichever passkeys it holds for this register and the " +
			"answer says which account it was.",
		Tags: []string{"Accounts"},
	}), a.beginSignin)

	huma.Register(api, huma.Operation{
		OperationID: "finish-signin",
		Method:      http.MethodPost,
		Path:        "/v1/accounts/login/finish",
		Summary:     "Finish signing in",
		Tags:        []string{"Accounts"},
	}, a.finishSignin)

	huma.Register(api, bounded(huma.Operation{
		OperationID: "use-recovery-code",
		Method:      http.MethodPost,
		Path:        "/v1/accounts/recover",
		Summary:     "Sign in with a recovery code",
		Description: "For somebody whose every passkey is gone. The code is single-use, and " +
			"the answer to a wrong one says nothing about whether it was ever real.",
		Tags: []string{"Accounts"},
	}), a.useRecoveryCode)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "read-account",
		Method:      http.MethodGet,
		Path:        "/v1/accounts/me",
		Summary:     "This account",
		Tags:        []string{"Accounts"},
	}), a.readAccount)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "rename-account",
		Method:      http.MethodPatch,
		Path:        "/v1/accounts/me",
		Summary:     "Change the name this account goes by",
		Description: "Free text, never verified, and the only thing an account holds about " +
			"the person behind it.",
		Tags: []string{"Accounts"},
	}), a.renameAccount)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "delete-account",
		Method:      http.MethodDelete,
		Path:        "/v1/accounts/me",
		Summary:     "Erase this account",
		Description: "Real deletion: passkeys, recovery codes, sessions, push endpoints, " +
			"notifications and the messages this account wrote to groups. Memberships and " +
			"intents stay with the reference removed, so a past attendance reads as " +
			"anonymous and a group's history stays accurate.",
		Tags: []string{"Accounts"},
	}), a.deleteAccount)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "sign-out",
		Method:      http.MethodPost,
		Path:        "/v1/accounts/me/signout",
		Summary:     "Sign this device out",
		Tags:        []string{"Accounts"},
	}), a.signOut)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "list-devices",
		Method:      http.MethodGet,
		Path:        "/v1/accounts/me/devices",
		Summary:     "The passkeys, sessions and notified devices on this account",
		Description: "Three lists, not one. A synced passkey signs somebody in on a device " +
			"that has never been asked for notification permission, so \"has a passkey\", " +
			"\"is signed in\" and \"can be notified here\" are different facts about " +
			"different devices.",
		Tags: []string{"Accounts"},
	}), a.listDevices)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "begin-add-passkey",
		Method:      http.MethodPost,
		Path:        "/v1/accounts/me/passkeys/begin",
		Summary:     "Start adding another passkey",
		Description: "A second passkey is the recovery story, not a convenience: there is no " +
			"address to mail a reset to, so a device-bound passkey that exists in one " +
			"place is an account one dropped phone away from being unreachable.",
		Tags: []string{"Accounts"},
	}), a.beginAddPasskey)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "finish-add-passkey",
		Method:      http.MethodPost,
		Path:        "/v1/accounts/me/passkeys/finish",
		Summary:     "Finish adding another passkey",
		Tags:        []string{"Accounts"},
	}), a.finishAddPasskey)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "rename-passkey",
		Method:      http.MethodPatch,
		Path:        "/v1/accounts/me/passkeys/{credential}",
		Summary:     "Rename a passkey",
		Tags:        []string{"Accounts"},
	}), a.renamePasskey)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "delete-passkey",
		Method:      http.MethodDelete,
		Path:        "/v1/accounts/me/passkeys/{credential}",
		Summary:     "Remove a passkey",
		Description: "Never the last one. An account with no passkeys is one nobody can ever " +
			"sign into again, including its owner — removing the last is deleting the " +
			"account, which is a different request.",
		Tags: []string{"Accounts"},
	}), a.deletePasskey)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "revoke-session",
		Method:      http.MethodDelete,
		Path:        "/v1/accounts/me/sessions/{session}",
		Summary:     "Sign another device out",
		Tags:        []string{"Accounts"},
	}), a.revokeSession)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "reissue-recovery-codes",
		Method:      http.MethodPost,
		Path:        "/v1/accounts/me/recovery-codes",
		Summary:     "Issue a fresh set of recovery codes",
		Description: "Replaces the old set rather than adding to it: a sheet somebody has " +
			"decided to replace must stop working, or regenerating them achieves nothing.",
		Tags: []string{"Accounts"},
	}), a.reissueRecoveryCodes)
}

// ceremonyOptions is what a browser is handed to run a ceremony.
type ceremonyOptions struct {
	Body struct {
		// Ceremony identifies the challenge this server is waiting for. It is
		// handed back with the answer; it authorises nothing on its own.
		Ceremony string `json:"ceremony"`

		// Options is the WebAuthn request, verbatim, for
		// `navigator.credentials`.
		Options json.RawMessage `json:"options"`
	}
}

// SignedInOutput is a session, handed to the frontend to put in a cookie.
type SignedInOutput struct {
	Body struct {
		Session   string    `json:"session"`
		ExpiresAt time.Time `json:"expires_at"`

		Account struct {
			ID   string `json:"id"`
			Name string `json:"name,omitempty"`
		} `json:"account"`

		// RecoveryCodes are returned only when they have just been issued —
		// at signup, and when somebody asks for a fresh set. There is no
		// endpoint that returns them again, because nothing here can: only
		// their hashes are kept.
		RecoveryCodes []string `json:"recovery_codes,omitempty"`
	}
}

// SignupBeginInput is somebody starting to make an account.
type SignupBeginInput struct {
	Body struct {
		// Name is what they want to be called inside a group. Optional, and
		// nothing here pushes for it: an account with no name is a perfectly
		// ordinary account, which is the whole posture of this register.
		Name string `json:"name,omitempty"`
	}
}

func (a *API) beginSignup(ctx context.Context, in *SignupBeginInput) (*ceremonyOptions, error) {
	if a.passkeys == nil {
		return nil, huma.Error503ServiceUnavailable("accounts are not available on this installation")
	}

	name, err := cleanName(in.Body.Name)
	if err != nil {
		return nil, err
	}
	// # A name is required, and this is where that holds
	//
	// It is the only thing an account carries — no address, no password,
	// nothing else — and it is what a group's member table shows and what a
	// message to a group is signed with. An account without one is a row other
	// people cannot address, and the member list beside it reads as a gap
	// rather than a person.
	//
	// Checked after cleaning, not before: a name of nothing but whitespace, or
	// one that sanitation empties, has to fail the same way as an empty field
	// rather than creating an account called "".
	if name == "" {
		return nil, huma.Error422UnprocessableEntity(
			"an account needs a name: it is the only thing a group learns about somebody")
	}

	handle := make([]byte, handleBytes)
	if _, err := rand.Read(handle); err != nil {
		log.Error().Err(err).Msg("cannot generate a user handle")
		return nil, huma.Error500InternalServerError("cannot start a registration")
	}

	// Not persisted. The account is created by the second request, from the
	// handle carried in the challenge state — so an abandoned ceremony leaves
	// a ceremony row that expires in two minutes and nothing else.
	account := &models.Account{Handle: handle, Name: name}

	creation, state, err := a.passkeys.BeginRegistration(account)
	if err != nil {
		log.Error().Err(err).Msg("cannot begin a registration")
		return nil, huma.Error500InternalServerError("cannot start a registration")
	}

	// The handle and the name go with the challenge, because the account does
	// not exist yet and they cannot be carried by the client: a browser that
	// chose its own handle could attach a passkey to somebody else's account.
	pending, err := json.Marshal(pendingAccount{Handle: account.Handle, Name: account.Name})
	if err != nil {
		log.Error().Err(err).Msg("cannot record a pending account")
		return nil, huma.Error500InternalServerError("cannot start a registration")
	}

	ceremony, err := a.store.BeginCeremony(ctx, ceremonySignup, "", state, pending)
	if err != nil {
		log.Error().Err(err).Msg("cannot store a challenge")
		return nil, huma.Error500InternalServerError("cannot start a registration")
	}

	return ceremonyAnswer(ceremony.ID, creation.Response)
}

// Ceremony purposes. A challenge issued for one cannot be finished as
// another: they are different questions, and the answer to "prove you hold a
// key" is not an answer to "make me a key".
const (
	ceremonySignup     = "signup"
	ceremonySignin     = "signin"
	ceremonyAddPasskey = "add-passkey"
	ceremonyLinkDevice = "link-device"
)

// pendingAccount is an account that does not exist yet, kept beside the
// challenge that will create it.
type pendingAccount struct {
	Handle []byte `json:"handle"`
	Name   string `json:"name"`
}

// ceremonyAnswer renders the options for the browser.
func ceremonyAnswer(id string, options any) (*ceremonyOptions, error) {
	encoded, err := json.Marshal(options)
	if err != nil {
		log.Error().Err(err).Msg("cannot encode the ceremony options")
		return nil, huma.Error500InternalServerError("cannot start the ceremony")
	}

	out := &ceremonyOptions{}
	out.Body.Ceremony = id
	out.Body.Options = encoded
	return out, nil
}

// CeremonyAnswerInput presents a browser's answer to a challenge.
type CeremonyAnswerInput struct {
	Body struct {
		Ceremony string `json:"ceremony"`

		// Credential is the browser's answer, verbatim. Passed through
		// untouched: it is a signed structure, and anything this service
		// reshaped on the way through would be something the signature no
		// longer covers.
		Credential json.RawMessage `json:"credential"`

		// DeviceLabel is what the browser says it is. A prompt for a person
		// reading their own device list, never a fact.
		DeviceLabel string `json:"device_label,omitempty"`
	}
}

func (a *API) finishSignup(ctx context.Context, in *CeremonyAnswerInput) (*SignedInOutput, error) {
	if a.passkeys == nil {
		return nil, huma.Error503ServiceUnavailable("accounts are not available on this installation")
	}

	ceremony, err := a.store.FinishCeremony(ctx, in.Body.Ceremony, ceremonySignup)
	if errors.Is(err, store.ErrCeremonyNotFound) {
		return nil, huma.Error422UnprocessableEntity("that registration has expired; start again")
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot read a challenge")
		return nil, huma.Error500InternalServerError("cannot finish the registration")
	}

	var pending pendingAccount
	if err := json.Unmarshal(ceremony.Subject, &pending); err != nil {
		log.Error().Err(err).Msg("a signup ceremony carried no account")
		return nil, huma.Error500InternalServerError("cannot finish the registration")
	}

	account := &models.Account{Handle: pending.Handle, Name: pending.Name}
	credential, err := a.passkeys.FinishRegistration(account, ceremony.Data, in.Body.Credential)
	if err != nil {
		log.Debug().Err(err).Msg("a registration was refused")
		return nil, huma.Error422UnprocessableEntity("that passkey could not be accepted")
	}
	credential.Name = deviceLabel(in.Body.DeviceLabel)

	if err := a.store.CreateAccount(ctx, account, &credential); err != nil {
		log.Error().Err(err).Msg("cannot create an account")
		return nil, huma.Error500InternalServerError("cannot finish the registration")
	}

	codes, err := a.issueRecoveryCodes(ctx, account.ID)
	if err != nil {
		// The account exists and works. Recovery codes that could not be
		// written are worth reporting and not worth refusing a signup over —
		// the account page offers a fresh set at any time.
		log.Error().Err(err).Str("account", account.ID).
			Msg("cannot issue recovery codes")
	}

	out, err := a.signIn(ctx, *account, in.Body.DeviceLabel)
	if err != nil {
		return nil, err
	}
	out.Body.RecoveryCodes = codes

	log.Info().Str("account", account.ID).Msg("an account was created")
	return out, nil
}

// SigninBeginInput starts a sign-in. It carries nothing, and that is the
// point: there is no identifier to ask for.
type SigninBeginInput struct {
	Body struct{}
}

func (a *API) beginSignin(ctx context.Context, _ *SigninBeginInput) (*ceremonyOptions, error) {
	if a.passkeys == nil {
		return nil, huma.Error503ServiceUnavailable("accounts are not available on this installation")
	}

	assertion, state, err := a.passkeys.BeginLogin()
	if err != nil {
		log.Error().Err(err).Msg("cannot begin a sign-in")
		return nil, huma.Error500InternalServerError("cannot start the sign-in")
	}

	ceremony, err := a.store.BeginCeremony(ctx, ceremonySignin, "", state, nil)
	if err != nil {
		log.Error().Err(err).Msg("cannot store a challenge")
		return nil, huma.Error500InternalServerError("cannot start the sign-in")
	}
	return ceremonyAnswer(ceremony.ID, assertion.Response)
}

func (a *API) finishSignin(ctx context.Context, in *CeremonyAnswerInput) (*SignedInOutput, error) {
	if a.passkeys == nil {
		return nil, huma.Error503ServiceUnavailable("accounts are not available on this installation")
	}

	ceremony, err := a.store.FinishCeremony(ctx, in.Body.Ceremony, ceremonySignin)
	if errors.Is(err, store.ErrCeremonyNotFound) {
		return nil, huma.Error422UnprocessableEntity("that sign-in has expired; try again")
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot read a challenge")
		return nil, huma.Error500InternalServerError("cannot finish the sign-in")
	}

	assertion, err := a.passkeys.FinishLogin(ceremony.Data, in.Body.Credential, a.lookupAccount(ctx))
	if err != nil {
		log.Debug().Err(err).Msg("a sign-in was refused")
		return nil, huma.Error401Unauthorized("that passkey was not accepted")
	}

	// Recorded, not enforced. A counter that went backwards means two things
	// are using one credential — but every synced passkey reports zero for
	// ever, so refusing a stalled counter would lock out most of the people
	// this flow exists for.
	err = a.store.RecordCredentialUse(ctx, assertion.CredentialID,
		assertion.SignCount, assertion.BackedUp)
	if err != nil {
		log.Error().Err(err).Msg("cannot record a credential's use")
	}

	return a.signIn(ctx, *assertion.Account, in.Body.DeviceLabel)
}

// lookupAccount finds the account an assertion belongs to.
//
// By handle first, because a discoverable credential returns one; by
// credential identifier as the fallback, for an authenticator that does not.
func (a *API) lookupAccount(ctx context.Context) passkey.Lookup {
	return func(rawID, handle []byte) (*models.Account, error) {
		if len(handle) > 0 {
			account, err := a.store.AccountByHandle(ctx, handle)
			if err == nil {
				return &account, nil
			}
			if !errors.Is(err, store.ErrAccountNotFound) {
				return nil, err
			}
		}

		account, err := a.store.AccountByCredential(ctx, rawID)
		if err != nil {
			return nil, err
		}
		return &account, nil
	}
}

// RecoveryInput presents a recovery code.
type RecoveryInput struct {
	Body struct {
		Code        string `json:"code"`
		DeviceLabel string `json:"device_label,omitempty"`
	}
}

func (a *API) useRecoveryCode(ctx context.Context, in *RecoveryInput) (*SignedInOutput, error) {
	account, err := a.store.RedeemRecoveryCode(ctx, token.HashRecovery(in.Body.Code))
	if errors.Is(err, store.ErrAccountNotFound) {
		// Wrong, spent and never issued are one answer. Telling them apart
		// would say whether a code had ever been real, which is exactly what
		// somebody working through guesses wants to know.
		return nil, huma.Error401Unauthorized("that code is not valid")
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot redeem a recovery code")
		return nil, huma.Error500InternalServerError("cannot sign in")
	}

	log.Info().Str("account", account.ID).Msg("an account was recovered with a code")
	return a.signIn(ctx, account, in.Body.DeviceLabel)
}

// signIn issues a session for a device.
func (a *API) signIn(ctx context.Context, account models.Account, label string) (*SignedInOutput, error) {
	value, hash, err := token.New()
	if err != nil {
		log.Error().Err(err).Msg("cannot issue a session token")
		return nil, huma.Error500InternalServerError("cannot sign in")
	}

	session, err := a.store.CreateSession(ctx, account.ID, hash, deviceLabel(label))
	if err != nil {
		log.Error().Err(err).Msg("cannot create a session")
		return nil, huma.Error500InternalServerError("cannot sign in")
	}
	if err := a.store.TouchAccount(ctx, account.ID); err != nil {
		log.Error().Err(err).Msg("cannot record a sign-in")
	}

	out := &SignedInOutput{}
	out.Body.Session = value
	out.Body.ExpiresAt = session.ExpiresAt
	out.Body.Account.ID = account.ID
	out.Body.Account.Name = account.Name
	return out, nil
}

// issueRecoveryCodes replaces an account's codes and returns them once.
func (a *API) issueRecoveryCodes(ctx context.Context, accountID string) ([]string, error) {
	values := make([]string, 0, models.RecoveryCodeCount)
	hashes := make([]string, 0, models.RecoveryCodeCount)
	for range models.RecoveryCodeCount {
		value, hash, err := token.Recovery()
		if err != nil {
			return nil, err
		}
		values = append(values, value)
		hashes = append(hashes, hash)
	}
	if err := a.store.StoreRecoveryCodes(ctx, accountID, hashes); err != nil {
		return nil, err
	}
	return values, nil
}

// AccountOutput is an account as its owner sees it.
type AccountOutput struct {
	Body struct {
		ID   string `json:"id"`
		Name string `json:"name,omitempty"`

		Passkeys int `json:"passkeys"`

		// RecoveryCodesLeft is how many are unspent, which is the number
		// worth showing: a sheet down to its last code should be replaced.
		RecoveryCodesLeft int64 `json:"recovery_codes_left"`

		// Unread is the number on the bell.
		Unread int64 `json:"unread"`

		// PushConfigured says whether this installation can send a push at
		// all, so the page can offer permission honestly rather than asking
		// for something it could never use.
		PushConfigured bool `json:"push_configured"`

		LastSeenAt time.Time `json:"last_seen_at"`
	}
}

func (a *API) readAccount(ctx context.Context, _ *struct{}) (*AccountOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	credentials, err := a.store.ListCredentials(ctx, who.Account.ID)
	if err != nil {
		log.Error().Err(err).Msg("cannot read an account's passkeys")
		return nil, huma.Error500InternalServerError("cannot read the account")
	}
	left, err := a.store.CountRecoveryCodes(ctx, who.Account.ID)
	if err != nil {
		log.Error().Err(err).Msg("cannot count recovery codes")
	}
	unread, err := a.store.CountUnread(ctx, who.Account.ID)
	if err != nil {
		log.Error().Err(err).Msg("cannot count unread notifications")
	}

	out := &AccountOutput{}
	out.Body.ID = who.Account.ID
	out.Body.Name = who.Account.Name
	out.Body.Passkeys = len(credentials)
	out.Body.RecoveryCodesLeft = left
	out.Body.Unread = unread
	out.Body.PushConfigured = a.push != nil
	out.Body.LastSeenAt = who.Account.LastSeenAt
	return out, nil
}

// RenameAccountInput changes the name an account goes by.
type RenameAccountInput struct {
	Body struct {
		Name string `json:"name"`
	}
}

func (a *API) renameAccount(ctx context.Context, in *RenameAccountInput) (*AccountOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	name, err := cleanName(in.Body.Name)
	if err != nil {
		return nil, err
	}
	if err := a.store.SetAccountName(ctx, who.Account.ID, name); err != nil {
		log.Error().Err(err).Msg("cannot rename an account")
		return nil, huma.Error500InternalServerError("cannot save the name")
	}

	who.Account.Name = name
	return a.readAccount(ctx, nil)
}

// DoneOutput is an answer with nothing to say beyond having worked.
//
// A body rather than a bare 204, because these are called from a page's own
// script: a fetch that can read `{"done": true}` needs no special case for a
// response with nothing in it.
type DoneOutput struct {
	Body struct {
		Done bool `json:"done"`
	}
}

func done() *DoneOutput {
	out := &DoneOutput{}
	out.Body.Done = true
	return out
}

func (a *API) deleteAccount(ctx context.Context, _ *struct{}) (*DoneOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	if err := a.store.DeleteAccount(ctx, who.Account.ID); err != nil {
		log.Error().Err(err).Str("account", who.Account.ID).Msg("cannot delete an account")
		return nil, huma.Error500InternalServerError("cannot delete the account")
	}

	log.Info().Msg("an account was erased at its owner's request")
	return done(), nil
}

func (a *API) signOut(ctx context.Context, _ *struct{}) (*DoneOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.store.DeleteSessionByToken(ctx, who.TokenHash); err != nil {
		log.Error().Err(err).Msg("cannot sign a device out")
		return nil, huma.Error500InternalServerError("cannot sign out")
	}
	return done(), nil
}

// PasskeyItem is one passkey in its owner's list.
type PasskeyItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	// Synced says whether this passkey survives losing the device. It is the
	// one fact that decides whether somebody urgently needs a second one, so
	// the page shows it rather than leaving everybody to guess.
	Synced bool `json:"synced"`

	AddedAt    time.Time  `json:"added_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// SessionItem is one signed-in device.
type SessionItem struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`

	// Current marks the device asking, so nobody signs themselves out while
	// trying to remove a laptop they left at the office.
	Current bool `json:"current"`

	SignedInAt time.Time `json:"signed_in_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// SubscriptionItem is one device that can be notified.
type SubscriptionItem struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`

	AddedAt    time.Time  `json:"added_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// DevicesOutput is the three lists.
type DevicesOutput struct {
	Body struct {
		Passkeys      []PasskeyItem      `json:"passkeys"`
		Sessions      []SessionItem      `json:"sessions"`
		Subscriptions []SubscriptionItem `json:"subscriptions"`
	}
}

func (a *API) listDevices(ctx context.Context, _ *struct{}) (*DevicesOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	credentials, err := a.store.ListCredentials(ctx, who.Account.ID)
	if err != nil {
		log.Error().Err(err).Msg("cannot read an account's passkeys")
		return nil, huma.Error500InternalServerError("cannot read the devices")
	}
	sessions, err := a.store.ListSessions(ctx, who.Account.ID)
	if err != nil {
		log.Error().Err(err).Msg("cannot read an account's sessions")
		return nil, huma.Error500InternalServerError("cannot read the devices")
	}
	subscriptions, err := a.store.ListSubscriptions(ctx, who.Account.ID)
	if err != nil {
		log.Error().Err(err).Msg("cannot read an account's push subscriptions")
		return nil, huma.Error500InternalServerError("cannot read the devices")
	}

	out := &DevicesOutput{}
	out.Body.Passkeys = make([]PasskeyItem, 0, len(credentials))
	for _, credential := range credentials {
		out.Body.Passkeys = append(out.Body.Passkeys, PasskeyItem{
			ID:         credential.ID,
			Name:       credential.Name,
			Synced:     credential.BackupEligible && credential.BackedUp,
			AddedAt:    credential.CreatedAt,
			LastUsedAt: credential.LastUsedAt,
		})
	}

	out.Body.Sessions = make([]SessionItem, 0, len(sessions))
	for _, session := range sessions {
		out.Body.Sessions = append(out.Body.Sessions, SessionItem{
			ID:         session.ID,
			Label:      session.DeviceLabel,
			Current:    session.ID == who.Session.ID,
			SignedInAt: session.CreatedAt,
			LastSeenAt: session.LastSeenAt,
			ExpiresAt:  session.ExpiresAt,
		})
	}

	out.Body.Subscriptions = make([]SubscriptionItem, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		out.Body.Subscriptions = append(out.Body.Subscriptions, SubscriptionItem{
			ID:         subscription.ID,
			Label:      subscription.Label,
			AddedAt:    subscription.CreatedAt,
			LastUsedAt: subscription.LastUsedAt,
		})
	}
	return out, nil
}

func (a *API) beginAddPasskey(ctx context.Context, _ *struct{}) (*ceremonyOptions, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if a.passkeys == nil {
		return nil, huma.Error503ServiceUnavailable("accounts are not available on this installation")
	}

	account, err := a.store.AccountWithCredentials(ctx, who.Account.ID)
	if err != nil {
		log.Error().Err(err).Msg("cannot read an account")
		return nil, huma.Error500InternalServerError("cannot start the registration")
	}

	creation, state, err := a.passkeys.BeginRegistration(&account)
	if err != nil {
		log.Error().Err(err).Msg("cannot begin a registration")
		return nil, huma.Error500InternalServerError("cannot start the registration")
	}

	ceremony, err := a.store.BeginCeremony(ctx, ceremonyAddPasskey, account.ID, state, nil)
	if err != nil {
		log.Error().Err(err).Msg("cannot store a challenge")
		return nil, huma.Error500InternalServerError("cannot start the registration")
	}
	return ceremonyAnswer(ceremony.ID, creation.Response)
}

func (a *API) finishAddPasskey(ctx context.Context, in *CeremonyAnswerInput) (*DevicesOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if a.passkeys == nil {
		return nil, huma.Error503ServiceUnavailable("accounts are not available on this installation")
	}

	ceremony, err := a.store.FinishCeremony(ctx, in.Body.Ceremony, ceremonyAddPasskey)
	if errors.Is(err, store.ErrCeremonyNotFound) {
		return nil, huma.Error422UnprocessableEntity("that registration has expired; start again")
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot read a challenge")
		return nil, huma.Error500InternalServerError("cannot add the passkey")
	}
	// A challenge issued to one account cannot be finished by another, or a
	// second tab signed in as somebody else would attach a passkey to the
	// wrong account.
	if ceremony.AccountID != who.Account.ID {
		return nil, huma.Error422UnprocessableEntity("that registration has expired; start again")
	}

	account, err := a.store.AccountWithCredentials(ctx, who.Account.ID)
	if err != nil {
		log.Error().Err(err).Msg("cannot read an account")
		return nil, huma.Error500InternalServerError("cannot add the passkey")
	}

	credential, err := a.passkeys.FinishRegistration(&account, ceremony.Data, in.Body.Credential)
	if err != nil {
		log.Debug().Err(err).Msg("a registration was refused")
		return nil, huma.Error422UnprocessableEntity("that passkey could not be accepted")
	}
	credential.AccountID = account.ID
	credential.Name = deviceLabel(in.Body.DeviceLabel)

	if err := a.store.AddCredential(ctx, &credential); err != nil {
		log.Error().Err(err).Msg("cannot store a passkey")
		return nil, huma.Error500InternalServerError("cannot add the passkey")
	}

	log.Info().Str("account", account.ID).Msg("a passkey was added to an account")
	return a.listDevices(ctx, nil)
}

// PasskeyPathInput names one of an account's passkeys.
type PasskeyPathInput struct {
	Credential string `path:"credential"`
	Body       struct {
		Name string `json:"name,omitempty"`
	}
}

func (a *API) renamePasskey(ctx context.Context, in *PasskeyPathInput) (*DevicesOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	err = a.store.RenameCredential(ctx, who.Account.ID, in.Credential, deviceLabel(in.Body.Name))
	if errors.Is(err, store.ErrAccountNotFound) {
		return nil, huma.Error404NotFound("no such passkey on this account")
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot rename a passkey")
		return nil, huma.Error500InternalServerError("cannot rename the passkey")
	}
	return a.listDevices(ctx, nil)
}

// CredentialPathInput names one of an account's passkeys, with no body.
type CredentialPathInput struct {
	Credential string `path:"credential"`
}

func (a *API) deletePasskey(ctx context.Context, in *CredentialPathInput) (*DevicesOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	err = a.store.DeleteCredential(ctx, who.Account.ID, in.Credential)
	switch {
	case errors.Is(err, store.ErrLastCredential):
		return nil, huma.Error409Conflict(
			"that is the only passkey on this account: removing it would lock you out for " +
				"good, because there is no address here to send a reset to. Add another " +
				"passkey first, or delete the account.")
	case errors.Is(err, store.ErrAccountNotFound):
		return nil, huma.Error404NotFound("no such passkey on this account")
	case err != nil:
		log.Error().Err(err).Msg("cannot remove a passkey")
		return nil, huma.Error500InternalServerError("cannot remove the passkey")
	}

	log.Info().Str("account", who.Account.ID).Msg("a passkey was removed from an account")
	return a.listDevices(ctx, nil)
}

// SessionPathInput names one of an account's sessions.
type SessionPathInput struct {
	Session string `path:"session"`
}

func (a *API) revokeSession(ctx context.Context, in *SessionPathInput) (*DevicesOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	err = a.store.DeleteSession(ctx, who.Account.ID, in.Session)
	if errors.Is(err, store.ErrSessionNotFound) {
		return nil, huma.Error404NotFound("no such session on this account")
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot revoke a session")
		return nil, huma.Error500InternalServerError("cannot sign that device out")
	}
	return a.listDevices(ctx, nil)
}

func (a *API) reissueRecoveryCodes(ctx context.Context, _ *struct{}) (*SignedInOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	codes, err := a.issueRecoveryCodes(ctx, who.Account.ID)
	if err != nil {
		log.Error().Err(err).Msg("cannot issue recovery codes")
		return nil, huma.Error500InternalServerError("cannot issue the codes")
	}

	// The same shape as a sign-in, minus the session: the codes are shown
	// once and this is the only moment they exist outside a hash.
	out := &SignedInOutput{}
	out.Body.Account.ID = who.Account.ID
	out.Body.Account.Name = who.Account.Name
	out.Body.RecoveryCodes = codes

	log.Info().Str("account", who.Account.ID).Msg("recovery codes were reissued")
	return out, nil
}

// cleanName sanitises and bounds the name an account goes by.
//
// The same bar every other stored string clears: invisible reordering
// characters out — the one edit this project makes to anybody's words — and
// executable content refused rather than cleaned.
func cleanName(name string) (string, error) {
	cleaned, _ := content.Sanitise(name)
	cleaned = trimTo(strings.TrimSpace(cleaned), maxAccountNameRunes)
	if content.ContainsExecutablePayload(cleaned) {
		return "", huma.Error422UnprocessableEntity("that name cannot be saved")
	}
	return cleaned, nil
}

// deviceLabel cleans what a browser says it is.
//
// It is the browser's own claim, shown to a person deciding whether to approve
// a device — so it is cut and sanitised like any other text that arrives from
// outside, and it is never treated as evidence of anything.
func deviceLabel(label string) string {
	cleaned, _ := content.Sanitise(label)
	cleaned = trimTo(strings.TrimSpace(cleaned), maxDeviceLabelRunes)
	if cleaned == "" || content.ContainsExecutablePayload(cleaned) {
		return "a device"
	}
	return cleaned
}
