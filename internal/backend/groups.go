package backend

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/cache"
	"github.com/CoderSyndicate/doleances/internal/content"
	"github.com/CoderSyndicate/doleances/internal/geo"
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/store"
)

// What a group's own words may be.
const (
	maxGroupNameRunes        = 120
	maxGroupDescriptionRunes = 2000
)

func (a *API) registerGroupRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "check-group-name",
		Method:      http.MethodGet,
		Path:        "/v1/groups/available",
		Summary:     "Is this group name free?",
		Description: "Two groups cannot share a name. Checked against live groups and against " +
			"the edits waiting on a decision, so somebody filling the form finds out at the " +
			"keystroke rather than at the write.",
		Tags: []string{"Groups"},
	}, a.checkGroupName)

	huma.Register(api, invalidates(cache.Groups)(authenticated(huma.Operation{
		OperationID: "create-group",
		Method:      http.MethodPost,
		Path:        "/v1/groups",
		Summary:     "Propose a local action group",
		Description: "The account that proposes a group becomes its first **admin**, and " +
			"nothing about that is permanent: a group must never depend on one " +
			"irreplaceable person.\n\n" +
			"There is no confirmation link and no management token any more. A group used to " +
			"prove a mailbox answered before it entered the pipeline, because the address " +
			"*was* the identity; an account has proved itself with a passkey and carries no " +
			"address to prove. What is left as the barrier is the curation queue — said " +
			"plainly, because it is a real loss.",
		Tags: []string{"Groups"},
	})), a.createGroup)

	huma.Register(api, huma.Operation{
		OperationID: "list-groups",
		Method:      http.MethodGet,
		Path:        "/v1/groups/map",
		Summary:     "The groups on the map",
		Description: "Accepted and visible only. A group with no pin is absent whatever the " +
			"viewport: unlike a doléance that named only a country, a meeting place with no " +
			"place is not a meeting place.",
		Tags: []string{"Groups"},
	}, a.listGroups)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "list-my-groups",
		Method:      http.MethodGet,
		Path:        "/v1/accounts/me/groups",
		Summary:     "The groups this account belongs to",
		Tags:        []string{"Groups"},
	}), a.listMyGroups)

	huma.Register(api, huma.Operation{
		OperationID: "get-group",
		Method:      http.MethodGet,
		Path:        "/v1/groups/{id}",
		Summary:     "One group",
		Description: "Resolves at any status. Leaving the map is not disappearing: somebody " +
			"holding the link reaches a page that says where the group stands, and that page " +
			"is how a hidden group comes back.\n\n" +
			"A signed-in caller is also told what they may do here, so the page can offer " +
			"leaving rather than joining without a second request.",
		Tags: []string{"Groups"},
	}, a.getGroup)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "manage-group",
		Method:      http.MethodGet,
		Path:        "/v1/groups/{id}/manage",
		Summary:     "Read a group as one of its admins",
		Description: "Behind being an admin of this group, which is the only credential a " +
			"group has now. The unrecoverable management token is gone, and with it the " +
			"`--development` bypass that existed only because it was a hash.",
		Tags: []string{"Groups"},
	}), a.manageGroup)

	huma.Register(api, invalidates(cache.Groups)(authenticated(huma.Operation{
		OperationID: "edit-group",
		Method:      http.MethodPatch,
		Path:        "/v1/groups/{id}",
		Summary:     "Change a group's name, description or meeting place",
		Description: "Sends it back through assessment: the text a model scored is no longer " +
			"the text on the page. A pending group is changed in place; a published one " +
			"produces a revision and stays on the map, unaltered, until somebody accepts " +
			"the change. The answer says which happened.",
		Tags: []string{"Groups"},
	})), a.editGroup)
}

// GroupNameInput asks about one name.
type GroupNameInput struct {
	Name string `query:"name"`
}

// GroupNameOutput answers.
type GroupNameOutput struct {
	Body struct {
		Available bool `json:"available"`
	}
}

func (a *API) checkGroupName(ctx context.Context, in *GroupNameInput) (*GroupNameOutput, error) {
	out := &GroupNameOutput{}

	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > maxGroupNameRunes {
		return out, nil
	}

	available, err := a.store.GroupNameAvailable(ctx, name)
	if err != nil {
		log.Error().Err(err).Msg("cannot check a group name")
		return nil, huma.Error500InternalServerError("cannot check that name")
	}
	out.Body.Available = available
	return out, nil
}

// CreateGroupInput is a proposed group.
type CreateGroupInput struct {
	Body struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`

		Latitude  float64 `json:"latitude,omitempty"`
		Longitude float64 `json:"longitude,omitempty"`
		Place     string  `json:"place,omitempty"`
		Country   string  `json:"country,omitempty"`
		Zoom      int     `json:"zoom,omitempty"`
	}
}

// resolveVenue turns a pinned point into a meeting place.
//
// It is deliberately *not* resolveLocation, and the two must not be merged
// back together however similar they look. A doléance's place is coarsened to
// a geohash-6 cell because the row it sits in also carries a birth year and an
// activity, and those three together identify a person. A group's row carries
// no person at all — it carries a pub, a hall or a square that somebody has to
// be able to find.
//
// Reusing the doléance resolver here was a real bug, not a theoretical one: a
// group pinned on a bar in Düsseldorf was stored as the centre of its cell and
// drawn 300 metres up the street, under the label "Düsseldorf, Stadtbezirk 1".
// Half a geohash-6 cell is about 300 metres, so that is exactly what the
// coarsening is worth when there is nobody to protect with it.
//
// So the coordinates are stored as pinned. What still varies with the map zoom
// is how much address detail is asked for, because a click on a city view is
// not somebody naming a building.
func (a *API) resolveVenue(ctx context.Context, lat, lng float64, zoom int, place, country string) *models.Location {
	precision := geo.VenuePrecisionForZoom(zoom)

	location := &models.Location{Label: place, CountryCode: country}

	// The one thing the doléance rule got right for both: a pin dropped on an
	// untouched country view is not a point, and the centre of a 1250 km cell
	// is not near it. A group with no findable address keeps its name and
	// stays off the map, which is the truth rather than a gap.
	if precision >= geo.MappablePrecision {
		location.Latitude, location.Longitude = lat, lng
	}

	if a.geocoder == nil {
		return location
	}

	named, err := a.geocoder.Reverse(ctx, lat, lng, precision)
	if err != nil {
		log.Warn().Err(err).Msg("reverse geocoding failed; storing the venue unnamed")
		return location
	}
	if named.Label != "" {
		location.Label = named.Label
	}
	if named.CountryCode != "" {
		location.CountryCode = named.CountryCode
	}
	return location
}

// CreateGroupOutput is the group as it now stands: pending, invisible, and
// waiting on the same pipeline everything else goes through.
type CreateGroupOutput struct {
	Body GroupItem
}

func (a *API) createGroup(ctx context.Context, in *CreateGroupInput) (*CreateGroupOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	body := in.Body

	name, _ := content.Sanitise(body.Name)
	name = strings.TrimSpace(name)
	description, _ := content.Sanitise(body.Description)
	description = strings.TrimSpace(description)

	if name == "" {
		return nil, huma.Error422UnprocessableEntity("a group needs a name")
	}
	if utf8.RuneCountInString(name) > maxGroupNameRunes {
		return nil, huma.Error422UnprocessableEntity("that name is too long")
	}
	if utf8.RuneCountInString(description) > maxGroupDescriptionRunes {
		return nil, huma.Error422UnprocessableEntity("that description is too long")
	}

	// The same guard the register applies to a doléance: a name and a
	// description are rendered on every card that shows the group.
	for field, value := range map[string]string{"name": name, "description": description} {
		if content.ContainsExecutablePayload(value) {
			log.Warn().Str("field", field).
				Msg("group: refused before assessment, it carries executable content")
			return nil, huma.Error422UnprocessableEntity("that cannot be saved")
		}
	}

	group := &models.Group{Name: name, Description: description}
	if body.Latitude != 0 || body.Longitude != 0 {
		group.Location = *a.resolveVenue(ctx, body.Latitude, body.Longitude, body.Zoom,
			trimTo(body.Place, maxNicknameRunes), strings.ToUpper(trimTo(body.Country, 2)))
	}

	if err := a.store.CreateGroup(ctx, group, who.Account.ID); err != nil {
		if errors.Is(err, store.ErrNameTaken) {
			return nil, huma.Error409Conflict("that name is already taken")
		}
		log.Error().Err(err).Msg("cannot create a group")
		return nil, huma.Error500InternalServerError("cannot record the group")
	}

	log.Info().Str("group", group.ID).Str("name", group.Name).
		Msg("group proposed and queued for assessment")
	return &CreateGroupOutput{Body: toGroupItem(*group)}, nil
}

// GroupItem is a group as the API reports it.
//
// A deliberate allowlist rather than the model. It used to be one because the
// row carried a token hash and its contacts' email addresses; it stays one
// because an allowlist is the only shape that cannot leak a column somebody
// adds later without thinking about who reads it.
type GroupItem struct {
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
}

func toGroupItem(group models.Group) GroupItem {
	return GroupItem{
		ID:          group.ID,
		Name:        group.Name,
		Description: group.Description,
		Status:      string(group.Status),
		Visible:     group.Visible,
		Place:       group.Location.Label,
		Latitude:    group.Location.Latitude,
		Longitude:   group.Location.Longitude,
		Geohash:     group.Location.Geohash,
		CreatedAt:   group.CreatedAt,
	}
}

// GroupListInput bounds the map listing.
type GroupListInput struct {
	// Bounds is "north,south,east,west" in degrees, the same contract the
	// register uses — one parameter, because the four are meaningless apart.
	Bounds string `query:"bounds" doc:"north,south,east,west in degrees"`
	Limit  int    `query:"limit" default:"200"`
}

// GroupListOutput is the map.
type GroupListOutput struct {
	Body struct {
		Groups []GroupItem `json:"groups"`

		// Total is how many groups there are in the world, whatever the
		// viewport shows. It is not a pagination detail: the number of groups
		// and their spread is the argument this map is making, and a reader
		// who sees four pins in their département without learning there are
		// nine hundred elsewhere has been told the opposite of the truth.
		Total int64 `json:"total"`
	}
}

func (a *API) listGroups(ctx context.Context, in *GroupListInput) (*GroupListOutput, error) {
	query := store.GroupQuery{Limit: in.Limit}
	if in.Bounds != "" {
		box, err := parseBounds(in.Bounds)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("bounds must be north,south,east,west")
		}
		query.Bounds = &box
	}

	type mapping struct {
		groups []models.Group
		total  int64
	}
	found, err := cache.Fetch(a.cache,
		cache.Keyed("groups.map", in.Bounds, strconv.Itoa(in.Limit)),
		cache.Groups, func() (mapping, error) {
			groups, total, err := a.store.FindGroups(ctx, query)
			return mapping{groups, total}, err
		})
	groups, total := found.groups, found.total
	if err != nil {
		log.Error().Err(err).Msg("cannot list the groups")
		return nil, huma.Error500InternalServerError("cannot read the groups")
	}

	out := &GroupListOutput{}
	out.Body.Total = total
	out.Body.Groups = make([]GroupItem, 0, len(groups))
	for _, group := range groups {
		out.Body.Groups = append(out.Body.Groups, toGroupItem(group))
	}
	return out, nil
}

// MyGroupItem is one of somebody's own groups, with what they may do in it.
type MyGroupItem struct {
	GroupItem

	// Role is empty for an ordinary member, who joins actions and nothing
	// more.
	Role string `json:"role,omitempty"`

	JoinedAt time.Time `json:"joined_at"`

	// Unread is how many messages a group has not looked at. Filled only for
	// somebody who can read them, which is an admin.
	Unread int64 `json:"unread,omitempty"`
}

// MyGroupsOutput is somebody's own groups.
type MyGroupsOutput struct {
	Body struct {
		Groups []MyGroupItem `json:"groups"`
	}
}

func (a *API) listMyGroups(ctx context.Context, _ *struct{}) (*MyGroupsOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	memberships, err := a.store.GroupsOf(ctx, who.Account.ID)
	if err != nil {
		log.Error().Err(err).Msg("cannot read an account's groups")
		return nil, huma.Error500InternalServerError("cannot read the groups")
	}

	out := &MyGroupsOutput{}
	out.Body.Groups = make([]MyGroupItem, 0, len(memberships))
	for _, membership := range memberships {
		group, err := a.store.GetGroup(ctx, membership.GroupID)
		if err != nil {
			// A membership whose group is gone is a row to skip, not a page to
			// fail: the rest of somebody's groups are still theirs to read.
			log.Warn().Err(err).Str("group", membership.GroupID).
				Msg("a membership points at a group that is not there")
			continue
		}

		item := MyGroupItem{
			GroupItem: toGroupItem(group),
			Role:      string(membership.Role),
			JoinedAt:  membership.JoinedAt,
		}
		if membership.Role == models.RoleAdmin {
			if unread, err := a.store.CountUnreadGroupMessages(ctx, group.ID); err == nil {
				item.Unread = unread
			}
		}
		out.Body.Groups = append(out.Body.Groups, item)
	}
	return out, nil
}

// GroupOutput is one group, with what the caller may do in it.
type GroupOutput struct {
	Body struct {
		GroupItem

		// Role is what a signed-in caller may do here: "admin", "host", or
		// empty. Absent entirely for a reader who is not signed in.
		Role string `json:"role,omitempty"`

		// Member says whether the caller has joined, which is a different
		// question from what role they hold — every admin is a member, and
		// most members hold no role at all.
		Member bool `json:"member"`
	}
}

func (a *API) getGroup(ctx context.Context, in *MessageIDInput) (*GroupOutput, error) {
	group, err := cache.Fetch(a.cache, cache.Keyed("groups.one", in.ID),
		cache.Groups, func() (models.Group, error) {
			return a.store.GetGroup(ctx, in.ID)
		})
	if errors.Is(err, store.ErrGroupNotFound) {
		return nil, huma.Error404NotFound("no such group")
	}
	if err != nil {
		log.Error().Err(err).Str("group", in.ID).Msg("cannot read a group")
		return nil, huma.Error500InternalServerError("cannot read the group")
	}

	out := &GroupOutput{}
	out.Body.GroupItem = toGroupItem(group)

	// Answered in the same request rather than by a second call, so the page
	// cannot show "join" to somebody who has already joined.
	if who := callerOf(ctx); who != nil {
		membership, err := a.store.MembershipOf(ctx, group.ID, who.Account.ID)
		switch {
		case err == nil:
			out.Body.Member = true
			out.Body.Role = string(membership.Role)
		case errors.Is(err, store.ErrNotAMember):
		default:
			log.Error().Err(err).Msg("cannot read a membership")
		}
	}
	return out, nil
}

// ManagedGroupOutput is the group as one of its admins sees it.
type ManagedGroupOutput struct {
	Body struct {
		GroupItem

		// Members is who has joined, which is the list this page manages.
		// There is no address in it and nothing else to put there: an account
		// is a chosen name and nothing more, so what a group's admins see
		// about their own members is what those members decided to be called.
		Members []MemberItem `json:"members"`

		// Unread is how many messages the group has not looked at.
		Unread int64 `json:"unread"`
	}
}

func (a *API) manageGroup(ctx context.Context, in *MessageIDInput) (*ManagedGroupOutput, error) {
	group, err := a.groupAdmin(ctx, in.ID)
	if err != nil {
		return nil, err
	}

	members, err := a.store.ListGroupMembers(ctx, group.ID)
	if err != nil {
		log.Error().Err(err).Str("group", group.ID).Msg("cannot read the members")
		return nil, huma.Error500InternalServerError("cannot read the group")
	}

	out := &ManagedGroupOutput{}
	out.Body.GroupItem = toGroupItem(group)
	out.Body.Members = toMemberItems(members)
	if unread, err := a.store.CountUnreadGroupMessages(ctx, group.ID); err == nil {
		out.Body.Unread = unread
	}
	return out, nil
}

// EditGroupInput changes a group's published face.
type EditGroupInput struct {
	ID   string `path:"id"`
	Body struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`

		// The meeting place, optional. A form that posted no map leaves the
		// pin where it is rather than unpinning the group.
		Latitude  float64 `json:"latitude,omitempty"`
		Longitude float64 `json:"longitude,omitempty"`
		Place     string  `json:"place,omitempty"`
		Country   string  `json:"country,omitempty"`
		Zoom      int     `json:"zoom,omitempty"`
	}
}

// EditGroupOutput is the group, plus what actually happened to the edit.
type EditGroupOutput struct {
	Body struct {
		GroupItem

		// Queued says the change is waiting on a review rather than live.
		//
		// It exists because the two outcomes look identical otherwise, and
		// telling somebody their edit is saved when a curator has not seen it
		// is how a group ends up wondering why the map still says the old
		// thing.
		Queued bool `json:"queued"`
	}
}

func (a *API) editGroup(ctx context.Context, in *EditGroupInput) (*EditGroupOutput, error) {
	if _, err := a.groupAdmin(ctx, in.ID); err != nil {
		return nil, err
	}

	name, _ := content.Sanitise(in.Body.Name)
	name = strings.TrimSpace(name)
	description, _ := content.Sanitise(in.Body.Description)
	description = strings.TrimSpace(description)

	if name == "" || utf8.RuneCountInString(name) > maxGroupNameRunes {
		return nil, huma.Error422UnprocessableEntity("a group needs a name")
	}
	if utf8.RuneCountInString(description) > maxGroupDescriptionRunes {
		return nil, huma.Error422UnprocessableEntity("that description is too long")
	}
	for _, value := range []string{name, description} {
		if content.ContainsExecutablePayload(value) {
			return nil, huma.Error422UnprocessableEntity("that cannot be saved")
		}
	}

	edit := store.GroupEdit{Name: name, Description: description}
	// A pin only counts when one was actually sent. Nobody moves a group to
	// Null Island, so a zero pair is an absent field rather than a location.
	if in.Body.Latitude != 0 || in.Body.Longitude != 0 {
		edit.Location = a.resolveVenue(ctx, in.Body.Latitude, in.Body.Longitude, in.Body.Zoom,
			trimTo(in.Body.Place, maxNicknameRunes), strings.ToUpper(trimTo(in.Body.Country, 2)))
	}

	group, applied, err := a.store.EditGroup(ctx, in.ID, edit)
	switch {
	case errors.Is(err, store.ErrNameTaken):
		return nil, huma.Error409Conflict("that name is already taken")
	case errors.Is(err, store.ErrGroupNotFound):
		return nil, huma.Error404NotFound("no such group")
	case err != nil:
		log.Error().Err(err).Str("group", in.ID).Msg("cannot edit a group")
		return nil, huma.Error500InternalServerError("cannot save the group")
	}

	out := &EditGroupOutput{}
	out.Body.GroupItem = toGroupItem(group)
	out.Body.Queued = !applied

	if applied {
		log.Info().Str("group", group.ID).
			Msg("group edited by an admin and re-queued for assessment")
	} else {
		// The published group is untouched and still on the map; what was
		// written is a revision waiting on a decision.
		log.Info().Str("group", group.ID).
			Msg("edit to a published group recorded as a revision; the group is unchanged")
	}
	return out, nil
}

// groupAdmin resolves the caller against the group they claim to manage.
//
// Against *that* group, never merely "is an admin somewhere": an admin of one
// group must not reach another, and checking only that somebody holds the role
// would let them.
//
// This replaced the management token, and the replacement is a simplification
// rather than a swap. The token was 32 random bytes shown once, unrecoverable
// by anybody including the operator — which meant the console could not build
// a link to a management page it was listing, and a `--development` bypass
// existed for that reason alone. Being an admin is recoverable by being the
// person, so both are gone.
func (a *API) groupAdmin(ctx context.Context, groupID string) (models.Group, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return models.Group{}, err
	}

	group, err := a.store.GetGroup(ctx, groupID)
	if errors.Is(err, store.ErrGroupNotFound) {
		return models.Group{}, huma.Error404NotFound("no such group")
	}
	if err != nil {
		log.Error().Err(err).Str("group", groupID).Msg("cannot read a group")
		return models.Group{}, huma.Error500InternalServerError("cannot read the group")
	}

	membership, err := a.store.MembershipOf(ctx, groupID, who.Account.ID)
	if errors.Is(err, store.ErrNotAMember) || (err == nil && membership.Role != models.RoleAdmin) {
		// 404 rather than 403, so somebody working through identifiers cannot
		// tell a group they may not manage from one that does not exist.
		return models.Group{}, huma.Error404NotFound("no such group")
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot read a membership")
		return models.Group{}, huma.Error500InternalServerError("cannot read the group")
	}
	return group, nil
}

// groupHost resolves the caller against a group whose actions they claim to
// manage.
//
// Hosts may manage actions and not the group, which is the whole reason the
// role exists: somebody can be trusted to post next month's meeting without
// being trusted to rename the group or hand it to somebody else.
func (a *API) groupHost(ctx context.Context, groupID string) (models.Group, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return models.Group{}, err
	}

	group, err := a.store.GetGroup(ctx, groupID)
	if errors.Is(err, store.ErrGroupNotFound) {
		return models.Group{}, huma.Error404NotFound("no such group")
	}
	if err != nil {
		log.Error().Err(err).Str("group", groupID).Msg("cannot read a group")
		return models.Group{}, huma.Error500InternalServerError("cannot read the group")
	}

	membership, err := a.store.MembershipOf(ctx, groupID, who.Account.ID)
	switch {
	case err == nil && (membership.Role == models.RoleAdmin || membership.Role == models.RoleHost):
		return group, nil
	case err == nil, errors.Is(err, store.ErrNotAMember):
		return models.Group{}, huma.Error404NotFound("no such group")
	default:
		log.Error().Err(err).Msg("cannot read a membership")
		return models.Group{}, huma.Error500InternalServerError("cannot read the group")
	}
}
