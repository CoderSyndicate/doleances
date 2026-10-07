// Package authentik talks to the identity provider the console's staff
// authenticate against.
//
// # Hand-rolled, like internal/llm
//
// authentik publishes an OpenAPI schema and a generated client for it. The
// generated client is enormous — hundreds of models for an API this uses nine
// endpoints of — and it would arrive with its own HTTP handling, its own
// retries and its own idea of logging, none of which match what the rest of
// this project does. Nine calls are cheaper to write than a dependency to keep
// in step.
//
// Every field name and every required field below was read out of authentik's
// own `schema.yml` rather than remembered, which matters more than usual here:
// nothing in this package can be tested against a real instance from a unit
// test, so the shapes are the part most likely to be wrong and the part worth
// being certain about.
//
// # What it is allowed to touch
//
// This register's own objects: its OAuth2 provider, its application, its two
// groups, and the people in them. It creates no flows, edits no flow bindings
// and changes nothing instance-wide. An operator's authentication flow is
// shared with every other application in their directory, and a tool that
// provisions itself has no business rewriting it — so where this needs
// something instance-wide it **checks and reports** rather than configuring.
// See Preconditions.
package authentik

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// requestTimeout bounds one call. Generous: an admin is watching a wizard, and
// a directory that takes four seconds to answer is slow rather than broken.
const requestTimeout = 30 * time.Second

// ErrNotFound is "the directory has no such thing", distinct from a call that
// failed. Several operations here are find-or-create and need to tell those
// apart.
var ErrNotFound = errors.New("not found in the directory")

// ErrTokenRefused is authentik refusing this console's own API token.
//
// Its commonest cause is expiry, and a live installation met it: authentik's
// default duration for a new API token is **thirty minutes**, so a console
// that kept the token pasted into its wizard worked perfectly and then refused
// everybody at once. The console now mints a credential of its own instead —
// see EnsureConsoleToken — so this should be a revocation rather than an
// expiry, and either way it is an operator's problem and nobody else's.
var ErrTokenRefused = errors.New("authentik refused this console's API token")

// ErrWouldEscalate is refusing to mint a credential for a directory superuser.
//
// # The path this closes
//
// A recovery link lets whoever holds it set that account's credential. This
// console mints them with a token belonging to whoever provisioned it —
// usually a directory superuser — so without this rule an **admin of this
// register** could name `akadmin` on the people page and be handed a link that
// takes over the operator's whole authentik.
//
// That is a larger power than the role they were given. Running a register's
// curation queue is not the same job as administering the directory the
// register borrows its identities from, and this console must not be a bridge
// between the two.
//
// It refuses the **credential, not the role**: a superuser may perfectly well
// also be this register's admin, and granting that is somebody saying so in
// their own directory. What they do not need is this console offering to reset
// them.
var ErrWouldEscalate = errors.New("that account administers the directory itself")

// Client is an authenticated connection to one authentik instance.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New builds a client against an instance URL and an API token.
//
// The URL is what an operator types, so it is normalised rather than trusted
// to be exact: a trailing slash, a path, or `/api/v3` already on the end are
// all things somebody will paste, and refusing them teaches nothing.
func New(instanceURL, token string) (*Client, error) {
	instanceURL = strings.TrimSpace(instanceURL)
	token = strings.TrimSpace(token)

	if instanceURL == "" {
		return nil, errors.New("the authentik instance URL is required")
	}
	if token == "" {
		return nil, errors.New("an authentik API token is required")
	}

	parsed, err := url.Parse(instanceURL)
	if err != nil {
		return nil, fmt.Errorf("the authentik instance URL is not a URL: %w", err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, fmt.Errorf("the authentik instance URL must be http or https, got %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, errors.New("the authentik instance URL has no host")
	}

	// Everything this package calls hangs off /api/v3, so the base is built
	// once rather than at nine call sites.
	base := strings.TrimSuffix(parsed.Scheme+"://"+parsed.Host+parsed.Path, "/")
	base = strings.TrimSuffix(base, "/api/v3")

	return &Client{
		baseURL: base + "/api/v3",
		token:   token,
		http:    &http.Client{Timeout: requestTimeout},
	}, nil
}

// InstanceURL is the instance this client speaks to, without the API path. It
// is what a page shows an operator and what a redirect URI is built from.
func (c *Client) InstanceURL() string {
	return strings.TrimSuffix(c.baseURL, "/api/v3")
}

// --- the objects, exactly as authentik names them ---

// User is a person in the directory.
//
// `pk` is an integer here and a UUID on a group, which is authentik's choice
// rather than a mistake in this file.
type User struct {
	PK       int      `json:"pk"`
	Username string   `json:"username"`
	Name     string   `json:"name"`
	Email    string   `json:"email,omitempty"`
	IsActive bool     `json:"is_active"`
	Groups   []string `json:"groups,omitempty"`

	// IsSuperuser is authentik's own, and this console reads it for one
	// reason: to refuse to mint a credential for such an account. See
	// RecoveryLink.
	IsSuperuser bool   `json:"is_superuser,omitempty"`
	Type        string `json:"type,omitempty"`
	UID         string `json:"uid,omitempty"`
}

// Group is a role, as this project uses them.
type Group struct {
	PK   string `json:"pk"`
	Name string `json:"name"`
}

// Provider is the OAuth2 provider the console authenticates through. The
// secret is returned once, on creation, and is what the console stores.
type Provider struct {
	PK           int    `json:"pk"`
	Name         string `json:"name"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`

	// SigningKey is the certificate the ID token is signed with. Empty means
	// authentik signs symmetrically with the client secret — see SigningKey
	// for why that does not work here.
	SigningKey string `json:"signing_key,omitempty"`
}

// Application is what a person sees on their authentik dashboard.
type Application struct {
	PK   string `json:"pk"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// Flow is one of the instance's flows, looked up by slug.
type Flow struct {
	PK   string `json:"pk"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// scopeMapping is a property mapping that puts a claim in the token.
type scopeMapping struct {
	PK        string `json:"pk"`
	ScopeName string `json:"scope_name"`
}

// page is authentik's list envelope.
type page[T any] struct {
	Pagination struct {
		Next  int `json:"next"`
		Count int `json:"count"`
	} `json:"pagination"`
	Results []T `json:"results"`
}

// --- the calls ---

// Me is who the API token belongs to.
//
// The wizard's first call, and it does two jobs: it proves the token works
// before anything is created, and it answers who to make the first admin —
// which is the person holding the token, and is not something the wizard
// should ask them to type.
//
// # It is the one endpoint here that wraps its answer
//
// `/core/users/me/` returns a `SessionUser`, which is `{user, original,
// users}`, where every other call in this file returns the object itself or a
// paginated list of them. Decoding it as a bare User yields a person with no
// username and no identifier — which is how this was found: the wizard refused
// with "authentik did not say who this token belongs to" against an instance
// whose token was perfectly good.
//
// That guard stays, because it turned a silent wrong answer into a legible
// one, and because the next version of this API could reshape the thing again.
func (c *Client) Me(ctx context.Context) (User, error) {
	var session struct {
		User selfUser `json:"user"`
	}
	if err := c.do(ctx, http.MethodGet, "/core/users/me/", nil, &session); err != nil {
		return User{}, err
	}

	who := User{
		PK:       session.User.PK,
		Username: session.User.Username,
		Name:     session.User.Name,
		Email:    session.User.Email,
		IsActive: session.User.IsActive,
		Type:     session.User.Type,
	}
	// Flattened to the identifiers the rest of this package uses, so a caller
	// never has to know that one endpoint describes a group differently.
	for _, group := range session.User.Groups {
		who.Groups = append(who.Groups, group.PK)
	}
	return who, nil
}

// selfUser is the person `/core/users/me/` describes, which is **not** the
// `User` every other endpoint returns.
//
// authentik calls it `UserSelf`, and the difference is not cosmetic: a `User`
// lists its groups as identifiers, where this lists them as objects. Decoding
// one as the other fails on that field — which is how this was found, against
// a live instance, one error after the wrapper itself.
//
// Kept as its own type rather than by loosening `User`, so the shape every
// other call returns stays exactly what the schema says it is and the oddity
// stays where the oddity is.
type selfUser struct {
	PK       int    `json:"pk"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email,omitempty"`
	IsActive bool   `json:"is_active"`
	Type     string `json:"type,omitempty"`

	Groups []struct {
		PK   string `json:"pk"`
		Name string `json:"name"`
	} `json:"groups"`
}

// Flow finds one by slug.
//
// A provider cannot be created without an authorization flow and an
// invalidation flow, and both are instance-wide objects this package will not
// create. Looking them up by slug is how it uses what is already there.
func (c *Client) Flow(ctx context.Context, slug string) (Flow, error) {
	var found page[Flow]
	err := c.do(ctx, http.MethodGet, "/flows/instances/?slug="+url.QueryEscape(slug), nil, &found)
	if err != nil {
		return Flow{}, err
	}
	for _, flow := range found.Results {
		if flow.Slug == slug {
			return flow, nil
		}
	}
	return Flow{}, fmt.Errorf("%w: flow %q", ErrNotFound, slug)
}

// ScopeMappings resolves the scope names a provider should carry to their
// identifiers.
//
// Missing ones are skipped rather than refused: an instance that has renamed
// or removed a default scope should still get a working provider, and the
// claims the console actually needs are checked separately where the absence
// can be explained.
func (c *Client) ScopeMappings(ctx context.Context, names ...string) ([]string, error) {
	var found page[scopeMapping]
	if err := c.do(ctx, http.MethodGet, "/propertymappings/provider/scope/?page_size=200", nil, &found); err != nil {
		return nil, err
	}

	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}

	var ids []string
	for _, mapping := range found.Results {
		if wanted[mapping.ScopeName] {
			ids = append(ids, mapping.PK)
		}
	}
	return ids, nil
}

// SigningKey finds a certificate the provider can sign tokens with.
//
// # Without one, a sign-in cannot be verified
//
// An authentik provider with no signing key signs its ID tokens **HS256**,
// symmetrically, with the client secret. With one it signs **RS256** and
// publishes the public half at the JWKS the discovery document advertises,
// which is what any OIDC library verifies against.
//
// Leaving it unset therefore produces a provider that authenticates people
// perfectly well and whose tokens this console cannot check — the sign-in
// fails at the last step with `unexpected signature algorithm "HS256"`, long
// after the directory has been provisioned and everything looks right. That is
// precisely what happened on the first live run.
//
// `has_key=true` because a certificate without its private half cannot sign.
// The first is taken: a stock authentik ships exactly one, self-signed, which
// is the right thing for this; an operator with several has a preference this
// has no way to learn, and the provider can be pointed at another by hand.
func (c *Client) SigningKey(ctx context.Context) (string, error) {
	var found page[struct {
		PK   string `json:"pk"`
		Name string `json:"name"`
	}]
	err := c.do(ctx, http.MethodGet, "/crypto/certificatekeypairs/?has_key=true", nil, &found)
	if err != nil {
		return "", err
	}
	if len(found.Results) == 0 {
		return "", fmt.Errorf(
			"%w: no certificate with a private key, so tokens cannot be signed", ErrNotFound)
	}
	log.Debug().Str("certificate", found.Results[0].Name).
		Msg("authentik: signing tokens with this certificate")
	return found.Results[0].PK, nil
}

// GroupByName finds a group, or ErrNotFound.
func (c *Client) GroupByName(ctx context.Context, name string) (Group, error) {
	var found page[Group]
	err := c.do(ctx, http.MethodGet, "/core/groups/?name="+url.QueryEscape(name), nil, &found)
	if err != nil {
		return Group{}, err
	}
	for _, group := range found.Results {
		if group.Name == name {
			return group, nil
		}
	}
	return Group{}, fmt.Errorf("%w: group %q", ErrNotFound, name)
}

// EnsureGroup finds a group or creates it.
//
// Find-or-create rather than create, because a wizard that failed halfway must
// be runnable again. A second attempt that collided on a name it created
// itself the first time would strand an operator with a half-provisioned
// directory and no way forward but the authentik UI.
func (c *Client) EnsureGroup(ctx context.Context, name string) (Group, error) {
	switch existing, err := c.GroupByName(ctx, name); {
	case err == nil:
		log.Debug().Str("group", name).Msg("authentik: group already exists")
		return existing, nil
	case !errors.Is(err, ErrNotFound):
		return Group{}, err
	}

	var created Group
	body := map[string]any{"name": name}
	if err := c.do(ctx, http.MethodPost, "/core/groups/", body, &created); err != nil {
		return Group{}, fmt.Errorf("create group %q: %w", name, err)
	}
	log.Info().Str("group", name).Str("pk", created.PK).Msg("authentik: group created")
	return created, nil
}

// ProviderSpec is what the console's OAuth2 provider needs to exist.
type ProviderSpec struct {
	Name string

	// RedirectURI is the console's callback. Matched strictly: a regex here
	// would be a provider that accepts a redirect somebody else can claim.
	RedirectURI string

	// AuthorizationFlow and InvalidationFlow are instance flows, by
	// identifier. Both are required by authentik and neither is created here.
	AuthorizationFlow string
	InvalidationFlow  string

	// ScopeMappings are the claims the token carries.
	ScopeMappings []string

	// SigningKey is the certificate the ID token is signed with. Without it
	// authentik signs HS256 and the sign-in cannot be verified.
	SigningKey string
}

// setSigningKey repairs a provider that was created without one.
func (c *Client) setSigningKey(ctx context.Context, providerPK int, key string) (Provider, error) {
	var patched Provider
	path := "/providers/oauth2/" + strconv.Itoa(providerPK) + "/"
	body := map[string]any{"signing_key": key}
	if err := c.do(ctx, http.MethodPatch, path, body, &patched); err != nil {
		return Provider{}, fmt.Errorf("set the provider signing key: %w", err)
	}
	return patched, nil
}

// EnsureProvider finds the console's provider by name, or creates it.
//
// **A found provider does not carry its client secret back.** authentik
// returns the secret on the object, so a find does retrieve it — but an
// operator who re-runs the wizard against a provider somebody has since edited
// gets what is there now, which is the honest answer rather than a second
// provider with the same purpose.
func (c *Client) EnsureProvider(ctx context.Context, spec ProviderSpec) (Provider, error) {
	var found page[Provider]
	err := c.do(ctx, http.MethodGet, "/providers/oauth2/?name="+url.QueryEscape(spec.Name), nil, &found)
	if err != nil {
		return Provider{}, err
	}
	for _, provider := range found.Results {
		if provider.Name != spec.Name {
			continue
		}
		// Found. If it has no signing key it signs HS256 and nothing can
		// verify it, so the existing provider is repaired rather than
		// returned as it is — an operator who ran the wizard before this was
		// understood should not have to delete anything by hand.
		if provider.SigningKey == "" && spec.SigningKey != "" {
			log.Warn().Str("provider", spec.Name).
				Msg("authentik: provider had no signing key; setting one so tokens can be verified")
			patched, err := c.setSigningKey(ctx, provider.PK, spec.SigningKey)
			if err != nil {
				return Provider{}, err
			}
			return patched, nil
		}
		log.Debug().Str("provider", spec.Name).Msg("authentik: provider already exists")
		return provider, nil
	}

	body := map[string]any{
		"name":               spec.Name,
		"authorization_flow": spec.AuthorizationFlow,
		"invalidation_flow":  spec.InvalidationFlow,
		"client_type":        "confidential",
		"redirect_uris": []map[string]string{
			{"matching_mode": "strict", "url": spec.RedirectURI},
		},
		// The console reads roles from authentik rather than from the token,
		// so the claims here are for the person's own benefit — who they are,
		// on their dashboard — rather than something authorisation depends on.
		"include_claims_in_id_token": true,
	}
	if spec.SigningKey != "" {
		// Without this authentik signs the ID token HS256 and no OIDC library
		// can verify it. See SigningKey.
		body["signing_key"] = spec.SigningKey
	}
	if len(spec.ScopeMappings) > 0 {
		body["property_mappings"] = spec.ScopeMappings
	}

	var created Provider
	if err := c.do(ctx, http.MethodPost, "/providers/oauth2/", body, &created); err != nil {
		return Provider{}, fmt.Errorf("create provider %q: %w", spec.Name, err)
	}
	log.Info().Str("provider", spec.Name).Int("pk", created.PK).
		Msg("authentik: oauth2 provider created")
	return created, nil
}

// EnsureApplication finds the console's application by slug, or creates it.
func (c *Client) EnsureApplication(ctx context.Context, name, slug string, providerPK int) (Application, error) {
	var found page[Application]
	err := c.do(ctx, http.MethodGet, "/core/applications/?slug="+url.QueryEscape(slug), nil, &found)
	if err != nil {
		return Application{}, err
	}
	for _, app := range found.Results {
		if app.Slug == slug {
			log.Debug().Str("application", slug).Msg("authentik: application already exists")
			return app, nil
		}
	}

	body := map[string]any{"name": name, "slug": slug, "provider": providerPK}

	var created Application
	if err := c.do(ctx, http.MethodPost, "/core/applications/", body, &created); err != nil {
		return Application{}, fmt.Errorf("create application %q: %w", slug, err)
	}
	log.Info().Str("application", slug).Msg("authentik: application created")
	return created, nil
}

// UserSpec is a person an admin is adding.
type UserSpec struct {
	// Username is what they type to sign in, and is the identity the audit
	// log will carry.
	Username string

	// Name is what a colleague recognises them by.
	Name string

	// Email is stored on the directory record as an identifier. **Nothing in
	// this project sends to it.** There is no mail server here and that was a
	// deliberate removal; an admin hands over the way in themselves.
	Email string

	// Groups are the role groups to put them in at once, by identifier.
	// Setting them at creation rather than afterwards means a person never
	// exists in a state where they are in the directory with no role.
	Groups []string
}

// CreateUser adds a person to the directory.
//
// Type `internal`, which is what a member of staff is. The alternative,
// `external`, is for people who use applications rather than run them, and
// counts differently against an enterprise licence.
func (c *Client) CreateUser(ctx context.Context, spec UserSpec) (User, error) {
	body := map[string]any{
		"username":  spec.Username,
		"name":      spec.Name,
		"is_active": true,
		"type":      "internal",
	}
	if spec.Email != "" {
		body["email"] = spec.Email
	}
	if len(spec.Groups) > 0 {
		body["groups"] = spec.Groups
	}

	var created User
	if err := c.do(ctx, http.MethodPost, "/core/users/", body, &created); err != nil {
		return User{}, fmt.Errorf("create user %q: %w", spec.Username, err)
	}
	log.Info().Str("username", spec.Username).Int("pk", created.PK).
		Msg("authentik: user created")
	return created, nil
}

// RecoveryLink mints a single-use link that lets somebody set up their own way
// in — which, on an instance whose authentication flow allows WebAuthn, is a
// passkey.
//
// This is how a person is onboarded, and it is deliberately not a password.
// Nothing is emailed: the link is returned here and an admin passes it on by
// whatever means the group already uses. It expires and it is single-use, so
// what an admin holds for a few minutes is not a credential that outlives the
// first login — which a temporary password would be, since authentik has no
// flag that forces a change at next sign-in.
//
// It needs a recovery flow set as the brand's default. Preconditions reports
// whether there is one, so the wizard can say so before an admin is halfway
// through adding somebody.
func (c *Client) RecoveryLink(ctx context.Context, userPK int) (string, error) {
	// Read before writing: see ErrWouldEscalate. The cost is one call on a
	// path somebody takes by hand, and what it buys is that an admin of this
	// register cannot mint themselves a way into the directory.
	who, err := c.UserByPK(ctx, userPK)
	switch {
	case err != nil:
		return "", fmt.Errorf("check who %d is before minting a link: %w", userPK, err)
	case who.IsSuperuser:
		log.Warn().Str("username", who.Username).
			Msg("refused to mint a recovery link for a directory superuser")
		return "", fmt.Errorf("%w: %s", ErrWouldEscalate, who.Username)
	}

	var answer struct {
		Link string `json:"link"`
	}
	path := "/core/users/" + strconv.Itoa(userPK) + "/recovery/"
	if err := c.do(ctx, http.MethodPost, path, nil, &answer); err != nil {
		return "", fmt.Errorf("mint a recovery link: %w", err)
	}
	if answer.Link == "" {
		return "", errors.New(
			"authentik returned no recovery link: the brand needs a recovery flow configured")
	}
	return answer.Link, nil
}

// UserByPK reads one person by their identifier.
func (c *Client) UserByPK(ctx context.Context, userPK int) (User, error) {
	var who User
	path := "/core/users/" + strconv.Itoa(userPK) + "/"
	if err := c.do(ctx, http.MethodGet, path, nil, &who); err != nil {
		return User{}, fmt.Errorf("read user %d: %w", userPK, err)
	}
	return who, nil
}

// HasRecoveryFlow reports whether the instance has a recovery flow at all.
//
// Asked only when the brand has none bound, to tell "somebody has to pick one"
// apart from "somebody has to build one". A fresh authentik has **no** recovery
// flow: it is not among the blueprints shipped enabled, and the published
// example builds one around email verification — which this project has no
// mail server for. What is wanted here is a flow a recovery *link* walks
// through, ending with the person setting up their own credential.
func (c *Client) HasRecoveryFlow(ctx context.Context) (bool, error) {
	var found page[struct {
		Slug string `json:"slug"`
	}]
	if err := c.do(ctx, http.MethodGet,
		"/flows/instances/?designation=recovery&page_size=1", nil, &found); err != nil {
		return false, fmt.Errorf("look for a recovery flow: %w", err)
	}
	return len(found.Results) > 0, nil
}

// UsersInGroup lists the people holding one role.
//
// Paged through to the end rather than taking the first page: a register whose
// staff list silently stopped at the page size would be one where somebody
// still has access and nobody can see them.
func (c *Client) UsersInGroup(ctx context.Context, groupName string) ([]User, error) {
	var all []User
	for next := 1; next > 0; {
		var found page[User]
		path := fmt.Sprintf("/core/users/?groups_by_name=%s&page=%d&page_size=100",
			url.QueryEscape(groupName), next)
		if err := c.do(ctx, http.MethodGet, path, nil, &found); err != nil {
			return nil, err
		}
		all = append(all, found.Results...)
		next = found.Pagination.Next
	}
	return all, nil
}

// UserByUsername finds one person, with the groups they are in.
//
// One call rather than listing both role groups and looking for them: this is
// asked on every write, and two full listings per acceptance would make the
// cost of checking grow with the size of the staff.
func (c *Client) UserByUsername(ctx context.Context, username string) (User, error) {
	var found page[User]
	path := "/core/users/?username=" + url.QueryEscape(username)
	if err := c.do(ctx, http.MethodGet, path, nil, &found); err != nil {
		return User{}, err
	}
	for _, person := range found.Results {
		if person.Username == username {
			return person, nil
		}
	}
	return User{}, fmt.Errorf("%w: user %q", ErrNotFound, username)
}

// AddToGroup gives somebody a role.
func (c *Client) AddToGroup(ctx context.Context, groupPK string, userPK int) error {
	body := map[string]any{"pk": userPK}
	path := "/core/groups/" + url.PathEscape(groupPK) + "/add_user/"
	if err := c.do(ctx, http.MethodPost, path, body, nil); err != nil {
		return fmt.Errorf("add user %d to group: %w", userPK, err)
	}
	return nil
}

// RemoveFromGroup takes a role away.
func (c *Client) RemoveFromGroup(ctx context.Context, groupPK string, userPK int) error {
	body := map[string]any{"pk": userPK}
	path := "/core/groups/" + url.PathEscape(groupPK) + "/remove_user/"
	if err := c.do(ctx, http.MethodPost, path, body, nil); err != nil {
		return fmt.Errorf("remove user %d from group: %w", userPK, err)
	}
	return nil
}

// Preconditions is what this package checked but will not change.
//
// Everything in it is instance-wide: flows shared with every other application
// in the operator's directory. Provisioning them from here would mean a tool
// that was asked to add itself quietly rewriting how everybody else signs in.
// So they are reported, in words an operator can act on, and the wizard says
// what is missing rather than papering over it.
type Preconditions struct {
	// AuthorizationFlow and InvalidationFlow exist, so a provider can be made.
	AuthorizationFlow string
	InvalidationFlow  string

	// HasRecoveryFlow says the default brand has a recovery flow bound to it,
	// which is what makes RecoveryLink work. Without it people can be created
	// and not onboarded.
	HasRecoveryFlow bool

	// RecoveryFlowExists says the instance has a recovery flow at all.
	//
	// Separate from the binding because the two have very different remedies,
	// and a message that assumed the easy one sent an operator looking for a
	// dropdown entry that was not there. **authentik ships no recovery flow**
	// — not a disabled blueprint, none at all — so on a fresh instance this is
	// false and somebody has to build one.
	RecoveryFlowExists bool

	// Missing names what is not there, ready to be shown.
	Missing []string
}

// Default flow slugs, which is what a stock authentik ships with.
const (
	defaultAuthorizationFlow = "default-provider-authorization-implicit-consent"
	defaultInvalidationFlow  = "default-provider-invalidation-flow"
)

// BrandHasRecoveryFlow reports whether anybody can be given a way in.
//
// `POST /core/users/{id}/recovery/` mints the single-use link that is this
// register's whole onboarding, and it answers `No recovery flow set.` unless
// the default brand carries one. `Brand.flow_recovery` is that binding — a
// nullable uuid — so an empty one is the refusal, known before anybody is
// created rather than after.
//
// A stock authentik ships the flow and binds nothing, which is why this is the
// first thing an operator has to do by hand and why the console says so.
func (c *Client) BrandHasRecoveryFlow(ctx context.Context) (bool, error) {
	var found page[struct {
		Domain       string `json:"domain"`
		Default      bool   `json:"default"`
		RecoveryFlow string `json:"flow_recovery"`
	}]
	if err := c.do(ctx, http.MethodGet, "/core/brands/?default=true", nil, &found); err != nil {
		return false, fmt.Errorf("read the default brand: %w", err)
	}
	if len(found.Results) == 0 {
		// No default brand at all. Nothing can be onboarded through it, which
		// is the answer rather than an error.
		log.Warn().Msg("authentik: no default brand, so nobody can be given a way in")
		return false, nil
	}
	return found.Results[0].RecoveryFlow != "", nil
}

// Check looks for everything the integration needs and reports what is absent.
func (c *Client) Check(ctx context.Context) (Preconditions, error) {
	var found Preconditions

	authorization, err := c.Flow(ctx, defaultAuthorizationFlow)
	switch {
	case err == nil:
		found.AuthorizationFlow = authorization.PK
	case errors.Is(err, ErrNotFound):
		found.Missing = append(found.Missing,
			"an authorization flow named "+defaultAuthorizationFlow)
	default:
		return found, err
	}

	invalidation, err := c.Flow(ctx, defaultInvalidationFlow)
	switch {
	case err == nil:
		found.InvalidationFlow = invalidation.PK
	case errors.Is(err, ErrNotFound):
		found.Missing = append(found.Missing,
			"an invalidation flow named "+defaultInvalidationFlow)
	default:
		return found, err
	}

	// Deliberately the brand rather than the flow. A flow named
	// `default-recovery-flow` can exist, unbound, and authentik will still
	// refuse a recovery link with `{"non_field_errors":["No recovery flow
	// set."]}` — which is what a live run met, after a check that said the
	// instance was fine. What `RecoveryLink` needs is the flow bound to the
	// brand, so that is the question asked.
	//
	// It also stops a false alarm in the other direction: an operator whose
	// recovery flow is bound under a name of their own was being told they had
	// none.
	bound, err := c.BrandHasRecoveryFlow(ctx)
	if err != nil {
		return found, err
	}
	found.HasRecoveryFlow = bound

	// Deliberately **not** added to Missing, which is the list of things this
	// package reports and will not create. A recovery flow is now the one
	// exception it does create — see EnsureRecoveryFlow — so naming it there
	// would tell an operator to go and do something the next step does for
	// them. The two booleans carry it instead, and the caller says what it
	// means in its own words.
	if !bound {
		exists, err := c.HasRecoveryFlow(ctx)
		if err != nil {
			return found, err
		}
		found.RecoveryFlowExists = exists
	} else {
		found.RecoveryFlowExists = true
	}

	return found, nil
}

// Ready reports whether a provider can be created at all.
func (p Preconditions) Ready() bool {
	return p.AuthorizationFlow != "" && p.InvalidationFlow != ""
}

// do performs one call.
//
// Traced at the boundary like every other outside service in this project:
// the request at TRACE, the decision at DEBUG, so a wrong answer can be
// debugged without guessing what was sent.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		log.Trace().Str("method", method).Str("path", path).
			RawJSON("body", encoded).Msg("authentik: request")
		payload = bytes.NewReader(encoded)
	} else {
		log.Trace().Str("method", method).Str("path", path).Msg("authentik: request")
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call authentik: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read the answer: %w", err)
	}
	log.Debug().Str("method", method).Str("path", path).
		Int("status", resp.StatusCode).Msg("authentik: answered")

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s %s", ErrNotFound, method, path)
	}
	// The credential itself, rather than the request. Worth its own sentinel
	// because the remedy is completely different and belongs to a different
	// person: nothing the caller sends can fix it, and nobody can sign in or
	// act until an operator supplies a working token. Without this the console
	// reported an expired token as "that sign-in could not be completed",
	// which sent the one person who could fix it looking at the wrong thing.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%w: %s %s: %s",
			ErrTokenRefused, method, path, refusal(raw, resp.StatusCode))
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("authentik refused %s %s: %s", method, path, refusal(raw, resp.StatusCode))
	}

	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("read the answer to %s %s: %w", method, path, err)
	}
	return nil
}

// refusal turns authentik's error body into one line somebody can act on.
//
// It answers validation failures as a map of field to messages, which read as
// JSON in a log and as nothing at all in a page. An operator who typed a URL
// wrong should be told which field authentik objected to, not handed a status
// code.
func refusal(raw []byte, status int) string {
	var fields map[string][]string
	if err := json.Unmarshal(raw, &fields); err == nil && len(fields) > 0 {
		var parts []string
		for field, messages := range fields {
			parts = append(parts, field+": "+strings.Join(messages, "; "))
		}
		return strings.Join(parts, ", ")
	}

	var detail struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &detail); err == nil && detail.Detail != "" {
		return detail.Detail
	}

	if trimmed := strings.TrimSpace(string(raw)); trimmed != "" && len(trimmed) < 300 {
		return trimmed
	}
	return "status " + strconv.Itoa(status)
}
