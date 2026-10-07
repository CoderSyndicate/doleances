package backend

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/content"
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/push"
	"github.com/CoderSyndicate/doleances/internal/store"
)

// maxNotifyTitleRunes and maxNotifyBodyRunes bound what is stored and pushed.
//
// A notification is a line on a list and a line on a lock screen. Anything
// longer is cut by the operating system anyway, so cutting it here means the
// stored record and the push say the same thing.
const (
	maxNotifyTitleRunes = 120
	maxNotifyBodyRunes  = 240
)

func (a *API) registerNotificationRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "read-push-key",
		Method:      http.MethodGet,
		Path:        "/v1/notifications/key",
		Summary:     "The VAPID public key, or nothing",
		Description: "A browser needs it to subscribe. An installation with no keys answers " +
			"with an empty one rather than an error: the notification list is the channel " +
			"and push is a tap on the shoulder, so a register without it is not broken.",
		Tags: []string{"Notifications"},
	}, a.readPushKey)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "subscribe-push",
		Method:      http.MethodPost,
		Path:        "/v1/notifications/subscribe",
		Summary:     "Let this device be notified",
		Description: "One subscription per browser on per device. It is an address for a " +
			"browser, not an identity: it cannot be searched for, written to by anybody " +
			"but this server, or correlated with anything outside this register.",
		Tags: []string{"Notifications"},
	}), a.subscribePush)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "unsubscribe-push",
		Method:      http.MethodDelete,
		Path:        "/v1/notifications/subscriptions/{subscription}",
		Summary:     "Stop notifying this device",
		Tags:        []string{"Notifications"},
	}), a.unsubscribePush)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "list-notifications",
		Method:      http.MethodGet,
		Path:        "/v1/notifications",
		Summary:     "This account's notifications",
		Description: "The list is the channel. Everything is written here whether or not a " +
			"push reached anybody, so somebody who refuses permission — or whose phone " +
			"cannot receive one at all — still finds out by coming back.",
		Tags: []string{"Notifications"},
	}), a.listNotifications)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "read-notification",
		Method:      http.MethodPost,
		Path:        "/v1/notifications/{notification}/read",
		Summary:     "Mark one as read",
		Tags:        []string{"Notifications"},
	}), a.readNotification)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "read-all-notifications",
		Method:      http.MethodPost,
		Path:        "/v1/notifications/read",
		Summary:     "Mark everything as read",
		Tags:        []string{"Notifications"},
	}), a.readAllNotifications)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "delete-notification",
		Method:      http.MethodDelete,
		Path:        "/v1/notifications/{notification}",
		Summary:     "Remove one from the list",
		Tags:        []string{"Notifications"},
	}), a.deleteNotification)
}

// PushKeyOutput is the VAPID public key.
type PushKeyOutput struct {
	Body struct {
		PublicKey string `json:"public_key,omitempty"`

		// Configured is explicit rather than left to be inferred from an
		// empty key, so a page can say "this register cannot send
		// notifications" instead of looking broken.
		Configured bool `json:"configured"`
	}
}

func (a *API) readPushKey(_ context.Context, _ *struct{}) (*PushKeyOutput, error) {
	out := &PushKeyOutput{}
	out.Body.PublicKey = a.pushKey
	out.Body.Configured = a.push != nil
	return out, nil
}

// SubscribeInput is a browser's push subscription, as the Push API produced it.
type SubscribeInput struct {
	Body struct {
		Endpoint string `json:"endpoint"`

		// P256dh and Auth are the browser's own keys. The payload is
		// encrypted to them, so the push service carries the message without
		// being able to read it.
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`

		Label string `json:"label,omitempty"`
	}
}

func (a *API) subscribePush(ctx context.Context, in *SubscribeInput) (*DoneOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	endpoint := strings.TrimSpace(in.Body.Endpoint)
	if !strings.HasPrefix(endpoint, "https://") || len(endpoint) > 512 {
		// A push endpoint is always an https URL issued by a push service.
		// Anything else is either a mistake or an attempt to make this server
		// send a request somewhere of the caller's choosing.
		return nil, huma.Error422UnprocessableEntity("that is not a push endpoint")
	}
	if in.Body.P256dh == "" || in.Body.Auth == "" {
		return nil, huma.Error422UnprocessableEntity("that subscription carries no keys")
	}

	subscription := &models.PushSubscription{
		AccountID: who.Account.ID,
		Endpoint:  endpoint,
		P256dh:    in.Body.P256dh,
		Auth:      in.Body.Auth,
		Label:     deviceLabel(in.Body.Label),
	}
	if err := a.store.Subscribe(ctx, subscription); err != nil {
		log.Error().Err(err).Msg("cannot record a push subscription")
		return nil, huma.Error500InternalServerError("cannot subscribe this device")
	}

	log.Debug().Str("account", who.Account.ID).Msg("a device subscribed to notifications")
	return done(), nil
}

// SubscriptionPathInput names one of an account's subscriptions.
type SubscriptionPathInput struct {
	Subscription string `path:"subscription"`
}

func (a *API) unsubscribePush(ctx context.Context, in *SubscriptionPathInput) (*DoneOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	err = a.store.Unsubscribe(ctx, who.Account.ID, in.Subscription)
	if errors.Is(err, store.ErrSubscriptionNotFound) {
		return nil, huma.Error404NotFound("no such device on this account")
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot remove a push subscription")
		return nil, huma.Error500InternalServerError("cannot unsubscribe this device")
	}
	return done(), nil
}

// NotificationItem is one thing somebody was told.
type NotificationItem struct {
	ID   string                  `json:"id"`
	Kind models.NotificationKind `json:"kind"`

	Title string `json:"title"`
	Body  string `json:"body,omitempty"`

	// SubjectType and SubjectID are what it is about, so the item can link
	// somewhere rather than being a sentence with no door.
	SubjectType string `json:"subject_type,omitempty"`
	SubjectID   string `json:"subject_id,omitempty"`

	Read      bool      `json:"read"`
	CreatedAt time.Time `json:"created_at"`
}

// NotificationsOutput is somebody's list.
type NotificationsOutput struct {
	Body struct {
		Items  []NotificationItem `json:"items"`
		Total  int64              `json:"total"`
		Unread int64              `json:"unread"`
	}
}

// NotificationListInput pages the list.
type NotificationListInput struct {
	Limit  int `query:"limit"`
	Offset int `query:"offset"`
}

func (a *API) listNotifications(ctx context.Context, in *NotificationListInput) (*NotificationsOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	items, total, err := a.store.ListNotifications(ctx, who.Account.ID, in.Limit, in.Offset)
	if err != nil {
		log.Error().Err(err).Msg("cannot read the notifications")
		return nil, huma.Error500InternalServerError("cannot read the notifications")
	}
	unread, err := a.store.CountUnread(ctx, who.Account.ID)
	if err != nil {
		log.Error().Err(err).Msg("cannot count unread notifications")
	}

	out := &NotificationsOutput{}
	out.Body.Total = total
	out.Body.Unread = unread
	out.Body.Items = make([]NotificationItem, 0, len(items))
	for _, item := range items {
		out.Body.Items = append(out.Body.Items, NotificationItem{
			ID:          item.ID,
			Kind:        item.Kind,
			Title:       item.Title,
			Body:        item.Body,
			SubjectType: item.SubjectType,
			SubjectID:   item.SubjectID,
			Read:        item.ReadAt != nil,
			CreatedAt:   item.CreatedAt,
		})
	}
	return out, nil
}

// NotificationPathInput names one notification.
type NotificationPathInput struct {
	Notification string `path:"notification"`
}

func (a *API) readNotification(ctx context.Context, in *NotificationPathInput) (*NotificationsOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.store.MarkNotificationRead(ctx, who.Account.ID, in.Notification); err != nil {
		log.Error().Err(err).Msg("cannot mark a notification read")
		return nil, huma.Error500InternalServerError("cannot mark it read")
	}
	return a.listNotifications(ctx, &NotificationListInput{})
}

func (a *API) readAllNotifications(ctx context.Context, _ *struct{}) (*NotificationsOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.store.MarkAllNotificationsRead(ctx, who.Account.ID); err != nil {
		log.Error().Err(err).Msg("cannot mark the notifications read")
		return nil, huma.Error500InternalServerError("cannot mark them read")
	}
	return a.listNotifications(ctx, &NotificationListInput{})
}

func (a *API) deleteNotification(ctx context.Context, in *NotificationPathInput) (*NotificationsOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.store.DeleteNotification(ctx, who.Account.ID, in.Notification); err != nil {
		log.Error().Err(err).Msg("cannot delete a notification")
		return nil, huma.Error500InternalServerError("cannot remove it")
	}
	return a.listNotifications(ctx, &NotificationListInput{})
}

// Announcement is one thing worth telling people.
type Announcement struct {
	Kind        models.NotificationKind
	Title       string
	Body        string
	SubjectType string
	SubjectID   string

	// Path is where tapping the push goes, relative to the site. Empty means
	// the notification list.
	Path string
}

// tell writes a notification for each account and pushes it to their devices.
//
// # The order is the design
//
// The rows are written first and the pushes sent second, and a failure in the
// second half changes nothing about the first. That is what makes it true that
// nothing exists only as a push: a refused permission, an iPhone that has not
// installed the site, an endpoint that died last week — in every case the
// person still finds out by coming back.
//
// # One notification, not a campaign
//
// One row per person and one push per device they subscribed. Every subscribed
// device buzzing once is not a breach of the rule: the rule is one per thing,
// not one per screen, and it is exactly what email did. What must never happen
// is the same event arriving twice on one device, which the payload's tag
// prevents.
func (a *API) tell(ctx context.Context, accountIDs []string, announcement Announcement) {
	if len(accountIDs) == 0 {
		return
	}

	title := trimTo(strings.TrimSpace(announcement.Title), maxNotifyTitleRunes)
	body := trimTo(strings.TrimSpace(announcement.Body), maxNotifyBodyRunes)

	rows := make([]models.Notification, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		if accountID == "" {
			continue
		}
		rows = append(rows, models.Notification{
			AccountID:   accountID,
			Kind:        announcement.Kind,
			SubjectType: announcement.SubjectType,
			SubjectID:   announcement.SubjectID,
			Title:       title,
			Body:        body,
		})
	}
	if len(rows) == 0 {
		return
	}

	if err := a.store.NotifyMany(ctx, rows); err != nil {
		// Logged and not retried. The caller is completing somebody's action
		// — joining a group, writing to one — and that must not fail because
		// a notification could not be written.
		log.Error().Err(err).Str("kind", string(announcement.Kind)).
			Msg("cannot write notifications")
		return
	}

	a.pushTo(ctx, accountIDs, announcement)
}

// pushTo delivers the tap on the shoulder.
func (a *API) pushTo(ctx context.Context, accountIDs []string, announcement Announcement) {
	if a.push == nil {
		return
	}

	devices, err := a.store.SubscriptionsFor(ctx, accountIDs)
	if err != nil {
		log.Error().Err(err).Msg("cannot read the push subscriptions")
		return
	}

	url := a.siteURL + "/notifications"
	if announcement.Path != "" {
		url = a.siteURL + announcement.Path
	}

	// The tag collapses repeats of the same event on one device. Keyed on
	// what the notification is about rather than on its row, so two rows
	// about one meeting do not buzz twice on one phone.
	tag := string(announcement.Kind)
	if announcement.SubjectID != "" {
		tag += ":" + announcement.SubjectID
	}

	payload := push.Payload{
		Title: trimTo(strings.TrimSpace(announcement.Title), maxNotifyTitleRunes),
		Body:  trimTo(strings.TrimSpace(announcement.Body), maxNotifyBodyRunes),
		URL:   url,
		Tag:   tag,
	}

	for _, device := range devices {
		err := a.push.Send(ctx, push.Device{
			Endpoint: device.Endpoint,
			P256dh:   device.P256dh,
			Auth:     device.Auth,
		}, payload)

		switch {
		case err == nil:
			if err := a.store.RecordPushSent(ctx, device.Endpoint); err != nil {
				log.Error().Err(err).Msg("cannot record a delivered push")
			}
		case errors.Is(err, push.ErrGone):
			// Deleted, never retried. A push endpoint is either there or it
			// is gone, which is the whole of the delivery-failure story here.
			if err := a.store.UnsubscribeEndpoint(ctx, device.Endpoint); err != nil {
				log.Error().Err(err).Msg("cannot remove a dead push subscription")
			}
			log.Debug().Msg("a push subscription was gone and has been removed")
		default:
			log.Warn().Err(err).Msg("cannot deliver a push notification")
		}
	}
}

// notifyText cleans a line that came from somebody else's writing before it
// becomes a notification.
//
// A notification body is rendered on a lock screen by software this project
// does not control, so the same guard every stored string gets applies here
// too: invisible reordering characters out, executable content refused. The
// register learned once that `html/template` does not touch `U+202E`.
func notifyText(text string) string {
	cleaned, _ := content.Sanitise(text)
	if content.ContainsExecutablePayload(cleaned) {
		return ""
	}
	return strings.TrimSpace(cleaned)
}
