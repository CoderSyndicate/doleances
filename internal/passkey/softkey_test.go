package passkey

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/fxamacker/cbor/v2"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// TestASoftKeyProducesWhatAVerifierWants.
//
// The shapes below are the ones a verifier reads byte offsets out of. Get any
// of them wrong and registration fails with an error that says nothing about
// which field was short — which is exactly the failure this catches, because
// every seeded local run and every ceremony test hangs off it.
func TestASoftKeyProducesWhatAVerifierWants(t *testing.T) {
	device, err := NewSoftKey("http://localhost:5302")
	if err != nil {
		t.Fatalf("NewSoftKey: %v", err)
	}

	// 32 bytes of relying-party hash, one of flags, four of counter.
	data := device.authenticatorData("localhost", 0x45)
	if len(data) != 37 {
		t.Errorf("authenticator data is %d bytes, want 37", len(data))
	}

	// 16 of AAGUID, two of length, the credential, then the COSE key.
	attested := device.attestedCredentialData()
	if len(attested) <= 16+2+len(device.credentialID) {
		t.Error("the attested credential data carries no public key")
	}
	if got := int(attested[16])<<8 | int(attested[17]); got != len(device.credentialID) {
		t.Errorf("the declared credential length is %d, want %d", got, len(device.credentialID))
	}

	// The coordinates are fixed-width, which is the property a verifier
	// depends on: a 31-byte coordinate is refused by every one of them. It
	// used to need padding by hand, because big.Int trims a leading zero —
	// about one key in 256 — and this asserts the answer rather than the
	// helper that no longer exists.
	var cose map[int]any
	if err := cbor.Unmarshal(attested[16+2+len(device.credentialID):], &cose); err != nil {
		t.Fatalf("the COSE key does not decode: %v", err)
	}
	for _, coordinate := range []int{-2, -3} {
		value, ok := cose[coordinate].([]byte)
		if !ok {
			t.Fatalf("COSE key %d = %T, want bytes", coordinate, cose[coordinate])
		}
		if len(value) != 32 {
			t.Errorf("COSE key %d is %d bytes, want 32", coordinate, len(value))
		}
	}
}

// TestASoftKeyBindsItsAnswerToTheRequest. The challenge and the origin are
// what tie an answer to one request on one site, and both travel in the client
// data the signature covers.
func TestASoftKeyBindsItsAnswerToTheRequest(t *testing.T) {
	device, err := NewSoftKey("http://localhost:5302")
	if err != nil {
		t.Fatalf("NewSoftKey: %v", err)
	}

	answer, err := device.Create(json.RawMessage(
		`{"challenge":"Y2hhbGxlbmdl","rp":{"id":"localhost"},"user":{"id":"YS1oYW5kbGU"}}`))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	response, ok := answer["response"].(map[string]any)
	if !ok {
		t.Fatal("the attestation carries no response")
	}
	for _, field := range []string{"clientDataJSON", "attestationObject"} {
		if value, _ := response[field].(string); value == "" {
			t.Errorf("the attestation is missing %s", field)
		}
	}

	raw, err := base64.RawURLEncoding.DecodeString(response["clientDataJSON"].(string))
	if err != nil {
		t.Fatalf("decode the client data: %v", err)
	}
	var client struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Origin    string `json:"origin"`
	}
	if err := json.Unmarshal(raw, &client); err != nil {
		t.Fatalf("read the client data: %v", err)
	}
	if client.Type != "webauthn.create" {
		t.Errorf("type = %q, want webauthn.create", client.Type)
	}
	if client.Challenge != "Y2hhbGxlbmdl" {
		t.Errorf("challenge = %q, want the one that was asked for", client.Challenge)
	}
	if client.Origin != "http://localhost:5302" {
		t.Errorf("origin = %q, want the configured one", client.Origin)
	}
}

// TestASoftKeyRoundTripsThroughTheRelyingParty is the test that matters: a
// registration this key produces has to be one the relying party accepts, and
// a sign-in it signs has to verify against the key that registration stored.
//
// It exercises `internal/passkey` against itself, which sounds circular and is
// not: the two halves are independent implementations of opposite sides of a
// specification, and everything between them — the CBOR, the byte offsets, the
// COSE key, the signature input — is the specification rather than this
// package's opinion.
func TestASoftKeyRoundTripsThroughTheRelyingParty(t *testing.T) {
	service, err := New(Options{
		ID:          "localhost",
		DisplayName: "doléances",
		Origins:     []string{"http://localhost:5302"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	device, err := NewSoftKey("http://localhost:5302")
	if err != nil {
		t.Fatalf("NewSoftKey: %v", err)
	}

	account := &models.Account{Handle: []byte("a-handle-of-random-bytes"), Name: "Camille"}

	creation, state, err := service.BeginRegistration(account)
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	options, err := json.Marshal(creation.Response)
	if err != nil {
		t.Fatalf("encode the options: %v", err)
	}

	attestation, err := device.Create(options)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	encoded, err := json.Marshal(attestation)
	if err != nil {
		t.Fatalf("encode the attestation: %v", err)
	}

	credential, err := service.FinishRegistration(account, state, encoded)
	if err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}
	if len(credential.PublicKey) == 0 {
		t.Error("the stored credential has no public key")
	}

	// Now sign in with it, which is what proves the stored key verifies.
	credential.AccountID = "an-account"
	account.Credentials = append(account.Credentials, credential)

	assertion, loginState, err := service.BeginLogin()
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	loginOptions, err := json.Marshal(assertion.Response)
	if err != nil {
		t.Fatalf("encode the assertion options: %v", err)
	}

	signed, err := device.Assert(loginOptions)
	if err != nil {
		t.Fatalf("Assert: %v", err)
	}
	encodedAssertion, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("encode the assertion: %v", err)
	}

	result, err := service.FinishLogin(loginState, encodedAssertion,
		func(rawID, handle []byte) (*models.Account, error) { return account, nil })
	if err != nil {
		t.Fatalf("FinishLogin: %v", err)
	}
	if result.Account != account {
		t.Error("the sign-in resolved to the wrong account")
	}
	if !bytes.Equal(result.CredentialID, device.CredentialID()) {
		t.Error("the sign-in named the wrong credential")
	}
}

// TestAnAssertionForAnotherOriginIsRefused. The origin is what stops a passkey
// for this register being usable by a site that merely looks like it, and it
// is checked rather than trusted.
func TestAnAssertionForAnotherOriginIsRefused(t *testing.T) {
	service, err := New(Options{ID: "localhost", Origins: []string{"http://localhost:5302"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The same relying party id, a different origin: the shape of a site
	// serving a convincing copy of this one.
	device, err := NewSoftKey("https://doleances.example.org")
	if err != nil {
		t.Fatalf("NewSoftKey: %v", err)
	}

	account := &models.Account{Handle: []byte("a-handle")}
	creation, state, err := service.BeginRegistration(account)
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	options, _ := json.Marshal(creation.Response)

	attestation, err := device.Create(options)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	encoded, _ := json.Marshal(attestation)

	if _, err := service.FinishRegistration(account, state, encoded); err == nil {
		t.Fatal("a registration from another origin was accepted")
	} else if !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want ErrRefused", err)
	}
}

// TestASoftKeyRemembersItsHandle.
//
// The handle is what a discoverable sign-in resolves an account by: the
// authenticator stores it at registration and hands it back with every
// assertion. A key that forgot it would produce assertions that verify and
// still fail, because nothing could say whose they were — which is exactly the
// failure this caught when the handle was a caller's argument.
func TestASoftKeyRemembersItsHandle(t *testing.T) {
	device, err := NewSoftKey("http://localhost:5302")
	if err != nil {
		t.Fatalf("NewSoftKey: %v", err)
	}

	if _, err := device.Create(json.RawMessage(
		`{"challenge":"Y2hhbGxlbmdl","rp":{"id":"localhost"},"user":{"id":"YS1oYW5kbGU"}}`)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	answer, err := device.Assert(json.RawMessage(
		`{"challenge":"YW5vdGhlcg","rpId":"localhost"}`))
	if err != nil {
		t.Fatalf("Assert: %v", err)
	}

	response := answer["response"].(map[string]any)
	handle, present := response["userHandle"].(string)
	if !present || handle != "YS1oYW5kbGU" {
		t.Errorf("userHandle = %q, want the one from registration", handle)
	}
}
