// Package push delivers a notification to a browser.
//
// It is this project's replacement for `internal/mailer`, and it is a
// deliberately smaller thing. A mailbox can be temporarily full, so mail
// needed a retry ladder, a delivery history and a notion of an address going
// inactive; a push endpoint is either there or it is gone, so the whole of the
// failure story here is: send once, and delete the endpoint the service says
// no longer exists.
//
// # What it does and does not know
//
// It knows how to encrypt a payload to a browser's keys and sign it for a push
// service. It does not know what a notification is for, who should get one, or
// whether one has already been sent — the store holds the notification, this
// sends the tap on the shoulder, and keeping the two apart is what makes it
// true that nothing exists only as a push.
package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// ErrGone means the endpoint is not there any more.
//
// The caller's only correct response is to delete the subscription. It is a
// named error rather than a status code because "410 means forget this
// browser" is a rule about this register's data, and a handler reading raw
// status codes is a handler that will eventually retry one.
var ErrGone = errors.New("push: the endpoint no longer exists")

// ErrNotConfigured means no VAPID keys are set, so nothing can be sent.
//
// Not a failure state: an installation with no keys still writes every
// notification to the list, which is the channel. See config.Push.
var ErrNotConfigured = errors.New("push: no VAPID keys configured")

// Device is one browser to deliver to.
type Device struct {
	Endpoint string

	// P256dh and Auth are the browser's own keys. The payload is encrypted to
	// them, so the push service carries the message without being able to read
	// it — which is what keeps a notification about somebody's group private
	// from the company delivering it.
	P256dh string
	Auth   string
}

// Payload is what a service worker receives.
//
// Deliberately thin. It is a title, a line and a door, and it carries no
// content beyond what the notification already says: a push travels through a
// third party's infrastructure, so even encrypted it is not the place for
// anything the register would not put in a notification list.
type Payload struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`

	// URL is where tapping it goes.
	URL string `json:"url,omitempty"`

	// Tag collapses repeats on one screen.
	//
	// This is what keeps *one notification, not a campaign* honest across
	// devices: the rule is one per thing, not one per screen, so every
	// subscribed device buzzing once is correct — but the same event arriving
	// twice on one device is not, and the Notification API's tag is what
	// prevents it.
	Tag string `json:"tag,omitempty"`
}

// Options configures the sender.
type Options struct {
	PublicKey  string
	PrivateKey string

	// Subject is how a push service reaches the operator — a `mailto:` or an
	// `https://` URL. Required by VAPID, and it is how a provider tells
	// somebody their sending is broken rather than silently dropping it.
	Subject string

	// Timeout bounds one delivery. A push service that hangs must not hold a
	// request that a person is waiting on.
	Timeout time.Duration
}

// Sender delivers payloads.
type Sender struct {
	opts   Options
	client *http.Client
}

// New builds a sender, or reports that push is switched off.
func New(opts Options) (*Sender, error) {
	if opts.PublicKey == "" || opts.PrivateKey == "" {
		return nil, ErrNotConfigured
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	return &Sender{opts: opts, client: &http.Client{Timeout: opts.Timeout}}, nil
}

// GenerateKeys makes a VAPID pair.
//
// Offered so a local run can have push without an operator generating keys by
// hand, and so the console can show somebody a pair to paste into their
// deployment. A generated pair is not persisted here: keys that changed on
// restart would silently invalidate every subscription in the database.
func GenerateKeys() (privateKey, publicKey string, err error) {
	privateKey, publicKey, err = webpush.GenerateVAPIDKeys()
	if err != nil {
		return "", "", fmt.Errorf("push: generate VAPID keys: %w", err)
	}
	return privateKey, publicKey, nil
}

// Send delivers one payload to one browser.
//
// Once. There is no retry here and there deliberately is not one: the
// notification is already written down, so a push that fails costs somebody a
// buzz and nothing else, and a retry loop against a push service is how an
// installation gets itself rate-limited for no gain.
func (s *Sender) Send(ctx context.Context, device Device, payload Payload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("push: encode the payload: %w", err)
	}

	subscription := &webpush.Subscription{
		Endpoint: device.Endpoint,
		Keys:     webpush.Keys{P256dh: device.P256dh, Auth: device.Auth},
	}

	response, err := webpush.SendNotificationWithContext(ctx, body, subscription, &webpush.Options{
		HTTPClient:      s.client,
		Subscriber:      s.opts.Subject,
		VAPIDPublicKey:  s.opts.PublicKey,
		VAPIDPrivateKey: s.opts.PrivateKey,
		// A day. Long enough that a phone switched off overnight still gets
		// told about tomorrow's meeting, short enough that nothing arrives
		// about something that has already happened.
		TTL: int((24 * time.Hour).Seconds()),
		// Normal: none of this is urgent enough to wake a device on a low
		// battery. A meeting next week is not an emergency.
		Urgency: webpush.UrgencyNormal,
		Topic:   payload.Tag,
	})
	if err != nil {
		return fmt.Errorf("push: send: %w", err)
	}
	defer response.Body.Close() //nolint:errcheck

	switch {
	case response.StatusCode == http.StatusNotFound,
		response.StatusCode == http.StatusGone:
		// The browser is gone — uninstalled, permission revoked, or the
		// subscription rotated. Delete it; never retry it.
		return ErrGone
	case response.StatusCode >= 400:
		return fmt.Errorf("push: the push service answered %s", response.Status)
	}
	return nil
}
