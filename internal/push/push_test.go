package push

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// answering is a push service that answers with one status and records what it
// was asked.
type answering struct {
	status int
	last   *http.Request
	body   []byte
}

func (a *answering) RoundTrip(req *http.Request) (*http.Response, error) {
	a.last = req
	if req.Body != nil {
		a.body, _ = io.ReadAll(req.Body)
	}
	return &http.Response{
		StatusCode: a.status,
		Status:     http.StatusText(a.status),
		Body:       http.NoBody,
		Header:     http.Header{},
	}, nil
}

func sender(t *testing.T, status int) (*Sender, *answering) {
	t.Helper()

	// A real VAPID pair, generated for this test. It signs nothing anybody
	// verifies here — the point is that the encryption and signing path runs
	// rather than being skipped.
	private, public, err := GenerateKeys()
	if err != nil {
		t.Fatalf("GenerateKeys: %v", err)
	}

	built, err := New(Options{
		PublicKey:  public,
		PrivateKey: private,
		Subject:    "mailto:operator@example.org",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	service := &answering{status: status}
	built.client.Transport = service
	return built, service
}

// device is a browser's subscription.
//
// The public key is generated rather than pasted from a specification example:
// it has to be a real point on P-256, because the payload is genuinely encrypted
// to it and the library refuses anything that is not.
func device(t *testing.T) Device {
	t.Helper()

	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate a subscription key: %v", err)
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("generate an auth secret: %v", err)
	}

	return Device{
		Endpoint: "https://push.example.org/send/abc",
		// The uncompressed point, which is what `PushSubscription.getKey`
		// returns.
		P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
		Auth:   base64.RawURLEncoding.EncodeToString(secret),
	}
}

// TestAGoneEndpointIsNamedRatherThanCountedAsAFailure.
//
// It is the whole of the delivery-failure story: a push service answering 404 or
// 410 means the browser is gone, and the caller's only correct response is to
// delete the subscription. A handler reading raw status codes is a handler that
// will eventually retry one.
func TestAGoneEndpointIsNamedRatherThanCountedAsAFailure(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusGone} {
		built, _ := sender(t, status)
		err := built.Send(context.Background(), device(t), Payload{Title: "something"})
		if !errors.Is(err, ErrGone) {
			t.Errorf("status %d gave %v, want ErrGone", status, err)
		}
	}
}

// TestAnOrdinaryFailureIsNotGone. A push service having a bad afternoon is not a
// browser that has been uninstalled, and deleting a subscription over a 500
// would silently stop notifying somebody for ever.
func TestAnOrdinaryFailureIsNotGone(t *testing.T) {
	built, _ := sender(t, http.StatusInternalServerError)

	err := built.Send(context.Background(), device(t), Payload{Title: "something"})
	if err == nil {
		t.Fatal("a 500 was reported as a success")
	}
	if errors.Is(err, ErrGone) {
		t.Error("a 500 was treated as a gone endpoint")
	}
}

// TestASuccessIsASuccess, for the two statuses a push service actually uses.
func TestASuccessIsASuccess(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusOK} {
		built, _ := sender(t, status)
		if err := built.Send(context.Background(), device(t), Payload{Title: "x"}); err != nil {
			t.Errorf("status %d gave %v, want success", status, err)
		}
	}
}

// TestThePayloadIsEncryptedAndSigned.
//
// Two properties, in one place because they are one request. The body must not
// contain the notification in the clear — a push travels through a third party,
// and the whole reason the browser's keys exist is that the service carries the
// message without being able to read it. And the request must carry VAPID
// authorisation, which is what stops anybody else sending to that endpoint.
func TestThePayloadIsEncryptedAndSigned(t *testing.T) {
	built, service := sender(t, http.StatusCreated)

	err := built.Send(context.Background(), device(t), Payload{
		Title: "Collectif Citoyen",
		Body:  "Dominique: la réunion de mardi tient-elle ?",
		URL:   "https://doleances.example.org/groups/abc",
		Tag:   "group_message:abc",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	for _, secret := range []string{"Collectif Citoyen", "Dominique", "réunion"} {
		if strings.Contains(string(service.body), secret) {
			t.Errorf("the request body carries %q in the clear", secret)
		}
	}
	if authorization := service.last.Header.Get("Authorization"); !strings.Contains(authorization, "vapid") {
		t.Errorf("Authorization = %q, want a VAPID credential", authorization)
	}
	// The tag travels as the Topic header, which is what lets a push service
	// replace a pending message rather than queue a second one.
	if topic := service.last.Header.Get("Topic"); topic != "group_message:abc" {
		t.Errorf("Topic = %q, want the payload's tag", topic)
	}
	if encoding := service.last.Header.Get("Content-Encoding"); encoding != "aes128gcm" {
		t.Errorf("Content-Encoding = %q, want the encrypted form", encoding)
	}
}

// TestNoKeysIsNotAFailureState.
//
// An installation with no VAPID keys still writes every notification to the
// list, which is the channel. Refusing to start would be treating the tap on the
// shoulder as the message.
func TestNoKeysIsNotAFailureState(t *testing.T) {
	for _, opts := range []Options{
		{},
		{PublicKey: "a-key"},
		{PrivateKey: "a-key"},
	} {
		if _, err := New(opts); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("New(%+v) = %v, want ErrNotConfigured", opts, err)
		}
	}
}

// TestThePayloadCarriesOnlyWhatAServiceWorkerNeeds. It is deliberately thin: a
// title, a line, a door and a tag. Anything else would be data travelling
// through a third party for no reason.
func TestThePayloadCarriesOnlyWhatAServiceWorkerNeeds(t *testing.T) {
	encoded, err := json.Marshal(Payload{Title: "t", Body: "b", URL: "/u", Tag: "g"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(fields) != 4 {
		t.Errorf("the payload has %d fields: %v", len(fields), fields)
	}
	for _, want := range []string{"title", "body", "url", "tag"} {
		if _, ok := fields[want]; !ok {
			t.Errorf("the payload is missing %q", want)
		}
	}
}
