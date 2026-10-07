package passkey

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// SoftKey is a passkey in software: a test double for an authenticator.
//
// # What it is for
//
// Registration and sign-in are the two most security-sensitive things this
// backend does and the two least visible from a Go test, because everything
// interesting normally happens in a browser. This puts them on the critical
// path of `go test` and of every seeded local run, which is the only way a
// break in them is noticed by anything other than a person trying to sign in.
//
// The alternative was a `--development` route that minted a session out of
// nothing. That would have tested the group endpoints and skipped the ceremony
// entirely — which is the half most worth testing.
//
// # What it is not
//
// **Not a security component, and never on a request path.** It keeps a
// private key in process memory, it asserts a user verification it never
// performed, and its attestation is "none". It proves the protocol is wired up
// correctly; it proves nothing whatever about a real device.
//
// It lives in this package rather than in a test file because two callers need
// it — the backend's own tests and the development seeder — and a copy in each
// is a copy to keep in step.
type SoftKey struct {
	key *ecdsa.PrivateKey

	// origin is what the browser would report — the page's own origin, which
	// the relying party checks against its configuration. A mismatch here is
	// the same refusal a phishing site would get, so it has to be the real
	// public address of the frontend.
	origin string

	credentialID []byte

	// handle is the user handle the relying party asked this key to store,
	// kept from the registration and handed back with every assertion.
	//
	// A real authenticator does exactly this, and it is what makes a
	// usernameless sign-in possible: the browser offers the credentials it
	// holds and the answer says which account each belongs to. A key that
	// forgot it would produce assertions no discoverable login can resolve.
	handle []byte

	// signCount stays at zero on purpose: that is what every synced passkey
	// reports, and the backend must treat a stalled counter as normal rather
	// than as a clone.
	signCount uint32
}

// NewSoftKey makes one, bound to the origin a browser on that page would
// report. The relying party checks it against its own configuration, so a
// mismatch here earns exactly the refusal a phishing site would get.
func NewSoftKey(origin string) (*SoftKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("passkey: generate a key: %w", err)
	}

	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		return nil, fmt.Errorf("passkey: generate a credential id: %w", err)
	}
	return &SoftKey{key: key, origin: origin, credentialID: id}, nil
}

// Ceremony is what the backend hands a browser: an opaque options blob and an
// identifier for the challenge it is now waiting for.
type Ceremony struct {
	Ceremony string          `json:"ceremony"`
	Options  json.RawMessage `json:"options"`
}

// creationOptions is the part of a registration request this needs.
type creationOptions struct {
	Challenge string `json:"challenge"`
	RP        struct {
		ID string `json:"id"`
	} `json:"rp"`
	User struct {
		ID string `json:"id"`
	} `json:"user"`
}

// requestOptions is the part of a sign-in request this needs.
type requestOptions struct {
	Challenge string `json:"challenge"`
	RPID      string `json:"rpId"`
}

// Create answers a registration challenge.
func (a *SoftKey) Create(options json.RawMessage) (map[string]any, error) {
	var parsed creationOptions
	if err := json.Unmarshal(options, &parsed); err != nil {
		return nil, fmt.Errorf("read the creation options: %w", err)
	}

	// Kept, because the relying party will expect it back at every sign-in.
	handle, err := base64.RawURLEncoding.DecodeString(parsed.User.ID)
	if err != nil {
		return nil, fmt.Errorf("passkey: unreadable user handle: %w", err)
	}
	a.handle = handle

	clientData := a.clientData("webauthn.create", parsed.Challenge)

	// AT is set because this carries the new credential; UP and UV say the
	// person was present and verified, which is a claim a real authenticator
	// earns and this one simply makes.
	const flagsAttested = 0x01 | 0x04 | 0x40
	authData := append(a.authenticatorData(parsed.RP.ID, flagsAttested), a.attestedCredentialData()...)

	// "none" attestation: no statement about what kind of authenticator this
	// is. It is what a platform passkey sends by default, and the backend
	// records the type without acting on it — the register has no business
	// refusing somebody's security key because of who made it.
	attestation, err := cbor.Marshal(map[string]any{
		"fmt":      "none",
		"attStmt":  map[string]any{},
		"authData": authData,
	})
	if err != nil {
		return nil, fmt.Errorf("encode the attestation: %w", err)
	}

	return map[string]any{
		"id":    base64.RawURLEncoding.EncodeToString(a.credentialID),
		"rawId": base64.RawURLEncoding.EncodeToString(a.credentialID),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
			"attestationObject": base64.RawURLEncoding.EncodeToString(attestation),
			"transports":        []string{"internal"},
		},
		"clientExtensionResults": map[string]any{},
	}, nil
}

// Assert answers a sign-in challenge.
//
// The user handle comes from the registration rather than from the caller: that
// is where a real authenticator gets it, and a caller that had to supply it
// could supply the wrong one and produce a test that passed for the wrong
// reason.
func (a *SoftKey) Assert(options json.RawMessage) (map[string]any, error) {
	var parsed requestOptions
	if err := json.Unmarshal(options, &parsed); err != nil {
		return nil, fmt.Errorf("read the request options: %w", err)
	}

	clientData := a.clientData("webauthn.get", parsed.Challenge)

	const flagsAsserted = 0x01 | 0x04
	authData := a.authenticatorData(parsed.RPID, flagsAsserted)

	// The signature covers the authenticator's own data and a hash of what the
	// client said — which is what binds an assertion to this challenge, this
	// origin, and nothing else.
	clientHash := sha256.Sum256(clientData)
	signed := sha256.Sum256(append(append([]byte{}, authData...), clientHash[:]...))

	signature, err := ecdsa.SignASN1(rand.Reader, a.key, signed[:])
	if err != nil {
		return nil, fmt.Errorf("sign the assertion: %w", err)
	}

	answer := map[string]any{
		"id":    base64.RawURLEncoding.EncodeToString(a.credentialID),
		"rawId": base64.RawURLEncoding.EncodeToString(a.credentialID),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
			"authenticatorData": base64.RawURLEncoding.EncodeToString(authData),
			"signature":         base64.RawURLEncoding.EncodeToString(signature),
		},
		"clientExtensionResults": map[string]any{},
	}
	if len(a.handle) > 0 {
		// What makes a usernameless sign-in work: the authenticator stores the
		// handle beside the key and hands it back with the credential chosen.
		answer["response"].(map[string]any)["userHandle"] =
			base64.RawURLEncoding.EncodeToString(a.handle)
	}
	return answer, nil
}

// CredentialID is the identifier this key registered under.
func (a *SoftKey) CredentialID() []byte { return a.credentialID }

func (a *SoftKey) clientData(kind, challenge string) []byte {
	// Field order does not matter: the relying party parses this rather than
	// comparing bytes, and the signature covers whatever was sent.
	encoded, _ := json.Marshal(map[string]any{
		"type":        kind,
		"challenge":   challenge,
		"origin":      a.origin,
		"crossOrigin": false,
	})
	return encoded
}

// authenticatorData is the fixed prefix every answer carries.
func (a *SoftKey) authenticatorData(rpID string, flags byte) []byte {
	hash := sha256.Sum256([]byte(rpID))

	data := make([]byte, 0, 37)
	data = append(data, hash[:]...)
	data = append(data, flags)
	data = binary.BigEndian.AppendUint32(data, a.signCount)
	return data
}

// attestedCredentialData carries the new credential and its public key.
func (a *SoftKey) attestedCredentialData() []byte {
	// All zeroes: an AAGUID identifies a model of authenticator, and this is
	// not one. A real platform passkey often sends zeroes too, for privacy.
	aaguid := make([]byte, 16)

	// SEC 1 uncompressed: 0x04 followed by the two 32-byte coordinates, which
	// is where COSE wants them and already the fixed width it requires.
	//
	// Read this way rather than from PublicKey.X and .Y, which Go 1.26
	// deprecated: big.Int trims leading zeroes, so a coordinate that starts
	// with one used to need padding back out by hand — a 31-byte value every
	// verifier rejects, and a bug that shows up on about one key in 256.
	point, err := a.key.PublicKey.Bytes()
	if err != nil {
		// Only reachable with a key this type generated itself.
		panic("softkey: its own public key is invalid: " + err.Error())
	}

	// COSE_Key for an ES256 key: kty=EC2(2), alg=ES256(-7), crv=P-256(1), and
	// the two coordinates. The integer keys are the format, not a choice — a
	// map with string keys here is silently unreadable.
	coseKey, _ := cbor.Marshal(map[int]any{
		1:  2,
		3:  -7,
		-1: 1,
		-2: point[1:33],
		-3: point[33:65],
	})

	data := make([]byte, 0, len(aaguid)+2+len(a.credentialID)+len(coseKey))
	data = append(data, aaguid...)
	data = binary.BigEndian.AppendUint16(data, uint16(len(a.credentialID)))
	data = append(data, a.credentialID...)
	return append(data, coseKey...)
}
