package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// ErrSubscriptionNotFound means no such push endpoint is registered here.
var ErrSubscriptionNotFound = errors.New("no such push subscription")

// Subscribe records that a browser has agreed to be told things.
//
// Upsert on the endpoint, because a browser that re-subscribes is handed the
// same endpoint back: two rows for it would send every notification twice to
// one screen, and the person would have no way to tell which of the two to
// remove.
//
// The keys are overwritten rather than kept, because a re-subscription can
// carry new ones — a browser that lost its permission and was granted it again
// generates a fresh pair, and the old ones then encrypt to nothing.
func (s *Store) Subscribe(ctx context.Context, subscription *models.PushSubscription) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "endpoint"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"account_id", "p256dh", "auth", "label", "updated_at",
		}),
	}).Create(subscription).Error
}

// ListSubscriptions is which of somebody's devices can be reached.
func (s *Store) ListSubscriptions(ctx context.Context, accountID string) ([]models.PushSubscription, error) {
	var subscriptions []models.PushSubscription
	err := s.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("created_at desc").
		Find(&subscriptions).Error
	return subscriptions, err
}

// Unsubscribe removes one device from somebody's own list.
func (s *Store) Unsubscribe(ctx context.Context, accountID, subscriptionID string) error {
	result := s.db.WithContext(ctx).
		Delete(&models.PushSubscription{}, "id = ? AND account_id = ?", subscriptionID, accountID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSubscriptionNotFound
	}
	return nil
}

// UnsubscribeEndpoint deletes a subscription the push service says is gone.
//
// This is the whole of the delivery-failure story, and it is deliberately not
// the ladder that *Email delivery* describes. A mailbox can be temporarily
// full, so retrying it makes sense; a push endpoint answering 404 or 410 is
// not there any more and will not be. There is no backoff, no phase two, and
// no column recording the last successful send.
func (s *Store) UnsubscribeEndpoint(ctx context.Context, endpoint string) error {
	return s.db.WithContext(ctx).
		Delete(&models.PushSubscription{}, "endpoint = ?", endpoint).Error
}

// RecordPushSent marks a subscription as having worked.
func (s *Store) RecordPushSent(ctx context.Context, endpoint string) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&models.PushSubscription{}).
		Where("endpoint = ?", endpoint).
		Update("last_used_at", now).Error
}

// Notify writes one thing worth telling somebody.
//
// The row comes first and the push second, always. A notification that existed
// only as a push would be lost to a refused permission, an iPhone that has not
// installed the site, or an endpoint that died between one week and the next —
// and the person would simply never find out.
func (s *Store) Notify(ctx context.Context, notification *models.Notification) error {
	return s.db.WithContext(ctx).Create(notification).Error
}

// NotifyMany writes the same thing to several people at once.
func (s *Store) NotifyMany(ctx context.Context, notifications []models.Notification) error {
	if len(notifications) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Create(&notifications).Error
}

// ListNotifications is somebody's own list, newest first.
func (s *Store) ListNotifications(ctx context.Context, accountID string, limit, offset int) ([]models.Notification, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	var total int64
	err := s.db.WithContext(ctx).Model(&models.Notification{}).
		Where("account_id = ?", accountID).Count(&total).Error
	if err != nil {
		return nil, 0, err
	}

	var notifications []models.Notification
	err = s.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("created_at desc").
		Limit(limit).Offset(offset).
		Find(&notifications).Error
	return notifications, total, err
}

// CountUnread is the number on the bell.
func (s *Store) CountUnread(ctx context.Context, accountID string) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&models.Notification{}).
		Where("account_id = ? AND read_at IS NULL", accountID).
		Count(&count).Error
	return count, err
}

// MarkNotificationRead marks one item read.
func (s *Store) MarkNotificationRead(ctx context.Context, accountID, notificationID string) error {
	now := time.Now()
	result := s.db.WithContext(ctx).Model(&models.Notification{}).
		Where("id = ? AND account_id = ? AND read_at IS NULL", notificationID, accountID).
		Updates(map[string]any{"read_at": now, "updated_at": now})
	return result.Error
}

// MarkAllNotificationsRead clears the bell.
func (s *Store) MarkAllNotificationsRead(ctx context.Context, accountID string) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&models.Notification{}).
		Where("account_id = ? AND read_at IS NULL", accountID).
		Updates(map[string]any{"read_at": now, "updated_at": now}).Error
}

// DeleteNotification removes one item from somebody's own list.
func (s *Store) DeleteNotification(ctx context.Context, accountID, notificationID string) error {
	return s.db.WithContext(ctx).
		Delete(&models.Notification{}, "id = ? AND account_id = ?", notificationID, accountID).Error
}

// WriteToGroup records a message to a group.
func (s *Store) WriteToGroup(ctx context.Context, message *models.GroupMessage) error {
	return s.db.WithContext(ctx).Create(message).Error
}

// ListGroupMessages is a group's inbox, newest first.
func (s *Store) ListGroupMessages(ctx context.Context, groupID string, limit, offset int) ([]models.GroupMessage, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	var total int64
	err := s.db.WithContext(ctx).Model(&models.GroupMessage{}).
		Where("group_id = ?", groupID).Count(&total).Error
	if err != nil {
		return nil, 0, err
	}

	var messages []models.GroupMessage
	err = s.db.WithContext(ctx).
		Where("group_id = ?", groupID).
		Order("created_at desc").
		Limit(limit).Offset(offset).
		Find(&messages).Error
	return messages, total, err
}

// MarkGroupMessageRead records that an admin has read it.
func (s *Store) MarkGroupMessageRead(ctx context.Context, groupID, messageID string) error {
	now := time.Now()
	result := s.db.WithContext(ctx).Model(&models.GroupMessage{}).
		Where("id = ? AND group_id = ? AND read_at IS NULL", messageID, groupID).
		Updates(map[string]any{"read_at": now, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	return nil
}

// CountUnreadGroupMessages is how much a group has not looked at.
func (s *Store) CountUnreadGroupMessages(ctx context.Context, groupID string) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&models.GroupMessage{}).
		Where("group_id = ? AND read_at IS NULL", groupID).
		Count(&count).Error
	return count, err
}

// PushTargetsForGroup is every device belonging to a group's admins.
//
// Admins rather than every member: a message to a group is addressed to
// whoever runs it, and pushing it to two hundred members would be publishing
// a private text to a mailing list.
func (s *Store) PushTargetsForGroup(ctx context.Context, groupID string, roles ...models.GroupRole) ([]string, []models.PushSubscription, error) {
	if len(roles) == 0 {
		roles = []models.GroupRole{models.RoleAdmin}
	}

	var memberships []models.GroupMembership
	err := s.db.WithContext(ctx).
		Where("group_id = ? AND role IN ?", groupID, roles).
		Find(&memberships).Error
	if err != nil {
		return nil, nil, err
	}

	accounts := make([]string, 0, len(memberships))
	for _, membership := range memberships {
		if membership.AccountID != "" {
			accounts = append(accounts, membership.AccountID)
		}
	}
	if len(accounts) == 0 {
		return nil, nil, nil
	}

	var subscriptions []models.PushSubscription
	err = s.db.WithContext(ctx).Where("account_id IN ?", accounts).Find(&subscriptions).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, err
	}
	return accounts, subscriptions, nil
}

// SubscriptionsFor is every device belonging to the named accounts.
func (s *Store) SubscriptionsFor(ctx context.Context, accountIDs []string) ([]models.PushSubscription, error) {
	if len(accountIDs) == 0 {
		return nil, nil
	}
	var subscriptions []models.PushSubscription
	err := s.db.WithContext(ctx).Where("account_id IN ?", accountIDs).Find(&subscriptions).Error
	return subscriptions, err
}
