package authentik

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fake is a stand-in authentik.
//
// **It proves this package's logic, not authentik's behaviour.** Nothing here
// can reach a real instance, so what these tests establish is that the client
// sends what this file believes it should and reads what it believes comes
// back. The belief itself came from authentik's own `schema.yml` — every field
// name and every required field — which is the closest thing to certainty
// available without a server.
type fake struct {
	t        *testing.T
	mux      *http.ServeMux
	server   *httptest.Server
	seen     []string
	bodies   map[string]map[string]any
	authSeen []string
}

func newFake(t *testing.T) *fake {
	t.Helper()

	f := &fake{t: t, mux: http.NewServeMux(), bodies: map[string]map[string]any{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.seen = append(f.seen, r.Method+" "+r.URL.Path)
		f.authSeen = append(f.authSeen, r.Header.Get("Authorization"))
		if r.Body != nil {
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) == nil {
				f.bodies[r.Method+" "+r.URL.Path] = body
			}
		}
		f.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fake) handle(path string, status int, body any) {
	f.mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body != nil {
			json.NewEncoder(w).Encode(body) //nolint:errcheck
		}
	})
}

// empty is the list envelope with nothing in it, which is what a find-or-create
// sees on its first run.
func empty() map[string]any {
	return map[string]any{
		"pagination": map[string]any{"next": 0, "count": 0},
		"results":    []any{},
	}
}

func list(results ...any) map[string]any {
	return map[string]any{
		"pagination": map[string]any{"next": 0, "count": len(results)},
		"results":    results,
	}
}

func (f *fake) client(t *testing.T) *Client {
	t.Helper()

	c, err := New(f.server.URL, "a-token")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestTheURLIsNormalisedTheWaySomebodyWouldPasteIt.
//
// An operator copies the address out of their browser, which may carry a
// trailing slash, and somebody reading the docs may paste the API root itself.
// Refusing either teaches nothing and costs a support question.
func TestTheURLIsNormalisedTheWaySomebodyWouldPasteIt(t *testing.T) {
	for _, given := range []string{
		"https://authentik.example",
		"https://authentik.example/",
		"https://authentik.example/api/v3",
		"https://authentik.example/api/v3/",
		"  https://authentik.example  ",
	} {
		c, err := New(given, "a-token")
		if err != nil {
			t.Fatalf("New(%q): %v", given, err)
		}
		if c.baseURL != "https://authentik.example/api/v3" {
			t.Errorf("New(%q) → base %q", given, c.baseURL)
		}
		if c.InstanceURL() != "https://authentik.example" {
			t.Errorf("New(%q) → instance %q", given, c.InstanceURL())
		}
	}
}

func TestWhatIsRefusedAtConstruction(t *testing.T) {
	for _, c := range []struct{ url, token, why string }{
		{"", "t", "no URL"},
		{"https://authentik.example", "", "no token"},
		{"ftp://authentik.example", "t", "not http"},
		{"not a url at all", "t", "no host"},
	} {
		if _, err := New(c.url, c.token); err == nil {
			t.Errorf("%s was accepted", c.why)
		}
	}
}

// TestTheTokenIsSentAsBearer. authentik authenticates API calls with
// `Authorization: Bearer <token>`; getting this wrong is a 403 on every call
// with nothing to say why.
func TestTheTokenIsSentAsBearer(t *testing.T) {
	f := newFake(t)
	f.handle("/api/v3/core/users/me/", 200, map[string]any{
		"user": map[string]any{"pk": 1, "username": "dominique"}})

	if _, err := f.client(t).Me(context.Background()); err != nil {
		t.Fatalf("Me: %v", err)
	}
	if len(f.authSeen) != 1 || f.authSeen[0] != "Bearer a-token" {
		t.Errorf("Authorization = %v, want Bearer a-token", f.authSeen)
	}
}

// TestMeIsWhoTheTokenBelongsTo — the wizard's first call, which proves the
// token before anything is created and answers who to make the first admin.
func TestMeIsWhoTheTokenBelongsTo(t *testing.T) {
	f := newFake(t)
	// A SessionUser, which is what authentik really returns here — the one
	// endpoint in this client that wraps its answer.
	f.handle("/api/v3/core/users/me/", 200, map[string]any{
		"user": map[string]any{
			"pk": 42, "username": "dominique", "name": "Dominique",
			"email": "d@example.org", "is_active": true, "type": "internal",
		},
	})

	who, err := f.client(t).Me(context.Background())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if who.PK != 42 || who.Username != "dominique" || who.Name != "Dominique" {
		t.Errorf("Me = %+v", who)
	}
}

// TestEnsureGroupIsFindOrCreate is what makes a half-finished wizard runnable
// again. A second attempt that collided on a name it created itself the first
// time would strand an operator with a half-provisioned directory.
func TestEnsureGroupIsFindOrCreate(t *testing.T) {
	// First run: nothing there, so it creates.
	fresh := newFake(t)
	fresh.mux.HandleFunc("/api/v3/core/groups/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(empty()) //nolint:errcheck
			return
		}
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"pk": "uuid-1", "name": "doleances-curators",
		})
	})

	group, err := fresh.client(t).EnsureGroup(context.Background(), "doleances-curators")
	if err != nil {
		t.Fatalf("EnsureGroup: %v", err)
	}
	if group.PK != "uuid-1" {
		t.Errorf("group = %+v", group)
	}
	if posts := countMethod(fresh.seen, http.MethodPost); posts != 1 {
		t.Errorf("%d creates on a fresh directory, want 1", posts)
	}

	// Second run: it is already there, so nothing is created.
	again := newFake(t)
	again.mux.HandleFunc("/api/v3/core/groups/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(list(map[string]any{ //nolint:errcheck
				"pk": "uuid-1", "name": "doleances-curators",
			}))
			return
		}
		t.Error("a group that already exists was created again")
	})

	group, err = again.client(t).EnsureGroup(context.Background(), "doleances-curators")
	if err != nil {
		t.Fatalf("EnsureGroup on a second run: %v", err)
	}
	if group.PK != "uuid-1" {
		t.Errorf("group = %+v", group)
	}
	if posts := countMethod(again.seen, http.MethodPost); posts != 0 {
		t.Errorf("%d creates on a second run, want none", posts)
	}
}

func countMethod(seen []string, method string) int {
	n := 0
	for _, s := range seen {
		if strings.HasPrefix(s, method+" ") {
			n++
		}
	}
	return n
}

// TestAProviderIsCreatedWithWhatAuthentikRequires.
//
// `name`, `authorization_flow`, `invalidation_flow` and `redirect_uris` are
// required by the schema, and the redirect is matched **strictly** — a regex
// there would be a provider that accepts a redirect somebody else can claim.
func TestAProviderIsCreatedWithWhatAuthentikRequires(t *testing.T) {
	f := newFake(t)
	f.mux.HandleFunc("/api/v3/providers/oauth2/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(empty()) //nolint:errcheck
			return
		}
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"pk": 7, "name": "doleances console",
			"client_id": "an-id", "client_secret": "a-secret",
		})
	})

	provider, err := f.client(t).EnsureProvider(context.Background(), ProviderSpec{
		Name:              "doleances console",
		RedirectURI:       "https://console.example/auth/callback",
		AuthorizationFlow: "flow-a",
		InvalidationFlow:  "flow-i",
		ScopeMappings:     []string{"m1", "m2"},
	})
	if err != nil {
		t.Fatalf("EnsureProvider: %v", err)
	}
	if provider.ClientID != "an-id" || provider.ClientSecret != "a-secret" {
		t.Errorf("provider = %+v, want the credentials back", provider)
	}

	sent := f.bodies["POST /api/v3/providers/oauth2/"]
	for _, required := range []string{"name", "authorization_flow", "invalidation_flow", "redirect_uris"} {
		if sent[required] == nil {
			t.Errorf("the create omitted %q, which authentik requires", required)
		}
	}
	uris, _ := sent["redirect_uris"].([]any)
	if len(uris) != 1 {
		t.Fatalf("redirect_uris = %v", sent["redirect_uris"])
	}
	first, _ := uris[0].(map[string]any)
	if first["matching_mode"] != "strict" {
		t.Errorf("matching_mode = %v, want strict", first["matching_mode"])
	}
	if first["url"] != "https://console.example/auth/callback" {
		t.Errorf("redirect url = %v", first["url"])
	}
}

// TestAPersonIsCreatedWithTheirRolesAtOnce.
//
// Groups are settable on the create, which matters: a person created first and
// given a role second exists, briefly, as somebody in the directory with no
// role — and if the second call fails, permanently.
func TestAPersonIsCreatedWithTheirRolesAtOnce(t *testing.T) {
	f := newFake(t)
	f.handle("/api/v3/core/users/", 201, map[string]any{
		"pk": 9, "username": "camille", "name": "Camille", "is_active": true,
	})

	created, err := f.client(t).CreateUser(context.Background(), UserSpec{
		Username: "camille", Name: "Camille", Email: "c@example.org",
		Groups: []string{"uuid-curators"},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if created.PK != 9 {
		t.Errorf("created = %+v", created)
	}

	sent := f.bodies["POST /api/v3/core/users/"]
	if sent["username"] != "camille" || sent["name"] != "Camille" {
		t.Errorf("the create sent %v", sent)
	}
	if sent["type"] != "internal" {
		t.Errorf("type = %v, want internal — a member of staff is not an external user", sent["type"])
	}
	groups, _ := sent["groups"].([]any)
	if len(groups) != 1 || groups[0] != "uuid-curators" {
		t.Errorf("groups = %v, want the role set at creation", sent["groups"])
	}
}

// TestNoPasswordIsEverSet is the decision, asserted.
//
// Onboarding is a single-use recovery link that ends in a passkey. A temporary
// password would be a credential the admin knows and that outlives the first
// login, because authentik has no flag that forces a change at next sign-in.
func TestNoPasswordIsEverSet(t *testing.T) {
	f := newFake(t)
	f.handle("/api/v3/core/users/", 201, map[string]any{"pk": 9, "username": "camille"})
	f.handle("/api/v3/core/users/9/recovery/", 200, map[string]any{
		"link": "https://authentik.example/if/flow/default-recovery-flow/?token=abc",
	})

	c := f.client(t)
	ctx := context.Background()

	created, err := c.CreateUser(ctx, UserSpec{Username: "camille", Name: "Camille"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	link, err := c.RecoveryLink(ctx, created.PK)
	if err != nil {
		t.Fatalf("RecoveryLink: %v", err)
	}
	if !strings.Contains(link, "token=abc") {
		t.Errorf("link = %q", link)
	}

	for _, call := range f.seen {
		if strings.Contains(call, "set_password") {
			t.Errorf("a password was set: %s", call)
		}
	}
}

// TestARecoveryLinkWithNoFlowSaysWhatIsMissing rather than handing back an
// empty string that a page would then show as a way in.
func TestARecoveryLinkWithNoFlowSaysWhatIsMissing(t *testing.T) {
	f := newFake(t)
	f.handle("/api/v3/core/users/9/", 200, map[string]any{"pk": 9, "username": "camille"})
	f.handle("/api/v3/core/users/9/recovery/", 200, map[string]any{})

	_, err := f.client(t).RecoveryLink(context.Background(), 9)
	if err == nil {
		t.Fatal("an empty link was accepted as a way in")
	}
	if !strings.Contains(err.Error(), "recovery flow") {
		t.Errorf("the error does not name what is missing: %v", err)
	}
}

// TestTheStaffListIsPagedToTheEnd. A list that silently stopped at the page
// size would be one where somebody still has access and nobody can see them.
func TestTheStaffListIsPagedToTheEnd(t *testing.T) {
	f := newFake(t)
	f.mux.HandleFunc("/api/v3/core/users/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1":
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"pagination": map[string]any{"next": 2, "count": 3},
				"results":    []any{map[string]any{"pk": 1}, map[string]any{"pk": 2}},
			})
		default:
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"pagination": map[string]any{"next": 0, "count": 3},
				"results":    []any{map[string]any{"pk": 3}},
			})
		}
	})

	people, err := f.client(t).UsersInGroup(context.Background(), "doleances-curators")
	if err != nil {
		t.Fatalf("UsersInGroup: %v", err)
	}
	if len(people) != 3 {
		t.Errorf("%d people, want all three across both pages", len(people))
	}
}

// TestCheckReportsWhatItWillNotCreate.
//
// Flows are instance-wide and shared with every other application in the
// operator's directory. A tool asked to add itself has no business rewriting
// how everybody else signs in, so what it needs and has not got is reported in
// words rather than provisioned quietly.
func TestCheckReportsWhatItWillNotCreate(t *testing.T) {
	f := newFake(t)
	f.mux.HandleFunc("/api/v3/flows/instances/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("designation") == "recovery" {
			// A stock authentik has none: not a disabled blueprint, none.
			json.NewEncoder(w).Encode(empty()) //nolint:errcheck
			return
		}
		slug := r.URL.Query().Get("slug")
		json.NewEncoder(w).Encode(list(map[string]any{ //nolint:errcheck
			"pk": "flow-" + slug, "slug": slug,
		}))
	})
	// A stock authentik: no recovery flow at all, and a brand naming none.
	f.handle("/api/v3/core/brands/", 200, list(map[string]any{
		"domain": "authentik-default", "default": true, "flow_recovery": nil,
	}))

	found, err := f.client(t).Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !found.Ready() {
		t.Error("a provider cannot be created although both its flows exist")
	}
	if found.HasRecoveryFlow {
		t.Error("a missing recovery flow was reported as present")
	}

	// Missing is what this package reports and will **not** create, and a
	// recovery flow is now the one thing it does create. Naming it there would
	// tell an operator to go and do what the next step does for them.
	if len(found.Missing) != 0 {
		t.Errorf("Missing = %v, want nothing an operator has to do by hand", found.Missing)
	}
	if found.RecoveryFlowExists {
		t.Error("an instance with no recovery flow was reported as having one")
	}
}

// TestARefusalIsReadable. authentik answers a validation failure as a map of
// field to messages; handing an operator a status code instead of "slug: this
// field must be unique" is how a wizard becomes unusable.
func TestARefusalIsReadable(t *testing.T) {
	f := newFake(t)
	f.mux.HandleFunc("/api/v3/core/groups/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(empty()) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"name": []string{"group with this name already exists."},
		})
	})

	_, err := f.client(t).EnsureGroup(context.Background(), "doleances-curators")
	if err == nil {
		t.Fatal("a refusal was accepted")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("the error loses what authentik said: %v", err)
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("the error loses which field: %v", err)
	}
}

// TestAMissingThingIsNotAFailedCall. Several operations here are
// find-or-create and need to tell "the directory has no such thing" from "the
// call did not work".
func TestAMissingThingIsNotAFailedCall(t *testing.T) {
	f := newFake(t)
	f.handle("/api/v3/flows/instances/", 200, empty())

	_, err := f.client(t).Flow(context.Background(), "no-such-flow")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestMeUnwrapsTheSessionUser pins the shape that a real instance taught us.
//
// `/core/users/me/` answers with `{user: …}` where every other call in this
// client answers with the object itself. Decoding it flat gives a person with
// no username, and the wizard then refuses a perfectly good token with
// "authentik did not say who this token belongs to" — which is exactly what
// happened against a live instance.
func TestMeUnwrapsTheSessionUser(t *testing.T) {
	f := newFake(t)
	f.handle("/api/v3/core/users/me/", 200, map[string]any{
		"user": map[string]any{"pk": 7, "username": "alice", "name": "Alice"},
		// The other two fields a SessionUser carries, present so the test
		// fails if somebody ever decodes the wrapper itself as the person.
		"original": map[string]any{"pk": 99, "username": "somebody-else"},
		"users":    []any{},
	})

	who, err := f.client(t).Me(context.Background())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if who.PK != 7 || who.Username != "alice" {
		t.Errorf("Me = %+v, want the user inside the wrapper", who)
	}
}

// TestMeReadsTheOtherUserModel.
//
// `/core/users/me/` describes a person as a `UserSelf`, where every other
// endpoint returns a `User`. The difference is not cosmetic: a `User` lists
// its groups as identifiers and this lists them as objects, so decoding one as
// the other fails on that field — which is exactly what a live instance did,
// one error after the wrapper itself.
func TestMeReadsTheOtherUserModel(t *testing.T) {
	f := newFake(t)
	f.handle("/api/v3/core/users/me/", 200, map[string]any{
		"user": map[string]any{
			"pk": 3, "username": "alice", "name": "Alice", "is_active": true,
			// Objects, not strings. A `User` would have []string here.
			"groups": []any{
				map[string]any{"pk": "uuid-admins", "name": "doleances_admin"},
				map[string]any{"pk": "uuid-curators", "name": "doleances_curator"},
			},
		},
	})

	who, err := f.client(t).Me(context.Background())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if who.PK != 3 || who.Username != "alice" {
		t.Errorf("Me = %+v", who)
	}
	// Flattened to identifiers, so a caller never has to know one endpoint
	// describes a group differently.
	if len(who.Groups) != 2 || who.Groups[0] != "uuid-admins" {
		t.Errorf("groups = %v, want the identifiers", who.Groups)
	}
}

// TestASigningCertificateIsFoundAndCarriedIntoTheProvider.
//
// The whole point of the field. A provider created without `signing_key` signs
// its ID tokens HS256 with the client secret, which no OIDC library will
// verify — so the live run provisioned a flawless directory and then refused
// every sign-in. `has_key=true` is asserted because a certificate without its
// private half cannot sign and would be the same bug one layer further in.
func TestASigningCertificateIsFoundAndCarriedIntoTheProvider(t *testing.T) {
	f := newFake(t)
	var query string
	f.mux.HandleFunc("/api/v3/crypto/certificatekeypairs/", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list(map[string]any{ //nolint:errcheck
			"pk": "cert-1", "name": "authentik Self-signed Certificate",
		})) //nolint:errcheck
	})

	key, err := f.client(t).SigningKey(context.Background())
	if err != nil {
		t.Fatalf("SigningKey: %v", err)
	}
	if key != "cert-1" {
		t.Errorf("key = %q, want cert-1", key)
	}
	if !strings.Contains(query, "has_key=true") {
		t.Errorf("query = %q, want has_key=true — a certificate with no private half cannot sign", query)
	}

	f.mux.HandleFunc("/api/v3/providers/oauth2/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(empty()) //nolint:errcheck
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"pk": 7, "signing_key": key}) //nolint:errcheck
	})

	if _, err := f.client(t).EnsureProvider(context.Background(), ProviderSpec{
		Name:              "doleances console",
		RedirectURI:       "https://console.example/auth/callback",
		AuthorizationFlow: "flow-a",
		InvalidationFlow:  "flow-i",
		SigningKey:        key,
	}); err != nil {
		t.Fatalf("EnsureProvider: %v", err)
	}
	if sent := f.bodies["POST /api/v3/providers/oauth2/"]; sent["signing_key"] != "cert-1" {
		t.Errorf("signing_key = %v, want it on the create body", sent["signing_key"])
	}
}

// TestNoCertificateIsAMissingThingRatherThanAFailure.
//
// It is reported the way a missing flow is, so the caller can say what to add
// instead of writing half a provider first.
func TestNoCertificateIsAMissingThingRatherThanAFailure(t *testing.T) {
	f := newFake(t)
	f.handle("/api/v3/crypto/certificatekeypairs/", 200, empty())

	if _, err := f.client(t).SigningKey(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestAProviderThatPredatesTheSigningKeyIsRepaired.
//
// EnsureProvider is find-or-create, so the provider the broken live run left
// behind would be found and returned exactly as it is — still HS256, still
// unverifiable, with a wizard reporting success. An operator should not have to
// delete anything in authentik by hand to recover from a bug of ours, so a
// found provider missing its key is patched.
func TestAProviderThatPredatesTheSigningKeyIsRepaired(t *testing.T) {
	f := newFake(t)
	f.handle("/api/v3/providers/oauth2/", 200, list(map[string]any{
		"pk": 7, "name": "doleances console",
		"client_id": "an-id", "client_secret": "a-secret",
	}))
	f.handle("/api/v3/providers/oauth2/7/", 200, map[string]any{
		"pk": 7, "name": "doleances console",
		"client_id": "an-id", "client_secret": "a-secret", "signing_key": "cert-1",
	})

	provider, err := f.client(t).EnsureProvider(context.Background(), ProviderSpec{
		Name:       "doleances console",
		SigningKey: "cert-1",
	})
	if err != nil {
		t.Fatalf("EnsureProvider: %v", err)
	}
	if provider.SigningKey != "cert-1" {
		t.Errorf("signing key = %q, want the repaired provider back", provider.SigningKey)
	}
	if provider.ClientID != "an-id" || provider.ClientSecret != "a-secret" {
		t.Errorf("provider = %+v, want the credentials preserved", provider)
	}

	var patched bool
	for _, call := range f.seen {
		if call == "PATCH /api/v3/providers/oauth2/7/" {
			patched = true
		}
		if call == "POST /api/v3/providers/oauth2/" {
			t.Error("the existing provider was re-created rather than repaired")
		}
	}
	if !patched {
		t.Errorf("calls = %v, want a PATCH of the existing provider", f.seen)
	}
	if sent := f.bodies["PATCH /api/v3/providers/oauth2/7/"]; sent["signing_key"] != "cert-1" {
		t.Errorf("patch body = %v", sent)
	}
}

// TestAProviderThatAlreadySignsIsLeftAlone.
//
// The repair must not become a write on every wizard run: an operator who
// pointed the provider at a certificate of their own choosing keeps it.
func TestAProviderThatAlreadySignsIsLeftAlone(t *testing.T) {
	f := newFake(t)
	f.handle("/api/v3/providers/oauth2/", 200, list(map[string]any{
		"pk": 7, "name": "doleances console", "signing_key": "their-own-cert",
	}))

	provider, err := f.client(t).EnsureProvider(context.Background(), ProviderSpec{
		Name:       "doleances console",
		SigningKey: "cert-1",
	})
	if err != nil {
		t.Fatalf("EnsureProvider: %v", err)
	}
	if provider.SigningKey != "their-own-cert" {
		t.Errorf("signing key = %q, want the operator's own choice untouched", provider.SigningKey)
	}
	for _, call := range f.seen {
		if strings.HasPrefix(call, "PATCH") {
			t.Errorf("calls = %v, want nothing written", f.seen)
		}
	}
}

// TestTheRecoveryQuestionIsAskedOfTheBrand.
//
// The flow existing is not the condition. A stock authentik ships
// `default-recovery-flow` and binds it to nothing, and `POST
// /core/users/{id}/recovery/` then answers `No recovery flow set.` — so a
// check that looked for the flow said the instance was ready and an admin
// created somebody who could never sign in. `Brand.flow_recovery` is the
// binding authentik actually enforces.
func TestTheRecoveryQuestionIsAskedOfTheBrand(t *testing.T) {
	for _, each := range []struct {
		name  string
		brand map[string]any
		want  bool
	}{
		{"bound", map[string]any{"default": true, "flow_recovery": "flow-uuid"}, true},
		{"unbound", map[string]any{"default": true, "flow_recovery": nil}, false},
	} {
		t.Run(each.name, func(t *testing.T) {
			f := newFake(t)
			var query string
			f.mux.HandleFunc("/api/v3/core/brands/", func(w http.ResponseWriter, r *http.Request) {
				query = r.URL.RawQuery
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(list(each.brand)) //nolint:errcheck
			})

			got, err := f.client(t).BrandHasRecoveryFlow(context.Background())
			if err != nil {
				t.Fatalf("BrandHasRecoveryFlow: %v", err)
			}
			if got != each.want {
				t.Errorf("= %v, want %v", got, each.want)
			}
			if !strings.Contains(query, "default=true") {
				t.Errorf("query = %q, want the default brand", query)
			}
		})
	}
}

// TestNoDefaultBrandIsAnAnswerRatherThanAnError.
//
// Nobody can be onboarded through a brand that does not exist, which is the
// same answer as one with nothing bound — and failing the check instead would
// take down a wizard that can still create everything else.
func TestNoDefaultBrandIsAnAnswerRatherThanAnError(t *testing.T) {
	f := newFake(t)
	f.handle("/api/v3/core/brands/", 200, empty())

	got, err := f.client(t).BrandHasRecoveryFlow(context.Background())
	if err != nil {
		t.Fatalf("BrandHasRecoveryFlow: %v", err)
	}
	if got {
		t.Error("an instance with no default brand was reported able to onboard somebody")
	}
}

// TestARefusedTokenIsNotAMissingThing.
//
// 403 and 404 both mean "no", and conflating them sent an operator looking in
// the wrong place: an expired token was reported as a sign-in that could not
// be completed, when the person had authenticated perfectly and the console's
// own credential was dead. Different sentinel, different sentence, different
// person who can fix it.
func TestARefusedTokenIsNotAMissingThing(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		f := newFake(t)
		f.handle("/api/v3/core/users/me/", status,
			map[string]any{"detail": "Token invalid/expired"})

		_, err := f.client(t).Me(context.Background())
		if !errors.Is(err, ErrTokenRefused) {
			t.Errorf("%d: err = %v, want ErrTokenRefused", status, err)
		}
		if errors.Is(err, ErrNotFound) {
			t.Errorf("%d: a refused token was read as a missing thing", status)
		}
		// authentik's own words, so the operator reads the cause rather than a
		// status code.
		if !strings.Contains(err.Error(), "Token invalid/expired") {
			t.Errorf("%d: err = %v, want authentik's own message", status, err)
		}
	}
}

// TestAUserThatIsNotThereIsStillNotFound.
//
// The other side of the same line: 404 must keep meaning what callers already
// depend on it meaning — RolesOf treats it as "holds nothing", which is how
// somebody removed from the directory is refused rather than erroring.
func TestAUserThatIsNotThereIsStillNotFound(t *testing.T) {
	f := newFake(t)
	f.handle("/api/v3/core/users/me/", http.StatusNotFound, nil)

	if _, err := f.client(t).Me(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestARecoveryFlowIsBuiltUnderThisRegistersName(t *testing.T) {
	f := newFake(t)
	made := map[string]int{}
	for _, path := range []string{
		"/api/v3/stages/prompt/prompts/", "/api/v3/stages/prompt/stages/",
		"/api/v3/stages/user_write/", "/api/v3/stages/user_login/",
		"/api/v3/flows/instances/", "/api/v3/flows/bindings/",
	} {
		f.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet {
				json.NewEncoder(w).Encode(empty()) //nolint:errcheck
				return
			}
			made[r.URL.Path]++
			json.NewEncoder(w).Encode(map[string]any{"pk": "made"}) //nolint:errcheck
		})
	}
	f.handle("/api/v3/stages/all/", 200, list(map[string]any{
		"pk": "webauthn-stage", "name": "default-authenticator-webauthn-setup",
		"component": "ak-stage-authenticator-webauthn-form",
	}))
	var patched bool
	f.mux.HandleFunc("/api/v3/core/brands/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPatch {
			patched = true
			json.NewEncoder(w).Encode(map[string]any{}) //nolint:errcheck
			return
		}
		json.NewEncoder(w).Encode(list(map[string]any{ //nolint:errcheck
			"brand_uuid": "brand-1", "default": true, "flow_recovery": nil,
		}))
	})

	got, err := f.client(t).EnsureRecoveryFlow(context.Background(), "doleances")
	if err != nil {
		t.Fatalf("EnsureRecoveryFlow: %v", err)
	}
	if got.Slug != "doleances-recovery" || !got.Created || !got.BoundToBrand {
		t.Errorf("got %+v, want a flow built under the app name and reachable", got)
	}
	if made["/api/v3/stages/prompt/prompts/"] != 2 {
		t.Errorf("%d prompts, want a password and its repeat", made["/api/v3/stages/prompt/prompts/"])
	}
	if made["/api/v3/flows/bindings/"] != 4 {
		t.Errorf("%d bindings, want the prompt, the write, the login and the passkey enrolment",
			made["/api/v3/flows/bindings/"])
	}
	// Without the login stage the flow writes a credential and leaves the
	// browser signed in as whoever it already was — on a first install, the
	// person who ran the wizard.
	if made["/api/v3/stages/user_login/"] != 1 {
		t.Error("the recovery flow does not sign anybody in, so the link is a password reset")
	}
	if !got.Passkey {
		t.Error("the flow stops at a password, which is the credential this project avoids")
	}
	if !patched {
		t.Error("the brand does not point at the flow, so no recovery link can reach it")
	}

	// The write stage must never create people: a recovery flow that could is
	// a way to make an account rather than to recover one.
	if sent := f.bodies["POST /api/v3/stages/user_write/"]; sent["user_creation_mode"] != "never_create" {
		t.Errorf("user_creation_mode = %v, want never_create", sent["user_creation_mode"])
	}
	// And it is reached by a link from a signed-out browser.
	if sent := f.bodies["POST /api/v3/flows/instances/"]; sent["authentication"] != "none" {
		t.Errorf("authentication = %v, want a flow somebody can reach before signing in",
			sent["authentication"])
	}
}

// TestABrandThatAlreadyRecoversKeepsItsOwnFlow.
//
// The one instance-wide effect of all this is the brand binding — it is what
// puts a recovery path on everybody's sign-in. A brand that already names one
// carries a decision about how an operator's other applications recover, and
// overwriting it is exactly what the no-flows rule existed to prevent. That
// half of the rule stands.
func TestABrandThatAlreadyRecoversKeepsItsOwnFlow(t *testing.T) {
	f := newFake(t)
	for _, path := range []string{
		"/api/v3/stages/prompt/prompts/", "/api/v3/stages/prompt/stages/",
		"/api/v3/stages/user_write/", "/api/v3/stages/user_login/",
		"/api/v3/flows/instances/", "/api/v3/flows/bindings/", "/api/v3/stages/all/",
	} {
		f.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet {
				json.NewEncoder(w).Encode(empty()) //nolint:errcheck
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"pk": "made"}) //nolint:errcheck
		})
	}
	f.handle("/api/v3/core/brands/", 200, list(map[string]any{
		"brand_uuid": "brand-1", "default": true, "flow_recovery": "theirs",
	}))

	got, err := f.client(t).EnsureRecoveryFlow(context.Background(), "doleances")
	if err != nil {
		t.Fatalf("EnsureRecoveryFlow: %v", err)
	}
	if got.BoundToBrand {
		t.Error("somebody else's recovery flow was replaced")
	}
	for _, call := range f.seen {
		if strings.HasPrefix(call, "PATCH /api/v3/core/brands/") {
			t.Errorf("the brand was written to: %v", f.seen)
		}
	}
}
