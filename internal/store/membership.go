package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// ErrAlreadyMember means the account is already in the group.
var ErrAlreadyMember = errors.New("already a member of that group")

// ErrNotAMember means the account is not in the group.
var ErrNotAMember = errors.New("not a member of that group")

// JoinGroup puts a signed-in account into a group.
//
// # What used to happen here, and why it is gone
//
// Joining used to mean giving a name and an address, waiting for a six-digit
// code, and typing it back — a flow that existed for one reason: to find out
// whether the address was real, because the address *was* the identity. An
// account has proved itself already, with a passkey, and it carries no address
// to prove. So joining is a press.
//
// Only a published group can be joined. A group still waiting on a decision
// has not been shown to anybody, and a membership in it would be a fact about
// a submission the public has no business knowing exists.
func (s *Store) JoinGroup(ctx context.Context, groupID, accountID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var group models.Group
		err := tx.First(&group, "id = ?", groupID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrGroupNotFound
		}
		if err != nil {
			return err
		}
		if group.Status != models.StatusAccepted {
			// A group awaiting a decision and a group that never existed are
			// one answer: a membership in a submission nobody has seen would
			// be a fact about it, and working through identifiers must not
			// tell anybody which of them are real.
			return ErrGroupNotFound
		}

		var existing int64
		err = tx.Model(&models.GroupMembership{}).
			Where("group_id = ? AND account_id = ?", groupID, accountID).
			Count(&existing).Error
		if err != nil {
			return err
		}
		if existing > 0 {
			return ErrAlreadyMember
		}

		// Ordinary member: joining is not a role. Roles are granted.
		return tx.Create(&models.GroupMembership{
			GroupID:   groupID,
			AccountID: accountID,
			JoinedAt:  time.Now(),
		}).Error
	})
}

// LeaveGroup takes an account out of a group.
//
// The membership row goes and nothing else does: a past attendance is
// anonymous by construction here, because there is no identity attached to
// one in the first place.
func (s *Store) LeaveGroup(ctx context.Context, groupID, accountID string) error {
	result := s.db.WithContext(ctx).
		Delete(&models.GroupMembership{}, "group_id = ? AND account_id = ?", groupID, accountID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotAMember
	}
	return nil
}

// Membership is what an account may do in a group.
type Membership struct {
	GroupID  string
	Role     models.GroupRole
	JoinedAt time.Time
}

// MembershipOf reports what an account may do in one group.
func (s *Store) MembershipOf(ctx context.Context, groupID, accountID string) (Membership, error) {
	var row models.GroupMembership
	err := s.db.WithContext(ctx).
		Where("group_id = ? AND account_id = ?", groupID, accountID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Membership{}, ErrNotAMember
	}
	if err != nil {
		return Membership{}, err
	}
	return Membership{GroupID: row.GroupID, Role: row.Role, JoinedAt: row.JoinedAt}, nil
}

// GroupsOf is every group an account belongs to, with what it may do there.
func (s *Store) GroupsOf(ctx context.Context, accountID string) ([]Membership, error) {
	var rows []models.GroupMembership
	err := s.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("joined_at asc").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	out := make([]Membership, 0, len(rows))
	for _, row := range rows {
		out = append(out, Membership{GroupID: row.GroupID, Role: row.Role, JoinedAt: row.JoinedAt})
	}
	return out, nil
}

// GroupMember is one person in a group, as whoever manages it sees them.
//
// There is no address in it any more, and nothing else to put there: an
// account is a chosen name and nothing else. What a group's admins see about
// their own members is what those members decided to be called.
type GroupMember struct {
	AccountID string
	Name      string
	Role      models.GroupRole
	JoinedAt  time.Time
}

// ListGroupMembers returns who has joined, oldest first.
func (s *Store) ListGroupMembers(ctx context.Context, groupID string) ([]GroupMember, error) {
	var memberships []models.GroupMembership
	err := s.db.WithContext(ctx).
		Where("group_id = ?", groupID).
		Order("joined_at asc").
		Find(&memberships).Error
	if err != nil || len(memberships) == 0 {
		return nil, err
	}

	ids := make([]string, 0, len(memberships))
	for _, membership := range memberships {
		ids = append(ids, membership.AccountID)
	}

	var accounts []models.Account
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&accounts).Error; err != nil {
		return nil, err
	}
	names := make(map[string]string, len(accounts))
	for _, account := range accounts {
		names[account.ID] = account.Name
	}

	members := make([]GroupMember, 0, len(memberships))
	for _, membership := range memberships {
		members = append(members, GroupMember{
			AccountID: membership.AccountID,
			Name:      names[membership.AccountID],
			Role:      membership.Role,
			JoinedAt:  membership.JoinedAt,
		})
	}
	return members, nil
}

// SetMemberRole grants or takes back a role.
//
// An empty role is an ordinary member, which is what taking one back means:
// the membership survives, so revoking somebody's admin does not remove them
// from the group they joined.
func (s *Store) SetMemberRole(ctx context.Context, groupID, accountID string, role models.GroupRole) error {
	if role != "" && !role.Valid() {
		return errors.New("store: unknown group role")
	}

	result := s.db.WithContext(ctx).Model(&models.GroupMembership{}).
		Where("group_id = ? AND account_id = ?", groupID, accountID).
		Updates(map[string]any{"role": role, "updated_at": time.Now()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotAMember
	}
	return nil
}

// CountAdmins is how many people can still manage a group.
//
// The succession rules turn on it: a group whose last admin leaves has to
// offer the role to somebody else, and a group with nobody at all is deleted
// rather than left on the map as a door nobody can open.
func (s *Store) CountAdmins(ctx context.Context, groupID string) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&models.GroupMembership{}).
		Where("group_id = ? AND role = ?", groupID, models.RoleAdmin).
		Count(&count).Error
	return count, err
}
