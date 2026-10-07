package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Group is a local action group as the API reports it.
type Group struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`

	Status  string `json:"status"`
	Visible bool   `json:"visible"`

	Place     string  `json:"place,omitempty"`
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
	Geohash   string  `json:"geohash,omitempty"`

	CreatedAt time.Time `json:"created_at"`

	// Role is what the signed-in reader may do here: "admin", "host", or
	// empty. Member says whether they have joined at all, which is a different
	// question — every admin is a member, and most members hold no role.
	Role   string `json:"role,omitempty"`
	Member bool   `json:"member"`
}

// Admin reports whether the reader may manage this group.
func (g Group) Admin() bool { return g.Role == "admin" }

// Host reports whether the reader may manage this group's actions.
func (g Group) Host() bool { return g.Role == "admin" || g.Role == "host" }

// ManagedGroup is the group as one of its admins sees it.
type ManagedGroup struct {
	Group

	// Members is who has joined. There is no address in it and nothing else to
	// put there: an account is a chosen name and nothing more.
	Members []Member `json:"members"`

	// Unread is how many messages the group has not looked at.
	Unread int64 `json:"unread"`
}

// MyGroup is one of the reader's own groups.
type MyGroup struct {
	Group
	JoinedAt time.Time `json:"joined_at"`
	Unread   int64     `json:"unread,omitempty"`
}

// GroupDraft is a proposed group.
//
// There is no contact address in it any more. A group used to prove a mailbox
// answered before it entered the pipeline, because the address *was* the
// identity; the account proposing it has proved itself with a passkey and
// carries no address to prove.
type GroupDraft struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`

	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
	Place     string  `json:"place,omitempty"`
	Country   string  `json:"country,omitempty"`
	Zoom      int     `json:"zoom,omitempty"`
}

// GroupNameAvailable reports whether a name can still be claimed.
//
// A failure answers "taken". The check is a courtesy at the keyboard and the
// real one happens at the write, so the safe direction when the backend is
// unreachable is to let somebody keep typing rather than promise a name this
// client could not verify.
func (c *Client) GroupNameAvailable(ctx context.Context, name string) (bool, error) {
	var payload struct {
		Available bool `json:"available"`
	}
	path := "/v1/groups/available?name=" + url.QueryEscape(name)
	if err := c.get(ctx, path, &payload); err != nil {
		return false, err
	}
	return payload.Available, nil
}

// CreateGroup proposes a group. The account that proposes it becomes its first
// admin, and nothing about that is permanent.
func (c *Client) CreateGroup(ctx context.Context, draft GroupDraft) (Group, error) {
	body, err := json.Marshal(draft)
	if err != nil {
		return Group{}, fmt.Errorf("encode group: %w", err)
	}

	var group Group
	if err := c.postJSON(ctx, "/v1/groups", body, &group); err != nil {
		return Group{}, err
	}
	return group, nil
}

// MyGroups is what the reader belongs to, for the *My groups* page.
func (c *Client) MyGroups(ctx context.Context) ([]MyGroup, error) {
	var payload struct {
		Groups []MyGroup `json:"groups"`
	}
	if err := c.get(ctx, "/v1/accounts/me/groups", &payload); err != nil {
		return nil, err
	}
	return payload.Groups, nil
}

// ListGroups returns the groups on the map and how many exist in the world.
//
// The total is not a pagination detail. The spread of groups across the map is
// the argument this page makes, and a viewport narrows what is drawn without
// narrowing what is counted.
func (c *Client) ListGroups(ctx context.Context, bounds string) ([]Group, int64, error) {
	path := "/v1/groups/map"
	if bounds != "" {
		path += "?bounds=" + url.QueryEscape(bounds)
	}

	var payload struct {
		Groups []Group `json:"groups"`
		Total  int64   `json:"total"`
	}
	if err := c.get(ctx, path, &payload); err != nil {
		return nil, 0, err
	}
	return payload.Groups, payload.Total, nil
}

// GetGroup returns one group, at any status.
func (c *Client) GetGroup(ctx context.Context, id string) (Group, error) {
	var group Group
	if err := c.get(ctx, "/v1/groups/"+url.PathEscape(id), &group); err != nil {
		return Group{}, err
	}
	return group, nil
}

// ManageGroup reads a group as one of its admins.
func (c *Client) ManageGroup(ctx context.Context, id string) (ManagedGroup, error) {
	var group ManagedGroup
	if err := c.get(ctx, "/v1/groups/"+url.PathEscape(id)+"/manage", &group); err != nil {
		return ManagedGroup{}, err
	}
	return group, nil
}

// GroupEdit is a change to a group's published face.
type GroupEdit struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`

	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
	Place     string  `json:"place,omitempty"`
	Country   string  `json:"country,omitempty"`
	Zoom      int     `json:"zoom,omitempty"`
}

// ManageGroupAsDeveloper reads a group by identifier, with no token.
//
// The route exists only when the backend runs under --development; everywhere
// else this returns a 404 like any other address the API does not have.
func (c *Client) ManageGroupAsDeveloper(ctx context.Context, id string) (ManagedGroup, error) {
	var group ManagedGroup
	if err := c.get(ctx, "/v1/groups/"+url.PathEscape(id)+"/managed", &group); err != nil {
		return ManagedGroup{}, err
	}
	return group, nil
}

// EditGroup changes a group's name, description or meeting place.
//
// Queued reports that the change is waiting on a review rather than live: a
// published group stays on the map exactly as it was until somebody accepts
// the edit, and telling its author it was saved would be a lie they only
// discover by looking at the map.
func (c *Client) EditGroup(ctx context.Context, id string, edit GroupEdit) (queued bool, err error) {
	body, err := json.Marshal(edit)
	if err != nil {
		return false, fmt.Errorf("encode edit: %w", err)
	}

	var answer struct {
		Queued bool `json:"queued"`
	}
	path := "/v1/groups/" + url.PathEscape(id)
	if err := c.sendJSON(ctx, http.MethodPatch, path, body, &answer); err != nil {
		return false, err
	}
	return answer.Queued, nil
}

// GroupQueueItem is one group awaiting a decision.
type GroupQueueItem struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Place       string    `json:"place,omitempty"`
	Status      string    `json:"status"`
	Assessed    bool      `json:"assessed"`
	Confidence  int       `json:"confidence"`
	Reason      string    `json:"reason,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// GroupQueue is what is waiting, and how much of it no classifier has seen.
type GroupQueue struct {
	Items      []GroupQueueItem `json:"items"`
	Unassessed int              `json:"unassessed"`
}

// ListGroupQueue returns the groups awaiting a decision.
func (c *Client) ListGroupQueue(ctx context.Context) (GroupQueue, error) {
	var queue GroupQueue
	if err := c.get(ctx, "/v1/curation/groups", &queue); err != nil {
		return GroupQueue{}, err
	}
	return queue, nil
}

// GroupRevisionItem is a proposed change, beside what it would replace.
type GroupRevisionItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Place       string `json:"place,omitempty"`

	CurrentName        string `json:"current_name"`
	CurrentDescription string `json:"current_description,omitempty"`
	CurrentPlace       string `json:"current_place,omitempty"`

	Moved bool `json:"moved"`

	Status     string    `json:"status"`
	Assessed   bool      `json:"assessed"`
	Confidence int       `json:"confidence"`
	Reason     string    `json:"reason,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// GroupRevisionQueue is the edits awaiting a decision.
type GroupRevisionQueue struct {
	Items      []GroupRevisionItem `json:"items"`
	Unassessed int                 `json:"unassessed"`
}

// ListGroupRevisionQueue returns the edits to published groups.
func (c *Client) ListGroupRevisionQueue(ctx context.Context) (GroupRevisionQueue, error) {
	var queue GroupRevisionQueue
	if err := c.get(ctx, "/v1/curation/group-revisions", &queue); err != nil {
		return GroupRevisionQueue{}, err
	}
	return queue, nil
}

// AcceptGroupRevision applies an edit to its group.
func (c *Client) AcceptGroupRevision(ctx context.Context, actor, id, reason string) error {
	return c.decideGroupRevision(ctx, "accept", actor, id, reason)
}

// RejectGroupRevision discards an edit, leaving the group as it was.
func (c *Client) RejectGroupRevision(ctx context.Context, actor, id, reason string) error {
	return c.decideGroupRevision(ctx, "reject", actor, id, reason)
}

func (c *Client) decideGroupRevision(ctx context.Context, verb, actor, id, reason string) error {
	body, err := json.Marshal(map[string]string{"actor": actor, "reason": reason})
	if err != nil {
		return fmt.Errorf("encode decision: %w", err)
	}
	path := "/v1/curation/group-revisions/" + url.PathEscape(id) + "/" + verb
	return c.send(ctx, http.MethodPost, path, "application/json", body)
}

// GroupsPage is one page of the console's listing.
type GroupsPage struct {
	Groups  []GroupQueueItem `json:"groups"`
	Page    int              `json:"page"`
	PerPage int              `json:"per_page"`
	Total   int64            `json:"total"`
}

// ListAllGroups returns every group, page by page.
func (c *Client) ListAllGroups(ctx context.Context, page, perPage int) (GroupsPage, error) {
	path := fmt.Sprintf("/v1/curation/groups/all?page=%d&per_page=%d", page, perPage)

	var listing GroupsPage
	if err := c.get(ctx, path, &listing); err != nil {
		return GroupsPage{}, err
	}
	return listing, nil
}

// AcceptGroup puts a group on the map.
func (c *Client) AcceptGroup(ctx context.Context, actor, id, reason string) error {
	return c.decideGroup(ctx, "accept", actor, id, reason)
}

// RejectGroup refuses a group, which deletes it and the address that created
// it.
func (c *Client) RejectGroup(ctx context.Context, actor, id, reason string) error {
	return c.decideGroup(ctx, "reject", actor, id, reason)
}

func (c *Client) decideGroup(ctx context.Context, verb, actor, id, reason string) error {
	body, err := json.Marshal(map[string]string{"actor": actor, "reason": reason})
	if err != nil {
		return fmt.Errorf("encode decision: %w", err)
	}
	path := "/v1/curation/groups/" + url.PathEscape(id) + "/" + verb
	return c.send(ctx, http.MethodPost, path, "application/json", body)
}

// Member is one person in a group, as whoever manages it sees them.
type Member struct {
	// AccountID names the person for the purpose of a role grant, and for
	// nothing else. It is a random identifier that says nothing about who
	// holds it.
	AccountID string `json:"account_id"`

	Name string `json:"name,omitempty"`

	// Role is empty for an ordinary member.
	Role string `json:"role,omitempty"`

	JoinedAt string `json:"joined_at"`
}

// JoinGroup joins the reader to a group.
//
// A press: there is no code, no address, and nothing left to prove that a
// passkey has not already proved.
func (c *Client) JoinGroup(ctx context.Context, id string) (Group, error) {
	var group Group
	path := "/v1/groups/" + url.PathEscape(id) + "/join"
	if err := c.postJSON(ctx, path, []byte("{}"), &group); err != nil {
		return Group{}, err
	}
	return group, nil
}

// LeaveGroup takes the reader out of a group.
func (c *Client) LeaveGroup(ctx context.Context, id string) (Group, error) {
	var group Group
	path := "/v1/groups/" + url.PathEscape(id) + "/join"
	if err := c.sendJSON(ctx, http.MethodDelete, path, []byte("{}"), &group); err != nil {
		return Group{}, err
	}
	return group, nil
}

// GroupMembers reads who has joined, as one of the group's admins.
func (c *Client) GroupMembers(ctx context.Context, id string) ([]Member, error) {
	var answer struct {
		Members []Member `json:"members"`
	}
	path := "/v1/groups/" + url.PathEscape(id) + "/members"
	if err := c.get(ctx, path, &answer); err != nil {
		return nil, err
	}
	return answer.Members, nil
}

// SetMemberRole grants or takes back a role. An empty role is an ordinary
// member, and taking one back leaves the membership alone.
func (c *Client) SetMemberRole(ctx context.Context, id, accountID, role string) ([]Member, error) {
	body, err := json.Marshal(map[string]string{"role": role})
	if err != nil {
		return nil, fmt.Errorf("encode role: %w", err)
	}

	var answer struct {
		Members []Member `json:"members"`
	}
	path := "/v1/groups/" + url.PathEscape(id) + "/members/" + url.PathEscape(accountID)
	if err := c.sendJSON(ctx, http.MethodPatch, path, body, &answer); err != nil {
		return nil, err
	}
	return answer.Members, nil
}

// GroupMessage is one message in a group's inbox.
type GroupMessage struct {
	ID string `json:"id"`

	// From is the name the sender chose, and all there is to know about them.
	From string `json:"from,omitempty"`

	Text      string    `json:"text"`
	Read      bool      `json:"read"`
	CreatedAt time.Time `json:"created_at"`
}

// WriteToGroup sends a message to a group's admins.
//
// It reports nothing about delivery, and there is nothing to report: whether an
// admin has a live push subscription is a fact about their devices, and an
// answer that varied with it would leak one.
func (c *Client) WriteToGroup(ctx context.Context, id, text string) error {
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return fmt.Errorf("encode message: %w", err)
	}

	var answer struct {
		Sent bool `json:"sent"`
	}
	path := "/v1/groups/" + url.PathEscape(id) + "/messages"
	return c.postJSON(ctx, path, body, &answer)
}

// GroupMessages is a group's inbox, as one of its admins.
func (c *Client) GroupMessages(ctx context.Context, id string) ([]GroupMessage, int64, error) {
	var answer struct {
		Items  []GroupMessage `json:"items"`
		Unread int64          `json:"unread"`
	}
	path := "/v1/groups/" + url.PathEscape(id) + "/messages"
	if err := c.get(ctx, path, &answer); err != nil {
		return nil, 0, err
	}
	return answer.Items, answer.Unread, nil
}

// ReadGroupMessage marks one message read.
func (c *Client) ReadGroupMessage(ctx context.Context, id, messageID string) error {
	path := "/v1/groups/" + url.PathEscape(id) + "/messages/" +
		url.PathEscape(messageID) + "/read"
	var ignored json.RawMessage
	return c.postJSON(ctx, path, []byte("{}"), &ignored)
}

// Action is one thing a group does.
type Action struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type"`

	StartsOn string `json:"starts_on,omitempty"`
	StartsAt string `json:"starts_at,omitempty"`

	// Rule is the RFC 5545 recurrence rule, Note the nuance it cannot carry,
	// and Parts the same rule broken up so a page can put it into a sentence.
	Rule  string          `json:"rule,omitempty"`
	Note  string          `json:"note,omitempty"`
	Parts *RecurrenceInfo `json:"parts,omitempty"`

	// NextOn is when this next happens. Derived and stored by the backend,
	// never computed here.
	NextOn string `json:"next_on,omitempty"`

	// Latitude, Longitude and Elsewhere are where it happens. Elsewhere says
	// the action has a venue of its own rather than the group's meeting place,
	// which a form cannot work out for itself: an inherited place is a copy.
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
	Elsewhere bool    `json:"elsewhere"`

	Place string `json:"place,omitempty"`

	Status     string `json:"status"`
	Retired    bool   `json:"retired"`
	Assessed   bool   `json:"assessed"`
	Confidence int    `json:"confidence"`
	Reason     string `json:"reason,omitempty"`
}

// ActionDraft is an announcement on its way in.
//
// It carried a `token` until actions moved from the group's management token to
// its roles. The field survived the move because nothing type-checks a JSON
// body against the endpoint that receives it — and the backend, which refuses
// properties it does not declare, was the thing that noticed.
type ActionDraft struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type"`

	StartsOn   string `json:"starts_on,omitempty"`
	StartsTime string `json:"starts_time,omitempty"`

	// The rhythm as the form's own choices. The RRULE is assembled by the
	// backend: no field here accepts one directly.
	Repeat   string `json:"repeat,omitempty"`
	Interval int    `json:"interval,omitempty"`
	Weekdays []int  `json:"weekdays,omitempty"`
	Monthly  string `json:"monthly,omitempty"`
	Week     int    `json:"week,omitempty"`
	Weekday  int    `json:"weekday,omitempty"`
	Day      int    `json:"day,omitempty"`
	Month    int    `json:"month,omitempty"`
	Note     string `json:"note,omitempty"`

	// Elsewhere says the action happens somewhere other than the group's
	// meeting place. Sent even when false — that is what moves one back.
	Elsewhere bool `json:"elsewhere"`

	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
	Place     string  `json:"place,omitempty"`
	Country   string  `json:"country,omitempty"`
	Zoom      int     `json:"zoom,omitempty"`
}

// RecurrenceInfo is a rhythm broken into the pieces a sentence needs, so a
// page can say it in its reader's language rather than showing an RRULE.
type RecurrenceInfo struct {
	Key      string `json:"key"`
	Interval int    `json:"interval,omitempty"`
	Weekdays []int  `json:"weekdays,omitempty"`
	Week     int    `json:"week,omitempty"`
	Day      int    `json:"day,omitempty"`
	Month    int    `json:"month,omitempty"`
}

// The accessors a template needs. Methods rather than exported fields alone,
// so the web package can describe what it needs as an interface without
// importing this one — and so a nil rhythm answers usefully instead of
// panicking in the middle of a page.
func (r *RecurrenceInfo) GetInterval() int {
	if r == nil {
		return 0
	}
	return r.Interval
}

func (r *RecurrenceInfo) GetWeekdays() []int {
	if r == nil {
		return nil
	}
	return r.Weekdays
}

func (r *RecurrenceInfo) GetWeek() int {
	if r == nil {
		return 0
	}
	return r.Week
}

func (r *RecurrenceInfo) GetDay() int {
	if r == nil {
		return 0
	}
	return r.Day
}

func (r *RecurrenceInfo) GetMonth() int {
	if r == nil {
		return 0
	}
	return r.Month
}

// Yearly reports whether the rhythm comes round once a year.
func (r *RecurrenceInfo) Yearly() bool {
	if r == nil {
		return false
	}
	return r.Month != 0
}

// Monthly reports whether the rhythm is a monthly one.
func (r *RecurrenceInfo) Monthly() bool {
	if r == nil {
		return false
	}
	return r.Week != 0 || r.Day != 0
}

// PublicActions is what a group is offering, for the group page.
func (c *Client) PublicActions(ctx context.Context, id string) ([]Action, error) {
	var answer struct {
		Actions []Action `json:"actions"`
	}
	if err := c.get(ctx, "/v1/groups/"+url.PathEscape(id)+"/actions", &answer); err != nil {
		return nil, err
	}
	return answer.Actions, nil
}

// GroupActions is everything a group has announced, at any status.
func (c *Client) GroupActions(ctx context.Context, id string) ([]Action, error) {
	var answer struct {
		Actions []Action `json:"actions"`
	}
	path := "/v1/groups/" + url.PathEscape(id) + "/actions/all"
	if err := c.get(ctx, path, &answer); err != nil {
		return nil, err
	}
	return answer.Actions, nil
}

// CreateAction announces one.
func (c *Client) CreateAction(ctx context.Context, id string, draft ActionDraft) ([]Action, error) {
	body, err := json.Marshal(draft)
	if err != nil {
		return nil, fmt.Errorf("encode action: %w", err)
	}

	var answer struct {
		Actions []Action `json:"actions"`
	}
	path := "/v1/groups/" + url.PathEscape(id) + "/actions"
	if err := c.postJSON(ctx, path, body, &answer); err != nil {
		return nil, err
	}
	return answer.Actions, nil
}

// EditAction changes one, which sends it back through assessment.
func (c *Client) EditAction(ctx context.Context, id, actionID string, draft ActionDraft) ([]Action, error) {
	body, err := json.Marshal(draft)
	if err != nil {
		return nil, fmt.Errorf("encode action: %w", err)
	}

	var answer struct {
		Actions []Action `json:"actions"`
	}
	path := "/v1/groups/" + url.PathEscape(id) + "/actions/" + url.PathEscape(actionID)
	if err := c.sendJSON(ctx, http.MethodPatch, path, body, &answer); err != nil {
		return nil, err
	}
	return answer.Actions, nil
}

// DeleteAction calls one off.
func (c *Client) DeleteAction(ctx context.Context, id, actionID string) ([]Action, error) {
	return c.actionCall(ctx, http.MethodDelete,
		"/v1/groups/"+url.PathEscape(id)+"/actions/"+url.PathEscape(actionID))
}

// ConfirmAction says a recurrent action still happens.
func (c *Client) ConfirmAction(ctx context.Context, id, actionID string) ([]Action, error) {
	return c.actionCall(ctx, http.MethodPost,
		"/v1/groups/"+url.PathEscape(id)+"/actions/"+url.PathEscape(actionID)+"/confirm")
}

func (c *Client) actionCall(ctx context.Context, method, path string) ([]Action, error) {
	var answer struct {
		Actions []Action `json:"actions"`
	}
	if err := c.sendJSON(ctx, method, path, []byte("{}"), &answer); err != nil {
		return nil, err
	}
	return answer.Actions, nil
}

// ActionQueueItem is one announcement awaiting a decision, with whose it is.
type ActionQueueItem struct {
	Action

	Group      string `json:"group"`
	GroupID    string `json:"group_id"`
	GroupPlace string `json:"group_place,omitempty"`
}

// ActionQueue is what is waiting.
type ActionQueue struct {
	Items      []ActionQueueItem `json:"items"`
	Unassessed int               `json:"unassessed"`
}

// ListActionQueue returns the announcements awaiting a curator.
func (c *Client) ListActionQueue(ctx context.Context) (ActionQueue, error) {
	var queue ActionQueue
	if err := c.get(ctx, "/v1/curation/actions", &queue); err != nil {
		return ActionQueue{}, err
	}
	return queue, nil
}

// AcceptAction publishes one.
func (c *Client) AcceptAction(ctx context.Context, actor, id, reason string) error {
	return c.decideAction(ctx, "accept", actor, id, reason)
}

// RejectAction refuses one, which deletes it. The group is untouched.
func (c *Client) RejectAction(ctx context.Context, actor, id, reason string) error {
	return c.decideAction(ctx, "reject", actor, id, reason)
}

func (c *Client) decideAction(ctx context.Context, verb, actor, id, reason string) error {
	body, err := json.Marshal(map[string]string{"actor": actor, "reason": reason})
	if err != nil {
		return fmt.Errorf("encode decision: %w", err)
	}
	path := "/v1/curation/actions/" + url.PathEscape(id) + "/" + verb
	return c.send(ctx, http.MethodPost, path, "application/json", body)
}
