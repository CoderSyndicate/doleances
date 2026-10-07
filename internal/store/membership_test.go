package store

import (
	"context"
	"errors"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// TestJoiningIsAPress is the shape of the whole change. Joining used to mean a
// name, an address, a six-digit code and a form to type it back into — all of
// it there to establish one thing, that the address was real, because the
// address *was* the identity. An account has proved itself with a passkey and
// carries no address to prove, so there is nothing left to do but say yes.
func TestJoiningIsAPress(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")
	dominique := account(t, s, "Dominique")

	if err := s.JoinGroup(ctx, group.ID, dominique.ID); err != nil {
		t.Fatalf("JoinGroup: %v", err)
	}

	members, err := s.ListGroupMembers(ctx, group.ID)
	if err != nil {
		t.Fatalf("ListGroupMembers: %v", err)
	}
	// Two: the author, who is the first admin, and the person who joined.
	if len(members) != 2 {
		t.Fatalf("%d members, want 2", len(members))
	}

	var joined *GroupMember
	for i, member := range members {
		if member.AccountID == dominique.ID {
			joined = &members[i]
		}
	}
	if joined == nil {
		t.Fatal("the person who joined is not in the member list")
	}
	if joined.Name != "Dominique" {
		t.Errorf("name = %q, want the name the account chose", joined.Name)
	}
	// Joining is not a role. Roles are granted.
	if joined.Role != "" {
		t.Errorf("role = %q, want an ordinary member", joined.Role)
	}
}

// TestAMemberListCarriesNoAddress. There is nothing to carry — which is the
// point of the change, not a gap in it. What a group's admins see about their
// own members is the name those members decided to be called.
func TestAMemberListCarriesNoAddress(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	if err := s.JoinGroup(ctx, group.ID, account(t, s, "Dominique").ID); err != nil {
		t.Fatalf("JoinGroup: %v", err)
	}

	members, err := s.ListGroupMembers(ctx, group.ID)
	if err != nil {
		t.Fatalf("ListGroupMembers: %v", err)
	}
	for _, member := range members {
		// A struct field cannot be asserted absent at runtime, so this asserts
		// the one thing that would betray an address having crept back in: a
		// name that looks like one.
		if member.Name != "" && (member.Name == "dominique@example.org") {
			t.Errorf("a member list carried an address: %q", member.Name)
		}
	}
}

// TestJoiningTwiceIsNotTwoMemberships: somebody pressed the button twice, or
// their page was stale. They wanted to be in the group and they are.
func TestJoiningTwiceIsNotTwoMemberships(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")
	dominique := account(t, s, "Dominique")

	if err := s.JoinGroup(ctx, group.ID, dominique.ID); err != nil {
		t.Fatalf("JoinGroup: %v", err)
	}
	if err := s.JoinGroup(ctx, group.ID, dominique.ID); !errors.Is(err, ErrAlreadyMember) {
		t.Errorf("err = %v, want ErrAlreadyMember", err)
	}

	members, _ := s.ListGroupMembers(ctx, group.ID)
	if len(members) != 2 {
		t.Errorf("%d members, want 2 — the same person joined twice", len(members))
	}
}

// TestOnlyAPublishedGroupCanBeJoined. A group still waiting on a decision has
// not been shown to anybody, and a membership in it would be a fact about a
// submission the public has no business knowing exists.
func TestOnlyAPublishedGroupCanBeJoined(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	pending := &models.Group{Name: "Collectif Citoyen"}
	if err := s.CreateGroup(ctx, pending, account(t, s, "Camille").ID); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	err := s.JoinGroup(ctx, pending.ID, account(t, s, "Dominique").ID)
	if !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("err = %v, want ErrGroupNotFound for a group awaiting review", err)
	}
	// And an identifier nobody issued is the same answer, so working through
	// identifiers tells nobody which groups exist.
	if err := s.JoinGroup(ctx, "not-an-id", account(t, s, "Ariane").ID); !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("err = %v, want ErrGroupNotFound", err)
	}
}

// TestLeavingRemovesTheMembershipAndNothingElse.
func TestLeavingRemovesTheMembershipAndNothingElse(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")
	dominique := account(t, s, "Dominique")

	if err := s.JoinGroup(ctx, group.ID, dominique.ID); err != nil {
		t.Fatalf("JoinGroup: %v", err)
	}
	if err := s.LeaveGroup(ctx, group.ID, dominique.ID); err != nil {
		t.Fatalf("LeaveGroup: %v", err)
	}

	if _, err := s.MembershipOf(ctx, group.ID, dominique.ID); !errors.Is(err, ErrNotAMember) {
		t.Errorf("err = %v, want ErrNotAMember", err)
	}
	// The account is untouched: leaving a group is not deleting yourself, and
	// conflating the two would make leaving something people hesitate over.
	if _, err := s.Account(ctx, dominique.ID); err != nil {
		t.Errorf("leaving a group removed the account: %v", err)
	}
	// Leaving twice is not an error worth a different answer, but it is worth
	// reporting: a caller that treats "not a member" as success is fine, and
	// one that needs to know gets told.
	if err := s.LeaveGroup(ctx, group.ID, dominique.ID); !errors.Is(err, ErrNotAMember) {
		t.Errorf("err = %v, want ErrNotAMember", err)
	}
}

// TestRolesAreGrantedAndTakenBack, and taking one back leaves the membership
// alone: somebody who is no longer an admin is still in the group they joined.
func TestRolesAreGrantedAndTakenBack(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")
	dominique := account(t, s, "Dominique")

	if err := s.JoinGroup(ctx, group.ID, dominique.ID); err != nil {
		t.Fatalf("JoinGroup: %v", err)
	}

	for _, role := range []models.GroupRole{models.RoleHost, models.RoleAdmin} {
		if err := s.SetMemberRole(ctx, group.ID, dominique.ID, role); err != nil {
			t.Fatalf("SetMemberRole(%q): %v", role, err)
		}
		membership, err := s.MembershipOf(ctx, group.ID, dominique.ID)
		if err != nil {
			t.Fatalf("MembershipOf: %v", err)
		}
		if membership.Role != role {
			t.Errorf("role = %q, want %q", membership.Role, role)
		}
	}

	if err := s.SetMemberRole(ctx, group.ID, dominique.ID, ""); err != nil {
		t.Fatalf("SetMemberRole(\"\"): %v", err)
	}
	membership, err := s.MembershipOf(ctx, group.ID, dominique.ID)
	if err != nil {
		t.Fatalf("revoking a role removed the membership: %v", err)
	}
	if membership.Role != "" {
		t.Errorf("role = %q, want an ordinary member", membership.Role)
	}
}

// TestAnUnknownRoleIsRefused. The column is a string, so the check has to be
// here: a row reading "moderator" would be a permission nothing in this
// project implements, and it would read as "no role" to every caller that
// compares against the three the model defines.
func TestAnUnknownRoleIsRefused(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")
	dominique := account(t, s, "Dominique")

	if err := s.JoinGroup(ctx, group.ID, dominique.ID); err != nil {
		t.Fatalf("JoinGroup: %v", err)
	}
	if err := s.SetMemberRole(ctx, group.ID, dominique.ID, "moderator"); err == nil {
		t.Error("an unknown role was accepted")
	}
}

// TestARoleIsScopedToOneGroup. An admin of one group must not reach another,
// and a membership lookup that matched on the account alone would let them.
func TestARoleIsScopedToOneGroup(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	first := published(t, s, "Collectif Citoyen")
	second := published(t, s, "Gilets Jaunes Guéret")
	dominique := account(t, s, "Dominique")

	if err := s.JoinGroup(ctx, first.ID, dominique.ID); err != nil {
		t.Fatalf("JoinGroup: %v", err)
	}
	if err := s.SetMemberRole(ctx, first.ID, dominique.ID, models.RoleAdmin); err != nil {
		t.Fatalf("SetMemberRole: %v", err)
	}

	if _, err := s.MembershipOf(ctx, second.ID, dominique.ID); !errors.Is(err, ErrNotAMember) {
		t.Errorf("err = %v, want an admin of one group to be nobody in another", err)
	}
	if err := s.SetMemberRole(ctx, second.ID, dominique.ID, models.RoleHost); !errors.Is(err, ErrNotAMember) {
		t.Errorf("err = %v, want ErrNotAMember", err)
	}
}

// TestGroupsOfIsWhatSomebodyBelongsTo, which is the *My groups* page.
func TestGroupsOfIsWhatSomebodyBelongsTo(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	first := published(t, s, "Collectif Citoyen")
	second := published(t, s, "Gilets Jaunes Guéret")
	dominique := account(t, s, "Dominique")

	for _, group := range []models.Group{first, second} {
		if err := s.JoinGroup(ctx, group.ID, dominique.ID); err != nil {
			t.Fatalf("JoinGroup(%q): %v", group.Name, err)
		}
	}

	joined, err := s.GroupsOf(ctx, dominique.ID)
	if err != nil {
		t.Fatalf("GroupsOf: %v", err)
	}
	if len(joined) != 2 {
		t.Fatalf("%d groups, want 2", len(joined))
	}
	// Somebody else's groups are not theirs.
	alone, err := s.GroupsOf(ctx, account(t, s, "Ariane").ID)
	if err != nil {
		t.Fatalf("GroupsOf: %v", err)
	}
	if len(alone) != 0 {
		t.Errorf("%d groups, want none", len(alone))
	}
}

// TestCountAdminsIsWhatSuccessionTurnsOn. A group whose last admin leaves has
// to offer the role to somebody else, and a group with nobody at all is
// deleted rather than left on the map as a door nobody can open — so the count
// has to be right, including when it is zero.
func TestCountAdminsIsWhatSuccessionTurnsOn(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")

	admins, err := s.CountAdmins(ctx, group.ID)
	if err != nil {
		t.Fatalf("CountAdmins: %v", err)
	}
	if admins != 1 {
		t.Fatalf("admins = %d, want the author", admins)
	}

	members, _ := s.ListGroupMembers(ctx, group.ID)
	if err := s.SetMemberRole(ctx, group.ID, members[0].AccountID, ""); err != nil {
		t.Fatalf("SetMemberRole: %v", err)
	}
	if admins, _ := s.CountAdmins(ctx, group.ID); admins != 0 {
		t.Errorf("admins = %d, want 0 — a leaderless group has to be findable", admins)
	}

	leaderless, err := s.ListLeaderlessGroups(ctx, 0)
	if err != nil {
		t.Fatalf("ListLeaderlessGroups: %v", err)
	}
	if len(leaderless) != 1 || leaderless[0].ID != group.ID {
		t.Errorf("found %d leaderless groups, want the one with no admin", len(leaderless))
	}
}

// TestDeletingAGroupTakesItsMessagesAndItsNotifications.
//
// The messages were private text addressed to admins who no longer exist, and
// there is nobody left who may read them. The notifications are worse than
// useless: each is a line offering a door into a group that is gone.
func TestDeletingAGroupTakesItsMessagesWithIt(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	group := published(t, s, "Collectif Citoyen")
	dominique := account(t, s, "Dominique")

	message := &models.GroupMessage{
		GroupID:       group.ID,
		FromAccountID: dominique.ID,
		FromName:      "Dominique",
		Text:          "Est-ce que la réunion de mardi tient toujours ?",
	}
	if err := s.WriteToGroup(ctx, message); err != nil {
		t.Fatalf("WriteToGroup: %v", err)
	}

	err := s.Notify(ctx, &models.Notification{
		AccountID: dominique.ID, Kind: models.NotifyGroupMessage,
		SubjectType: "group", SubjectID: group.ID, Title: group.Name,
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if err := s.DeleteGroup(ctx, group.ID); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}

	var left int64
	s.DB().Model(&models.GroupMessage{}).Count(&left) //nolint:errcheck
	if left != 0 {
		t.Errorf("%d messages outlived the group they were written to", left)
	}
	if _, total, _ := s.ListNotifications(ctx, dominique.ID, 20, 0); total != 0 {
		t.Errorf("%d notifications still point at a group that is gone", total)
	}
	if _, err := s.GetGroup(ctx, group.ID); !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("err = %v, want the group to be gone", err)
	}
}
