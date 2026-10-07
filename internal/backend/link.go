package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/store"
	"github.com/CoderSyndicate/doleances/internal/token"
)

// Device linking exists because the built-in cross-device flows do not reach
// far enough.
//
// A synced passkey crosses devices inside one ecosystem — Apple to Apple,
// Google to Google — and the browser's own QR flow needs Bluetooth proximity
// between the phone and the computer. Somebody with an Android phone and a Mac
// has neither, and without this they would need a second account and would
// lose their groups in the process.
//
// # Two things make it safe, and both are needed
//
// The token is single-use and lives two minutes, so a photographed screen is
// worth very little. And the **old** device confirms the new one before
// anything is attached: somebody who tricks a person into scanning their code
// still has to get that person to approve a browser they do not recognise.
// Either half alone would be a relay attack waiting to happen.
func (a *API) registerLinkRoutes(api huma.API) {
	huma.Register(api, authenticated(huma.Operation{
		OperationID: "open-device-link",
		Method:      http.MethodPost,
		Path:        "/v1/accounts/me/link",
		Summary:     "Offer this account to another device",
		Description: "Returns a single-use token to show as a QR code. It lives two minutes " +
			"and authorises nothing on its own: the new device claims it, and this device " +
			"has to approve what claimed it.",
		Tags: []string{"Accounts"},
	}), a.openLink)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "read-device-link",
		Method:      http.MethodGet,
		Path:        "/v1/accounts/me/link",
		Summary:     "What is waiting to be approved",
		Description: "The label is the new device's own claim about itself. It is a prompt " +
			"for the person reading it, never a fact — a browser can say anything.",
		Tags: []string{"Accounts"},
	}), a.readLink)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "approve-device-link",
		Method:      http.MethodPost,
		Path:        "/v1/accounts/me/link/{link}/approve",
		Summary:     "Approve the waiting device",
		Tags:        []string{"Accounts"},
	}), a.approveLink)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "refuse-device-link",
		Method:      http.MethodDelete,
		Path:        "/v1/accounts/me/link/{link}",
		Summary:     "Refuse the waiting device",
		Description: "The case that matters: somebody who did not start a linking sees a " +
			"prompt for a browser they do not recognise, and says no.",
		Tags: []string{"Accounts"},
	}), a.refuseLink)

	huma.Register(api, bounded(huma.Operation{
		OperationID: "claim-device-link",
		Method:      http.MethodPost,
		Path:        "/v1/accounts/link/claim",
		Summary:     "Present a link token from a new device",
		Description: "Learns only that the token was real, which the caller already knew. " +
			"Nothing is attached and no session is issued until the other device approves.",
		Tags: []string{"Accounts"},
	}), a.claimLink)

	huma.Register(api, huma.Operation{
		OperationID: "poll-device-link",
		Method:      http.MethodPost,
		Path:        "/v1/accounts/link/status",
		Summary:     "Has the other device approved yet",
		Tags:        []string{"Accounts"},
	}, a.pollLink)

	huma.Register(api, bounded(huma.Operation{
		OperationID: "begin-link-passkey",
		Method:      http.MethodPost,
		Path:        "/v1/accounts/link/register/begin",
		Summary:     "Start making a passkey on the approved device",
		Tags:        []string{"Accounts"},
	}), a.beginLinkPasskey)

	huma.Register(api, huma.Operation{
		OperationID: "finish-link-passkey",
		Method:      http.MethodPost,
		Path:        "/v1/accounts/link/register/finish",
		Summary:     "Finish making a passkey on the approved device",
		Description: "Spends the token, attaches the passkey and signs the new device in. " +
			"This is the step that cannot be repeated.",
		Tags: []string{"Accounts"},
	}, a.finishLinkPasskey)
}

// LinkOfferOutput is the token to show as a QR code.
type LinkOfferOutput struct {
	Body struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
}

func (a *API) openLink(ctx context.Context, _ *struct{}) (*LinkOfferOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	value, hash, err := token.New()
	if err != nil {
		log.Error().Err(err).Msg("cannot issue a link token")
		return nil, huma.Error500InternalServerError("cannot open the link")
	}

	link, err := a.store.CreateLinkToken(ctx, who.Account.ID, hash)
	if err != nil {
		log.Error().Err(err).Msg("cannot open a device link")
		return nil, huma.Error500InternalServerError("cannot open the link")
	}

	out := &LinkOfferOutput{}
	out.Body.Token = value
	out.Body.ExpiresAt = link.ExpiresAt
	return out, nil
}

// LinkWaitingOutput is what the signed-in device is being asked to approve.
type LinkWaitingOutput struct {
	Body struct {
		// Waiting is false when nothing has claimed a token, which is the
		// ordinary case and not an error.
		Waiting bool `json:"waiting"`

		ID string `json:"id,omitempty"`

		// Label is what the new device says it is. Shown, never believed.
		Label string `json:"label,omitempty"`

		ClaimedAt *time.Time `json:"claimed_at,omitempty"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
	}
}

func (a *API) readLink(ctx context.Context, _ *struct{}) (*LinkWaitingOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	out := &LinkWaitingOutput{}
	link, err := a.store.PendingLink(ctx, who.Account.ID)
	if errors.Is(err, store.ErrLinkNotFound) {
		return out, nil
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot read a pending device link")
		return nil, huma.Error500InternalServerError("cannot read the link")
	}

	out.Body.Waiting = true
	out.Body.ID = link.ID
	out.Body.Label = link.ClaimLabel
	out.Body.ClaimedAt = link.ClaimedAt
	out.Body.ExpiresAt = &link.ExpiresAt
	return out, nil
}

// LinkPathInput names one pending link.
type LinkPathInput struct {
	Link string `path:"link"`
}

func (a *API) approveLink(ctx context.Context, in *LinkPathInput) (*DoneOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	err = a.store.ConfirmLink(ctx, who.Account.ID, in.Link)
	if errors.Is(err, store.ErrLinkNotFound) {
		return nil, huma.Error404NotFound("nothing is waiting to be approved")
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot approve a device link")
		return nil, huma.Error500InternalServerError("cannot approve the device")
	}

	log.Info().Str("account", who.Account.ID).Msg("a new device was approved")
	return done(), nil
}

func (a *API) refuseLink(ctx context.Context, in *LinkPathInput) (*DoneOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	err = a.store.RejectLink(ctx, who.Account.ID, in.Link)
	if errors.Is(err, store.ErrLinkNotFound) {
		return nil, huma.Error404NotFound("nothing is waiting to be approved")
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot refuse a device link")
		return nil, huma.Error500InternalServerError("cannot refuse the device")
	}

	log.Warn().Str("account", who.Account.ID).Msg("a device link was refused")
	return done(), nil
}

// LinkTokenInput presents a link token from the new device.
type LinkTokenInput struct {
	Body struct {
		Token string `json:"token"`

		// Label is what this browser says it is, shown to whoever approves.
		Label string `json:"label,omitempty"`
	}
}

// LinkStatusOutput is how far the linking has got.
type LinkStatusOutput struct {
	Body struct {
		Claimed   bool `json:"claimed"`
		Confirmed bool `json:"confirmed"`
	}
}

func (a *API) claimLink(ctx context.Context, in *LinkTokenInput) (*LinkStatusOutput, error) {
	link, err := a.store.ClaimLinkToken(ctx, token.Hash(in.Body.Token), deviceLabel(in.Body.Label))
	if errors.Is(err, store.ErrLinkNotFound) {
		// Expired, already claimed and never issued are one answer. Telling
		// them apart would let somebody probe which codes had been real.
		return nil, huma.Error422UnprocessableEntity("that code is not usable")
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot claim a device link")
		return nil, huma.Error500InternalServerError("cannot use that code")
	}

	out := &LinkStatusOutput{}
	out.Body.Claimed = true
	out.Body.Confirmed = link.ConfirmedAt != nil
	return out, nil
}

func (a *API) pollLink(ctx context.Context, in *LinkTokenInput) (*LinkStatusOutput, error) {
	state, err := a.store.LinkProgress(ctx, token.Hash(in.Body.Token))
	if errors.Is(err, store.ErrLinkNotFound) {
		return nil, huma.Error422UnprocessableEntity("that code is not usable")
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot read a device link")
		return nil, huma.Error500InternalServerError("cannot read the link")
	}

	out := &LinkStatusOutput{}
	out.Body.Claimed = state.Claimed
	out.Body.Confirmed = state.Confirmed
	return out, nil
}

func (a *API) beginLinkPasskey(ctx context.Context, in *LinkTokenInput) (*ceremonyOptions, error) {
	if a.passkeys == nil {
		return nil, huma.Error503ServiceUnavailable("accounts are not available on this installation")
	}

	account, err := a.store.ConfirmedLink(ctx, token.Hash(in.Body.Token))
	if errors.Is(err, store.ErrLinkNotFound) {
		return nil, huma.Error422UnprocessableEntity("that code is not usable, or has not been approved")
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot read a device link")
		return nil, huma.Error500InternalServerError("cannot start the registration")
	}

	creation, state, err := a.passkeys.BeginRegistration(&account)
	if err != nil {
		log.Error().Err(err).Msg("cannot begin a registration")
		return nil, huma.Error500InternalServerError("cannot start the registration")
	}

	ceremony, err := a.store.BeginCeremony(ctx, ceremonyLinkDevice, account.ID, state, nil)
	if err != nil {
		log.Error().Err(err).Msg("cannot store a challenge")
		return nil, huma.Error500InternalServerError("cannot start the registration")
	}
	return ceremonyAnswer(ceremony.ID, creation.Response)
}

// LinkFinishInput is the new device's answer, with the token it is spending.
type LinkFinishInput struct {
	Body struct {
		Token    string `json:"token"`
		Ceremony string `json:"ceremony"`

		// Credential is the browser's signed structure, passed through
		// untouched: anything reshaped on the way is something the signature
		// no longer covers.
		Credential json.RawMessage `json:"credential"`

		DeviceLabel string `json:"device_label,omitempty"`
	}
}

func (a *API) finishLinkPasskey(ctx context.Context, in *LinkFinishInput) (*SignedInOutput, error) {
	if a.passkeys == nil {
		return nil, huma.Error503ServiceUnavailable("accounts are not available on this installation")
	}

	ceremony, err := a.store.FinishCeremony(ctx, in.Body.Ceremony, ceremonyLinkDevice)
	if errors.Is(err, store.ErrCeremonyNotFound) {
		return nil, huma.Error422UnprocessableEntity("that registration has expired; start again")
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot read a challenge")
		return nil, huma.Error500InternalServerError("cannot finish the registration")
	}

	// The token is spent here, at the end. Everything before this point can
	// be retried; this cannot, which is what makes a photographed screen
	// worth nothing a second time.
	account, err := a.store.SpendLinkToken(ctx, token.Hash(in.Body.Token))
	if errors.Is(err, store.ErrLinkNotFound) {
		return nil, huma.Error422UnprocessableEntity("that code is not usable, or has not been approved")
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot spend a device link")
		return nil, huma.Error500InternalServerError("cannot finish the registration")
	}
	if ceremony.AccountID != account.ID {
		return nil, huma.Error422UnprocessableEntity("that registration has expired; start again")
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
		return nil, huma.Error500InternalServerError("cannot finish the registration")
	}

	log.Info().Str("account", account.ID).Msg("an account was linked to a new device")
	return a.signIn(ctx, account, in.Body.DeviceLabel)
}
