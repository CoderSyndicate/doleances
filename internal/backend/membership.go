package backend

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/cache"
	"github.com/CoderSyndicate/doleances/internal/content"
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/store"
)

func (a *API) registerMembershipRoutes(api huma.API) {
	huma.Register(api, invalidates(cache.Groups)(authenticated(huma.Operation{
		OperationID: "join-group",
		Method:      http.MethodPost,
		Path:        "/v1/groups/{id}/join",
		Summary:     "Join a group",
		Description: "A press. Joining used to mean giving a name and an address and typing " +
			"back a six-digit code, for one reason: to find out whether the address was " +
			"real, because the address *was* the identity. An account has proved itself " +
			"already and carries no address to prove.",
		Tags: []string{"Groups"},
	})), a.joinGroup)

	huma.Register(api, invalidates(cache.Groups)(authenticated(huma.Operation{
		OperationID: "leave-group",
		Method:      http.MethodDelete,
		Path:        "/v1/groups/{id}/join",
		Summary:     "Leave a group",
		Description: "The membership row goes and nothing else does. A past attendance here " +
			"is anonymous by construction, because there is no identity attached to one in " +
			"the first place.",
		Tags: []string{"Groups"},
	})), a.leaveGroup)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "list-group-members",
		Method:      http.MethodGet,
		Path:        "/v1/groups/{id}/members",
		Summary:     "Who has joined a group",
		Description: "Behind being an admin of this group. It carries no addresses, because " +
			"there are none to carry: what an admin sees about their own members is the " +
			"name those members chose.",
		Tags: []string{"Groups"},
	}), a.listGroupMembers)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "set-member-role",
		Method:      http.MethodPatch,
		Path:        "/v1/groups/{id}/members/{account}",
		Summary:     "Grant or take back a role",
		Description: "An empty role is an ordinary member. Taking a role back leaves the " +
			"membership alone: somebody who is no longer an admin is still in the group " +
			"they joined. Audited either way.",
		Tags: []string{"Groups"},
	}), a.setMemberRole)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "write-to-group",
		Method:      http.MethodPost,
		Path:        "/v1/groups/{id}/messages",
		Summary:     "Write to a group",
		Description: "This is what replaced the contact address. The register holds no way to " +
			"reach anybody, so reaching a group happens inside it: a signed-in person " +
			"writes, the group's admins are notified, and neither side learns anything " +
			"about the other beyond a chosen name.\n\n" +
			"The answer never says whether anybody was reached. Whether an admin has a live " +
			"push subscription is a fact about that person's devices, and phrasing that " +
			"varied with it would leak one.",
		Tags: []string{"Groups"},
	}), a.writeToGroup)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "list-group-messages",
		Method:      http.MethodGet,
		Path:        "/v1/groups/{id}/messages",
		Summary:     "A group's inbox",
		Description: "Behind being an admin. This is the first private text this project " +
			"carries, so the same guards are applied at the door as to anything published — " +
			"sanitation, executable content refused — and the group's own admins are the " +
			"moderators of what reaches them.",
		Tags: []string{"Groups"},
	}), a.listGroupMessages)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "read-group-message",
		Method:      http.MethodPost,
		Path:        "/v1/groups/{id}/messages/{message}/read",
		Summary:     "Mark a message read",
		Tags:        []string{"Groups"},
	}), a.readGroupMessage)
}

// MemberItem is one person in a group, as whoever manages it sees them.
type MemberItem struct {
	// AccountID names the person for the purpose of a role grant, and for
	// nothing else. It is a random identifier: it is not a name, it cannot be
	// searched for, and it says nothing about who holds it.
	AccountID string `json:"account_id"`

	Name string `json:"name,omitempty"`

	// Role is empty for an ordinary member, who joins actions and nothing
	// more.
	Role string `json:"role,omitempty"`

	JoinedAt string `json:"joined_at"`
}

// MemberListOutput is who has joined.
type MemberListOutput struct {
	Body struct {
		Members []MemberItem `json:"members"`
	}
}

func toMemberItems(members []store.GroupMember) []MemberItem {
	items := make([]MemberItem, 0, len(members))
	for _, member := range members {
		items = append(items, MemberItem{
			AccountID: member.AccountID,
			Name:      member.Name,
			Role:      string(member.Role),
			JoinedAt:  member.JoinedAt.Format("2006-01-02"),
		})
	}
	return items
}

func (a *API) joinGroup(ctx context.Context, in *MessageIDInput) (*GroupOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	err = a.store.JoinGroup(ctx, in.ID, who.Account.ID)
	switch {
	case errors.Is(err, store.ErrGroupNotFound):
		// Only a group somebody can find is a group somebody can join, and a
		// group still in review has not been shown to anybody yet.
		return nil, huma.Error404NotFound("no such group")
	case errors.Is(err, store.ErrAlreadyMember):
		// Already done is not a failure. Somebody who pressed twice, or whose
		// page was stale, wanted to be in the group and is.
	case err != nil:
		log.Error().Err(err).Str("group", in.ID).Msg("cannot join a group")
		return nil, huma.Error500InternalServerError("cannot join the group")
	default:
		log.Info().Str("group", in.ID).Msg("somebody joined a group")
	}

	return a.getGroup(ctx, in)
}

func (a *API) leaveGroup(ctx context.Context, in *MessageIDInput) (*GroupOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	// Succession first, because the answer changes what leaving means. A group
	// whose last admin walks away has nobody who can post an action, accept a
	// change, or bring it back from hiding — so the people who could take it
	// on are told, and a group with nobody at all is deleted rather than left
	// on the map as a door nobody can open.
	if err := a.offerSuccession(ctx, in.ID, who.Account.ID); err != nil {
		return nil, err
	}

	err = a.store.LeaveGroup(ctx, in.ID, who.Account.ID)
	switch {
	case errors.Is(err, store.ErrNotAMember):
		// Not in it is where they wanted to be.
	case err != nil:
		log.Error().Err(err).Str("group", in.ID).Msg("cannot leave a group")
		return nil, huma.Error500InternalServerError("cannot leave the group")
	default:
		log.Info().Str("group", in.ID).Msg("somebody left a group")
	}

	return a.getGroup(ctx, in)
}

// offerSuccession tells a group that its last admin is going.
//
// The order is *Group roles*' own: hosts first, because they already manage
// actions and are the obvious next admins; ordinary members otherwise; and a
// group with nobody at all is deleted immediately, because an empty group with
// no admin is not a group.
//
// The one-week deletion for a group nobody takes on is not here: it belongs to
// the sweep, which is where anything with a deadline lives.
func (a *API) offerSuccession(ctx context.Context, groupID, leavingAccountID string) error {
	membership, err := a.store.MembershipOf(ctx, groupID, leavingAccountID)
	if errors.Is(err, store.ErrNotAMember) {
		return nil
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot read a membership")
		return huma.Error500InternalServerError("cannot leave the group")
	}
	if membership.Role != models.RoleAdmin {
		return nil
	}

	admins, err := a.store.CountAdmins(ctx, groupID)
	if err != nil {
		log.Error().Err(err).Msg("cannot count a group's admins")
		return huma.Error500InternalServerError("cannot leave the group")
	}
	if admins > 1 {
		return nil
	}

	group, err := a.store.GetGroup(ctx, groupID)
	if err != nil {
		log.Error().Err(err).Msg("cannot read a group")
		return huma.Error500InternalServerError("cannot leave the group")
	}

	members, err := a.store.ListGroupMembers(ctx, groupID)
	if err != nil {
		log.Error().Err(err).Msg("cannot read a group's members")
		return huma.Error500InternalServerError("cannot leave the group")
	}

	var hosts, others []string
	for _, member := range members {
		if member.AccountID == leavingAccountID || member.AccountID == "" {
			continue
		}
		if member.Role == models.RoleHost {
			hosts = append(hosts, member.AccountID)
		} else {
			others = append(others, member.AccountID)
		}
	}

	successors := hosts
	if len(successors) == 0 {
		successors = others
	}
	if len(successors) == 0 {
		// Nobody at all. Deleted rather than left dormant: a leaderless group
		// cannot be reactivated by anyone, so leaving it on the map would be
		// advertising a door nobody can open.
		if err := a.store.DeleteGroup(ctx, groupID); err != nil {
			log.Error().Err(err).Str("group", groupID).
				Msg("cannot delete a group whose last member left")
			return huma.Error500InternalServerError("cannot leave the group")
		}
		log.Warn().Str("group", group.Name).
			Msg("a group was deleted because its last member left")
		return nil
	}

	a.tell(ctx, successors, Announcement{
		Kind:        models.NotifyGroupDecision,
		Title:       group.Name,
		Body:        "The last admin is leaving. Will you take the group on?",
		SubjectType: "group",
		SubjectID:   group.ID,
		Path:        "/groups/" + group.ID,
	})
	return nil
}

func (a *API) listGroupMembers(ctx context.Context, in *MessageIDInput) (*MemberListOutput, error) {
	group, err := a.groupAdmin(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return a.memberList(ctx, group.ID)
}

// SetRoleInput grants or takes back a role.
type SetRoleInput struct {
	ID      string `path:"id"`
	Account string `path:"account"`
	Body    struct {
		// Role is "admin", "host", or empty for an ordinary member.
		Role string `json:"role"`
	}
}

func (a *API) setMemberRole(ctx context.Context, in *SetRoleInput) (*MemberListOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := a.groupAdmin(ctx, in.ID); err != nil {
		return nil, err
	}

	role := models.GroupRole(strings.TrimSpace(in.Body.Role))
	if role != "" && !role.Valid() {
		return nil, huma.Error422UnprocessableEntity("that is not a role")
	}

	// An admin may not take their own admin away while they are the last one.
	// It is the same rule as removing the only passkey from an account: the
	// group would be left with nobody who can run it, and the person doing it
	// would not have meant that.
	if in.Account == who.Account.ID && role != models.RoleAdmin {
		admins, err := a.store.CountAdmins(ctx, in.ID)
		if err != nil {
			log.Error().Err(err).Msg("cannot count a group's admins")
			return nil, huma.Error500InternalServerError("cannot change the role")
		}
		if admins <= 1 {
			return nil, huma.Error409Conflict(
				"you are this group's only admin: make somebody else an admin first, or " +
					"leave the group, which offers it to the others")
		}
	}

	err = a.store.SetMemberRole(ctx, in.ID, in.Account, role)
	if errors.Is(err, store.ErrNotAMember) {
		return nil, huma.Error404NotFound("that person is not in this group")
	}
	if err != nil {
		log.Error().Err(err).Str("group", in.ID).Msg("cannot set a member role")
		return nil, huma.Error500InternalServerError("cannot change the role")
	}

	// Granting and revoking are both recorded. The acting identity is an
	// account rather than a named human, and that is said plainly rather than
	// left to look like an OIDC identity. The member is recorded by account
	// identifier, never by anything else: an audit log is a record of what was
	// done, not a second copy of who somebody is.
	action := models.AuditRoleGrant
	if role == "" {
		action = models.AuditRoleRevoke
	}
	a.recordDecision(ctx, "account "+who.Account.ID, action,
		"account", in.Account, "group "+in.ID+", role "+string(role))

	// Told, because a role is something a person is given rather than
	// something that happens to their row. Somebody who has been made an
	// admin has to find out, or the group has handed responsibility to
	// somebody who does not know they hold it.
	if role != "" {
		group, err := a.store.GetGroup(ctx, in.ID)
		if err == nil {
			a.tell(ctx, []string{in.Account}, Announcement{
				Kind:        models.NotifyGroupDecision,
				Title:       group.Name,
				Body:        "You are now " + string(role) + " of this group.",
				SubjectType: "group",
				SubjectID:   group.ID,
				Path:        "/groups/" + group.ID,
			})
		}
	}

	log.Info().Str("group", in.ID).Str("role", string(role)).
		Msg("group member role changed")

	return a.memberList(ctx, in.ID)
}

func (a *API) memberList(ctx context.Context, id string) (*MemberListOutput, error) {
	members, err := a.store.ListGroupMembers(ctx, id)
	if err != nil {
		log.Error().Err(err).Str("group", id).Msg("cannot read the members")
		return nil, huma.Error500InternalServerError("cannot read the members")
	}

	out := &MemberListOutput{}
	out.Body.Members = toMemberItems(members)
	return out, nil
}

// maxGroupMessageRunes bounds what somebody may write to a group.
//
// Longer than a notification and much shorter than a doléance: this is a
// message to two or three people, not a text for the register, and an
// unbounded field on an endpoint anybody signed in can call is a way to fill
// somebody else's inbox.
const maxGroupMessageRunes = 2000

// WriteToGroupInput is somebody writing to a group.
type WriteToGroupInput struct {
	ID   string `path:"id"`
	Body struct {
		Text string `json:"text"`
	}
}

// WriteToGroupOutput says the group will see it, and says nothing else.
type WriteToGroupOutput struct {
	Body struct {
		// Sent is always true on success, and carries no information about
		// delivery. Whether any admin has a live push subscription is a fact
		// about their devices; an answer that varied with it would leak one.
		Sent bool `json:"sent"`
	}
}

func (a *API) writeToGroup(ctx context.Context, in *WriteToGroupInput) (*WriteToGroupOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	group, err := a.store.GetGroup(ctx, in.ID)
	if errors.Is(err, store.ErrGroupNotFound) || group.Status != models.StatusAccepted {
		return nil, huma.Error404NotFound("no such group")
	}
	if err != nil {
		log.Error().Err(err).Str("group", in.ID).Msg("cannot read a group")
		return nil, huma.Error500InternalServerError("cannot read the group")
	}

	text, _ := content.Sanitise(in.Body.Text)
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, huma.Error422UnprocessableEntity("there is nothing to send")
	}
	if utf8.RuneCountInString(text) > maxGroupMessageRunes {
		return nil, huma.Error422UnprocessableEntity("that message is too long")
	}
	// Refused, not cleaned, exactly as the register refuses a doléance
	// carrying a script: this text is rendered on somebody's page, and
	// removing part of what a person wrote is not something anybody here does.
	if content.ContainsExecutablePayload(text) {
		return nil, huma.Error422UnprocessableEntity("that cannot be sent")
	}

	message := &models.GroupMessage{
		GroupID:       group.ID,
		FromAccountID: who.Account.ID,
		FromName:      who.Account.Name,
		Text:          text,
	}
	if err := a.store.WriteToGroup(ctx, message); err != nil {
		log.Error().Err(err).Str("group", group.ID).Msg("cannot record a message to a group")
		return nil, huma.Error500InternalServerError("cannot send the message")
	}

	// The admins, not every member: a message to a group is addressed to
	// whoever runs it, and pushing it to two hundred members would be
	// publishing a private text to a mailing list.
	admins, _, err := a.store.PushTargetsForGroup(ctx, group.ID, models.RoleAdmin)
	if err != nil {
		log.Error().Err(err).Msg("cannot read a group's admins")
	} else {
		from := who.Account.Name
		if from == "" {
			from = "somebody"
		}
		a.tell(ctx, admins, Announcement{
			Kind:        models.NotifyGroupMessage,
			Title:       group.Name,
			Body:        from + ": " + notifyText(text),
			SubjectType: "group",
			SubjectID:   group.ID,
			Path:        "/groups/" + group.ID + "/manage",
		})
	}

	out := &WriteToGroupOutput{}
	out.Body.Sent = true
	return out, nil
}

// GroupMessageItem is one message in a group's inbox.
type GroupMessageItem struct {
	ID string `json:"id"`

	// From is the name the sender chose, and all there is to know about them.
	// An account carries neither address nor real name, so a group's inbox is
	// a list of what people called themselves.
	From string `json:"from,omitempty"`

	Text      string    `json:"text"`
	Read      bool      `json:"read"`
	CreatedAt time.Time `json:"created_at"`
}

// GroupMessagesOutput is a group's inbox.
type GroupMessagesOutput struct {
	Body struct {
		Items  []GroupMessageItem `json:"items"`
		Total  int64              `json:"total"`
		Unread int64              `json:"unread"`
	}
}

// GroupInboxInput pages a group's inbox.
type GroupInboxInput struct {
	ID     string `path:"id"`
	Limit  int    `query:"limit"`
	Offset int    `query:"offset"`
}

func (a *API) listGroupMessages(ctx context.Context, in *GroupInboxInput) (*GroupMessagesOutput, error) {
	group, err := a.groupAdmin(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return a.groupInbox(ctx, group.ID, in.Limit, in.Offset)
}

// ReadGroupMessageInput names one message in a group's inbox.
type ReadGroupMessageInput struct {
	ID      string `path:"id"`
	Message string `path:"message"`
}

func (a *API) readGroupMessage(ctx context.Context, in *ReadGroupMessageInput) (*GroupMessagesOutput, error) {
	group, err := a.groupAdmin(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if err := a.store.MarkGroupMessageRead(ctx, group.ID, in.Message); err != nil {
		log.Error().Err(err).Msg("cannot mark a group message read")
		return nil, huma.Error500InternalServerError("cannot mark it read")
	}
	return a.groupInbox(ctx, group.ID, 0, 0)
}

func (a *API) groupInbox(ctx context.Context, groupID string, limit, offset int) (*GroupMessagesOutput, error) {
	messages, total, err := a.store.ListGroupMessages(ctx, groupID, limit, offset)
	if err != nil {
		log.Error().Err(err).Str("group", groupID).Msg("cannot read a group's messages")
		return nil, huma.Error500InternalServerError("cannot read the messages")
	}
	unread, err := a.store.CountUnreadGroupMessages(ctx, groupID)
	if err != nil {
		log.Error().Err(err).Msg("cannot count unread group messages")
	}

	out := &GroupMessagesOutput{}
	out.Body.Total = total
	out.Body.Unread = unread
	out.Body.Items = make([]GroupMessageItem, 0, len(messages))
	for _, message := range messages {
		out.Body.Items = append(out.Body.Items, GroupMessageItem{
			ID:        message.ID,
			From:      message.FromName,
			Text:      message.Text,
			Read:      message.ReadAt != nil,
			CreatedAt: message.CreatedAt,
		})
	}
	return out, nil
}
