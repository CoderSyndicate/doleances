package store

import (
	"context"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// TestResubscribingIsOneDeviceNotTwo.
//
// A browser that re-subscribes is handed the same endpoint back. Two rows for
// it would send every notification twice to one screen, and the person would
// have no way to tell which of the two to remove.
func TestResubscribingIsOneDeviceNotTwo(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	camille := account(t, s, "Camille")

	for _, keys := range []struct{ p256dh, auth, label string }{
		{"first-key", "first-auth", "a phone"},
		{"fresh-key", "fresh-auth", "the same phone"},
	} {
		err := s.Subscribe(ctx, &models.PushSubscription{
			AccountID: camille.ID,
			Endpoint:  "https://push.example/abc",
			P256dh:    keys.p256dh,
			Auth:      keys.auth,
			Label:     keys.label,
		})
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
	}

	devices, err := s.ListSubscriptions(ctx, camille.ID)
	if err != nil {
		t.Fatalf("ListSubscriptions: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("%d subscriptions, want 1 for one browser", len(devices))
	}
	// The keys are overwritten rather than kept: a browser that lost its
	// permission and was granted it again generates a fresh pair, and the old
	// ones then encrypt to nothing.
	if devices[0].P256dh != "fresh-key" || devices[0].Auth != "fresh-auth" {
		t.Error("a re-subscription kept the old keys")
	}
	if devices[0].Label != "the same phone" {
		t.Errorf("label = %q, want the newer one", devices[0].Label)
	}
}

// TestADeadEndpointIsDeletedNotRetried.
//
// This is the whole of the delivery-failure story, and it is deliberately not
// the ladder *Email delivery* describes. A mailbox can be temporarily full, so
// retrying it makes sense; a push endpoint answering 404 or 410 is not there
// any more and will not be.
func TestADeadEndpointIsDeletedNotRetried(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	camille := account(t, s, "Camille")

	err := s.Subscribe(ctx, &models.PushSubscription{
		AccountID: camille.ID, Endpoint: "https://push.example/gone",
		P256dh: "p", Auth: "a",
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := s.UnsubscribeEndpoint(ctx, "https://push.example/gone"); err != nil {
		t.Fatalf("UnsubscribeEndpoint: %v", err)
	}
	devices, _ := s.ListSubscriptions(ctx, camille.ID)
	if len(devices) != 0 {
		t.Error("a dead endpoint was kept")
	}
}

// TestANotificationExistsWhetherOrNotAPushDoes.
//
// The list is the channel and the push is a tap on the shoulder. An iPhone
// that has not installed the site receives no push at all, a permission can be
// refused, and an endpoint can die between one week and the next — so nothing
// may exist only as a push.
func TestANotificationExistsWhetherOrNotAPushDoes(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	camille := account(t, s, "Camille")

	// No subscription at all: the notification still has to land.
	err := s.Notify(ctx, &models.Notification{
		AccountID: camille.ID, Kind: models.NotifyGroupMessage,
		Title: "Collectif Citoyen", Body: "Dominique: bonjour",
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}

	items, total, err := s.ListNotifications(ctx, camille.ID, 20, 0)
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("%d notifications, want 1", total)
	}
	unread, err := s.CountUnread(ctx, camille.ID)
	if err != nil {
		t.Fatalf("CountUnread: %v", err)
	}
	if unread != 1 {
		t.Errorf("unread = %d, want 1", unread)
	}

	if err := s.MarkNotificationRead(ctx, camille.ID, items[0].ID); err != nil {
		t.Fatalf("MarkNotificationRead: %v", err)
	}
	if unread, _ := s.CountUnread(ctx, camille.ID); unread != 0 {
		t.Errorf("unread = %d after reading, want 0", unread)
	}

	// Somebody else's notification is not theirs to read or remove.
	dominique := account(t, s, "Dominique")
	if err := s.DeleteNotification(ctx, dominique.ID, items[0].ID); err != nil {
		t.Fatalf("DeleteNotification: %v", err)
	}
	if _, total, _ := s.ListNotifications(ctx, camille.ID, 20, 0); total != 1 {
		t.Error("another account deleted somebody's notification")
	}
}

// TestOnlyAdminsAreToldAboutAMessage.
//
// A message to a group is addressed to whoever runs it. Pushing it to two
// hundred members would be publishing a private text to a mailing list.
func TestOnlyAdminsAreToldAboutAMessage(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	member := account(t, s, "Dominique")
	if err := s.JoinGroup(ctx, group.ID, member.ID); err != nil {
		t.Fatalf("JoinGroup: %v", err)
	}
	for _, id := range []string{member.ID} {
		err := s.Subscribe(ctx, &models.PushSubscription{
			AccountID: id, Endpoint: "https://push.example/" + id,
			P256dh: "p", Auth: "a",
		})
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
	}

	admins, devices, err := s.PushTargetsForGroup(ctx, group.ID, models.RoleAdmin)
	if err != nil {
		t.Fatalf("PushTargetsForGroup: %v", err)
	}
	// The author, who is the group's only admin.
	if len(admins) != 1 {
		t.Fatalf("%d admins, want 1", len(admins))
	}
	for _, id := range admins {
		if id == member.ID {
			t.Error("an ordinary member was counted as an admin")
		}
	}
	// The admin has no subscription, so there is nothing to deliver to — and
	// the member's subscription must not be picked up instead.
	if len(devices) != 0 {
		t.Errorf("%d devices, want none: the admin has not subscribed", len(devices))
	}
}

// TestAGroupsInboxIsWhereItIsReached, and the unread count is what tells an
// admin something is waiting.
func TestAGroupsInboxIsWhereItIsReached(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")
	dominique := account(t, s, "Dominique")

	message := &models.GroupMessage{
		GroupID: group.ID, FromAccountID: dominique.ID, FromName: "Dominique",
		Text: "La réunion de mardi tient-elle ?",
	}
	if err := s.WriteToGroup(ctx, message); err != nil {
		t.Fatalf("WriteToGroup: %v", err)
	}

	items, total, err := s.ListGroupMessages(ctx, group.ID, 20, 0)
	if err != nil {
		t.Fatalf("ListGroupMessages: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("%d messages, want 1", total)
	}
	if unread, _ := s.CountUnreadGroupMessages(ctx, group.ID); unread != 1 {
		t.Error("a new message is not counted as unread")
	}

	if err := s.MarkGroupMessageRead(ctx, group.ID, items[0].ID); err != nil {
		t.Fatalf("MarkGroupMessageRead: %v", err)
	}
	if unread, _ := s.CountUnreadGroupMessages(ctx, group.ID); unread != 0 {
		t.Error("a read message is still counted as unread")
	}

	// A message belongs to one group, so another group's identifier is not a
	// way to read or mark it.
	other := published(t, s, "Gilets Jaunes Guéret")
	if _, total, _ := s.ListGroupMessages(ctx, other.ID, 20, 0); total != 0 {
		t.Error("a message reached another group's inbox")
	}
}
