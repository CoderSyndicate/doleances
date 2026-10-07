package console

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/authentik"
)

// fakeDirectory is an authentik that answers enough to be provisioned.
type fakeDirectory struct {
	t            *testing.T
	server       *httptest.Server
	seen         []string
	bodies       map[string]map[string]any
	noFlows      bool
	noCert       bool
	noRecovery   bool
	existing     map[string]bool // paths that already have the object
	groupAdded   bool
	groupRemoved bool

	// admins is what a listing of the admin group answers, so a test can be
	// the last admin or one of several.
	admins []any

	// brandBound makes the default brand already name somebody else's
	// recovery flow, which this wizard must not overwrite.
	brandBound  bool
	tokenMinted bool

	// superuser makes the account a link is asked for administer the
	// directory, which must be refused one.
	superuser bool

	// existingUsers is what a lookup by username finds.
	existingUsers []any
	brandPatched  bool
	made          []string
	bindings      int
	addedUserPK   int
}

func newDirectory(t *testing.T) *fakeDirectory {
	t.Helper()

	d := &fakeDirectory{t: t, bodies: map[string]map[string]any{}, existing: map[string]bool{}}
	d.server = httptest.NewServer(http.HandlerFunc(d.route))
	t.Cleanup(d.server.Close)
	return d
}

func (d *fakeDirectory) route(w http.ResponseWriter, r *http.Request) {
	d.seen = append(d.seen, r.Method+" "+r.URL.Path)
	if r.Body != nil {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) == nil {
			d.bodies[r.Method+" "+r.URL.Path] = body
		}
	}
	w.Header().Set("Content-Type", "application/json")

	empty := map[string]any{"pagination": map[string]any{"next": 0, "count": 0}, "results": []any{}}
	write := func(v any) { json.NewEncoder(w).Encode(v) } //nolint:errcheck

	switch {
	case r.URL.Path == "/api/v3/core/users/me/":
		// A SessionUser wrapper, which is what authentik really answers here.
		write(map[string]any{"user": map[string]any{
			"pk": 1, "username": "dominique", "name": "Dominique", "is_active": true}})

	case r.URL.Path == "/api/v3/core/users/" && r.Method == http.MethodGet:
		// A lookup by username answers from the directory's own people; a
		// listing by group answers with whoever holds that role.
		if r.URL.Query().Get("username") != "" {
			write(map[string]any{
				"pagination": map[string]any{"next": 0, "count": len(d.existingUsers)},
				"results":    d.existingUsers,
			})
			return
		}
		write(map[string]any{
			"pagination": map[string]any{"next": 0, "count": len(d.admins)},
			"results":    d.admins,
		})

	case r.URL.Path == "/api/v3/core/users/" && r.Method == http.MethodPost:
		// Echoes what was asked for, so a test can tell who was created.
		sent := d.bodies["POST /api/v3/core/users/"]
		write(map[string]any{
			"pk": 5, "username": sent["username"], "name": sent["name"], "is_active": true})

	case r.URL.Path == "/api/v3/flows/instances/" && r.Method == http.MethodPost:
		d.made = append(d.made, r.URL.Path)
		write(map[string]any{"pk": "flow-made"})

	case r.URL.Path == "/api/v3/flows/instances/":
		slug := r.URL.Query().Get("slug")
		if strings.HasSuffix(slug, "-recovery") {
			// The flow this wizard builds does not exist until it builds it.
			write(empty)
			return
		}
		if d.noFlows && slug != "default-recovery-flow" {
			write(empty)
			return
		}
		if d.noRecovery && slug == "default-recovery-flow" {
			write(empty)
			return
		}
		write(map[string]any{
			"pagination": map[string]any{"next": 0, "count": 1},
			"results":    []any{map[string]any{"pk": "flow-" + slug, "slug": slug}},
		})

	case r.URL.Path == "/api/v3/core/brands/":
		// The binding authentik enforces when a recovery link is asked for.
		brand := map[string]any{
			"domain": "authentik-default", "default": true, "brand_uuid": "brand-1"}
		if !d.noRecovery {
			brand["flow_recovery"] = "flow-recovery-uuid"
		}
		if d.brandBound {
			brand["flow_recovery"] = "somebody-elses-flow"
		}
		write(map[string]any{
			"pagination": map[string]any{"next": 0, "count": 1},
			"results":    []any{brand},
		})

	case strings.HasPrefix(r.URL.Path, "/api/v3/core/brands/") && r.Method == http.MethodPatch:
		d.brandPatched = true
		write(map[string]any{"brand_uuid": "brand-1"})

	// The objects a recovery flow is built from. All find-or-create, so an
	// empty listing is the first run and the POST is what follows.
	case r.URL.Path == "/api/v3/stages/prompt/prompts/",
		r.URL.Path == "/api/v3/stages/prompt/stages/",
		r.URL.Path == "/api/v3/stages/user_write/",
		r.URL.Path == "/api/v3/stages/user_login/":
		if r.Method == http.MethodGet {
			write(empty)
			return
		}
		d.made = append(d.made, r.URL.Path)
		write(map[string]any{"pk": "made-" + fmt.Sprint(len(d.made))})

	case r.URL.Path == "/api/v3/stages/all/":
		// A stock authentik ships one, and it is reused rather than created.
		write(map[string]any{
			"pagination": map[string]any{"next": 0, "count": 1},
			"results": []any{map[string]any{
				"pk":        "webauthn-stage",
				"name":      "default-authenticator-webauthn-setup",
				"component": "ak-stage-authenticator-webauthn-form",
			}},
		})

	case r.URL.Path == "/api/v3/flows/bindings/":
		if r.Method == http.MethodGet {
			write(empty)
			return
		}
		d.bindings++
		write(map[string]any{"pk": "binding"})

	case r.URL.Path == "/api/v3/crypto/certificatekeypairs/":
		if d.noCert {
			write(empty)
			return
		}
		write(map[string]any{
			"pagination": map[string]any{"next": 0, "count": 1},
			"results": []any{map[string]any{
				"pk": "cert-1", "name": "authentik Self-signed Certificate"}},
		})

	case r.URL.Path == "/api/v3/propertymappings/provider/scope/":
		write(map[string]any{
			"pagination": map[string]any{"next": 0, "count": 1},
			"results":    []any{map[string]any{"pk": "scope-openid", "scope_name": "openid"}},
		})

	case r.URL.Path == "/api/v3/providers/oauth2/":
		if r.Method == http.MethodGet {
			write(empty)
			return
		}
		write(map[string]any{"pk": 7, "name": "doleances console",
			"client_id": "an-id", "client_secret": "a-secret"})

	case r.URL.Path == "/api/v3/core/applications/":
		if r.Method == http.MethodGet {
			write(empty)
			return
		}
		write(map[string]any{"pk": "app-uuid", "slug": "doleances"})

	case r.URL.Path == "/api/v3/core/groups/":
		if r.Method == http.MethodGet {
			write(empty)
			return
		}
		name, _ := d.bodies["POST /api/v3/core/groups/"]["name"].(string)
		write(map[string]any{"pk": "uuid-" + name, "name": name})

	// The console's own credential: absent on a first run, then readable back
	// by name, which is what stops a second run minting a duplicate.
	case r.URL.Path == "/api/v3/core/tokens/":
		if r.Method == http.MethodPost {
			d.tokenMinted = true
			write(map[string]any{"identifier": "console"})
			return
		}
		if d.tokenMinted {
			write(map[string]any{
				"pagination": map[string]any{"next": 0, "count": 1},
				"results":    []any{map[string]any{"identifier": "console"}},
			})
			return
		}
		write(empty)

	case strings.HasSuffix(r.URL.Path, "/view_key/"):
		write(map[string]any{"key": "a-minted-token"})

	// Who a link is about to be minted for — read first, so a directory
	// superuser can be refused one.
	case regexp.MustCompile(`^/api/v3/core/users/\d+/$`).MatchString(r.URL.Path):
		write(map[string]any{
			"pk": 5, "username": "luscus", "is_active": true,
			"is_superuser": d.superuser,
		})

	case strings.HasSuffix(r.URL.Path, "/recovery/"):
		write(map[string]any{"link": "https://auth.example/recover/one-time"})

	case strings.HasSuffix(r.URL.Path, "/remove_user/"):
		d.groupRemoved = true
		w.WriteHeader(http.StatusNoContent)

	case strings.HasSuffix(r.URL.Path, "/add_user/"):
		d.groupAdded = true
		if body := d.bodies[r.Method+" "+r.URL.Path]; body != nil {
			if pk, ok := body["pk"].(float64); ok {
				d.addedUserPK = int(pk)
			}
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (d *fakeDirectory) client(t *testing.T) *authentik.Client {
	t.Helper()

	c, err := authentik.New(d.server.URL, "a-token")
	if err != nil {
		t.Fatalf("authentik.New: %v", err)
	}
	return c
}

// TestProvisioningCreatesThisRegistersOwnObjects — and the admin is whoever
// held the token, which is not a field anybody is asked to type.
func TestProvisioningCreatesThisRegistersOwnObjects(t *testing.T) {
	d := newDirectory(t)

	got, err := provision(context.Background(), d.client(t), "https://console.example", "doleances", firstAdmin{})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if !got.Done {
		t.Fatalf("provisioning did not finish: %+v", got)
	}
	if got.AdminUsername != "dominique" {
		t.Errorf("first admin = %q, want whoever held the token", got.AdminUsername)
	}
	if !got.RecoveryReady {
		t.Error("a recovery flow was present and not reported")
	}

	p := got.Provisioned
	if p.ClientID != "an-id" || p.ClientSecret != "a-secret" {
		t.Errorf("credentials = %+v", p)
	}
	if p.AdminGroup != "uuid-doleances_admin" || p.CuratorGroup != "uuid-doleances_curator" {
		t.Errorf("groups = %q and %q", p.AdminGroup, p.CuratorGroup)
	}
	if p.AppName != "doleances" {
		t.Errorf("app name = %q", p.AppName)
	}
	if p.ProvisionedBy != "dominique" {
		t.Errorf("provisioned_by = %q", p.ProvisionedBy)
	}

	// The person holding the token was made an admin. Without this the wizard
	// finishes with a console nobody can administer — a door nobody can open.
	if !d.groupAdded || d.addedUserPK != 1 {
		t.Error("the person who ran the wizard was not made an admin")
	}

	// The redirect URI is this console's callback, exactly.
	sent := d.bodies["POST /api/v3/providers/oauth2/"]
	uris, _ := sent["redirect_uris"].([]any)
	first, _ := uris[0].(map[string]any)
	if first["url"] != "https://console.example/auth/callback" {
		t.Errorf("redirect = %v", first["url"])
	}
}

// TestTheTokenIsProvedBeforeAnythingIsCreated.
//
// An operator who made a token without enough permission, or typed the URL
// wrong, should find out before their directory has half a provider in it.
func TestTheTokenIsProvedBeforeAnythingIsCreated(t *testing.T) {
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer refusing.Close()

	c, err := authentik.New(refusing.URL, "a-bad-token")
	if err != nil {
		t.Fatalf("authentik.New: %v", err)
	}

	got, err := provision(context.Background(), c, "https://console.example", "doleances", firstAdmin{})
	if err == nil {
		t.Fatal("a refused token provisioned something")
	}
	if got.Done {
		t.Error("provisioning reported success on a refused token")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Errorf("the error does not say the token was refused: %v", err)
	}
}

// TestMissingFlowsCreateNothingAndSaySo.
//
// Flows are instance-wide and shared with every other application in the
// operator's directory. This will not make them, so it has to leave the
// directory untouched and explain — rather than creating half a provider and
// failing on the rest.
func TestMissingFlowsCreateNothingAndSaySo(t *testing.T) {
	d := newDirectory(t)
	d.noFlows = true

	got, err := provision(context.Background(), d.client(t), "https://console.example", "doleances", firstAdmin{})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if got.Done {
		t.Error("provisioning claimed success without the flows it needs")
	}
	if len(got.Missing) == 0 {
		t.Error("nothing was reported as missing")
	}
	for _, call := range d.seen {
		if strings.HasPrefix(call, "POST ") {
			t.Errorf("something was created although the instance was not ready: %s", call)
		}
	}
}

// TestNoRecoveryFlowStillProvisionsAndSaysWhatIsLost.
//
// Everything can be created without it; what cannot happen is onboarding
// anybody. That is a real state and the wizard has to name it rather than
// leave an admin to discover it the first time they add a colleague.
func TestAnInstanceWithNoRecoveryFlowGetsOneBuilt(t *testing.T) {
	d := newDirectory(t)
	d.noRecovery = true

	got, err := provision(context.Background(), d.client(t), "https://console.example", "doleances", firstAdmin{})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if !got.Done {
		t.Fatal("a missing recovery flow stopped provisioning entirely")
	}
	if !got.RecoveryReady {
		t.Error("the flow was built and nobody can still be onboarded")
	}
	if !got.RecoveryFlow.Created || got.RecoveryFlow.Slug != "doleances-recovery" {
		t.Errorf("RecoveryFlow = %+v, want one built under this register's name", got.RecoveryFlow)
	}
	if len(d.made) != 6 {
		t.Errorf("made = %v, want two prompts, a prompt stage, a write stage, a login stage "+
			"and the flow", d.made)
	}
	if d.bindings != 4 {
		t.Errorf("%d stage bindings, want the prompt, the write, the login and the passkey",
			d.bindings)
	}
	if !got.RecoveryFlow.Passkey {
		t.Error("the flow stops at a password, which is the credential this project avoids")
	}
	if !d.brandPatched {
		t.Error("the flow was built and the brand does not point at it, so no link can reach it")
	}
	if len(got.Missing) != 0 {
		t.Errorf("Missing = %v, want nothing left for an operator to do", got.Missing)
	}
}

// TestABrandThatAlreadyRecoversIsNotTouchedAtAll.
//
// Nothing is built either: a directory whose brand already names a recovery
// flow can already hand an account over, and this wizard has no business
// adding a second one beside it. The guard that refuses to overwrite the
// binding lives in the client — see the authentik package — because this path
// never reaches it.
func TestABrandThatAlreadyRecoversIsNotTouchedAtAll(t *testing.T) {
	d := newDirectory(t)

	got, err := provision(context.Background(), d.client(t), "https://console.example", "doleances", firstAdmin{})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if !got.RecoveryReady {
		t.Error("a brand that can already recover was reported as unable to")
	}
	if d.brandPatched {
		t.Error("somebody else's recovery flow was overwritten")
	}
	if len(d.made) != 0 {
		t.Errorf("made = %v, want nothing built beside what already works", d.made)
	}
}

// TestRunningItTwiceIsSafe. A wizard that failed halfway must be runnable
// again: the second attempt finds what the first made rather than colliding
// with it.
func TestRunningItTwiceIsSafe(t *testing.T) {
	d := newDirectory(t)
	ctx := context.Background()

	if _, err := provision(ctx, d.client(t), "https://console.example", "doleances", firstAdmin{}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := len(d.seen)

	got, err := provision(ctx, d.client(t), "https://console.example", "doleances", firstAdmin{})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !got.Done {
		t.Error("a second run did not finish")
	}
	if len(d.seen) <= first {
		t.Error("the second run made no calls at all, which cannot be right")
	}
}

// TestTheNameIsThePrefixForEverything.
//
// One authentik may hold more than one of these registers — a test beside the
// real thing is the obvious case. Without a prefix the second installation
// finds the first one's groups already there, joins them, and quietly hands
// its curators power over somebody else's register.
func TestTheNameIsThePrefixForEverything(t *testing.T) {
	d := newDirectory(t)

	got, err := provision(context.Background(), d.client(t), "https://console.example", "doleances-test", firstAdmin{})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if got.AppName != "doleances-test" {
		t.Errorf("app name = %q", got.AppName)
	}
	if got.Provisioned.AdminGroup != "uuid-doleances-test_admin" {
		t.Errorf("admin group = %q, want the prefixed name", got.Provisioned.AdminGroup)
	}
	if got.Provisioned.CuratorGroup != "uuid-doleances-test_curator" {
		t.Errorf("curator group = %q, want the prefixed name", got.Provisioned.CuratorGroup)
	}

	// The application is named by it too, which is what the OIDC issuer URL is
	// built from — so two installations cannot share a discovery document.
	app := d.bodies["POST /api/v3/core/applications/"]
	if app["slug"] != "doleances-test" {
		t.Errorf("application slug = %v", app["slug"])
	}
}

// TestANameThatWouldBreakSomethingIsRefused. It becomes a slug, two group
// names and part of an issuer URL; one that was legal in one of those and not
// the others would fail somewhere far from where it was typed.
func TestANameThatWouldBreakSomethingIsRefused(t *testing.T) {
	d := newDirectory(t)

	for _, bad := range []string{"", "Doléances", "has space", "UPPER", "-leading", "trailing-", "a/b"} {
		if _, err := provision(context.Background(), d.client(t), "https://c.example", bad, firstAdmin{}); err == nil {
			t.Errorf("%q was accepted as a name", bad)
		}
	}
}

// TestTheProviderIsGivenSomethingToSignWith.
//
// Without it authentik signs the ID token HS256 with the client secret, which
// is exactly what the first live run did: a complete, correct directory and a
// console that refused every sign-in afterwards. The key has to reach the
// create body, so that is what is asserted rather than that a lookup happened.
func TestTheProviderIsGivenSomethingToSignWith(t *testing.T) {
	d := newDirectory(t)

	result, err := provision(context.Background(), d.client(t),
		"https://console.example", "doleances", firstAdmin{})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if !result.Done {
		t.Fatalf("missing = %v, want a finished provisioning", result.Missing)
	}
	if sent := d.bodies["POST /api/v3/providers/oauth2/"]; sent["signing_key"] != "cert-1" {
		t.Errorf("signing_key = %v, want the certificate on the create body", sent["signing_key"])
	}
}

// TestNoCertificateStopsItBeforeAnythingIsCreated.
//
// Reported the way a missing flow is. The alternative is what happened live —
// a provider, an application and two groups in somebody's directory, and a
// sign-in nobody can complete.
func TestNoCertificateStopsItBeforeAnythingIsCreated(t *testing.T) {
	d := newDirectory(t)
	d.noCert = true

	result, err := provision(context.Background(), d.client(t),
		"https://console.example", "doleances", firstAdmin{})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if result.Done {
		t.Error("provisioning said done with nothing to sign tokens with")
	}
	if len(result.Missing) == 0 {
		t.Error("nothing was reported missing, so an operator has nothing to act on")
	}
	for _, call := range d.seen {
		if call == "POST /api/v3/providers/oauth2/" || call == "POST /api/v3/core/groups/" {
			t.Errorf("calls = %v, want nothing created", d.seen)
			break
		}
	}
}

// TestTheConsoleKeepsATokenOfItsOwn.
//
// The token pasted into the wizard is a **bootstrap** credential: somebody
// made it by hand so a first run could create the provider it could not yet
// authenticate against, and authentik expires it after thirty minutes because
// that is what such a token is for. Keeping it as the console's permanent
// credential conflated the two, and a live installation showed the cost —
// setup worked, the admin signed in, and half an hour later every sign-in and
// every curation decision failed at once.
func TestTheConsoleKeepsATokenOfItsOwn(t *testing.T) {
	d := newDirectory(t)

	got, err := provision(context.Background(), d.client(t),
		"https://console.example", "doleances", firstAdmin{})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if got.ConsoleToken.Key != "a-minted-token" {
		t.Errorf("ConsoleToken = %+v, want the minted key, not the pasted one", got.ConsoleToken)
	}
	if got.ConsoleToken.Identifier != "doleances-console" {
		t.Errorf("identifier = %q, want it named for this register so it can be revoked",
			got.ConsoleToken.Identifier)
	}

	// Non-expiring is the whole point, and it belongs to the person who ran
	// the wizard, so it carries exactly the rights provisioning already used.
	sent := d.bodies["POST /api/v3/core/tokens/"]
	if sent["expiring"] != false {
		t.Errorf("expiring = %v, want a credential that does not stop working", sent["expiring"])
	}
	if sent["intent"] != "api" {
		t.Errorf("intent = %v, want api", sent["intent"])
	}
}

// TestTheWizardCanNameSomebodyOtherThanTheTokensOwner.
//
// On a fresh authentik the token's owner is `akadmin` — the bootstrap account,
// not a person. Left as the first admin, the only identity in the audit log is
// a shared built-in nobody signs in as, and the human actually running the
// wizard is never onboarded: they would invite themselves from the console
// afterwards, which is the same flow with an extra step and a wrong row left
// behind.
func TestTheWizardCanNameSomebodyOtherThanTheTokensOwner(t *testing.T) {
	d := newDirectory(t)

	got, err := provision(context.Background(), d.client(t),
		"https://console.example", "doleances",
		firstAdmin{Username: "camille", Name: "Camille", Email: "camille@example.org"})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if got.AdminUsername != "camille" {
		t.Errorf("AdminUsername = %q, want the person the wizard named", got.AdminUsername)
	}
	if got.TokenOwner != "dominique" {
		t.Errorf("TokenOwner = %q, want whose token did the work", got.TokenOwner)
	}
	if got.AdminLink == "" {
		t.Error("the named admin got no way in, so the register has an admin who cannot reach it")
	}

	// Created, and put in both groups rather than one.
	if sent := d.bodies["POST /api/v3/core/users/"]; sent["username"] != "camille" {
		t.Errorf("created = %v, want the named person", sent)
	}
	var added int
	for _, call := range d.seen {
		if strings.HasSuffix(call, "/add_user/") {
			added++
		}
	}
	if added != 2 {
		t.Errorf("%d group memberships, want both roles — there is nobody else to curate", added)
	}
}

// TestANamedAdminWhoAlreadyExistsIsAdopted.
//
// The ordinary case on a directory somebody already works in: an operator
// names their own account. Creating a second one under a suffixed name, or
// refusing, would both be worse than using the one that is there.
func TestANamedAdminWhoAlreadyExistsIsAdopted(t *testing.T) {
	d := newDirectory(t)
	d.existingUsers = []any{map[string]any{
		"pk": 42, "username": "camille", "name": "Camille", "is_active": true}}

	got, err := provision(context.Background(), d.client(t),
		"https://console.example", "doleances", firstAdmin{Username: "camille"})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if got.AdminUsername != "camille" {
		t.Errorf("AdminUsername = %q", got.AdminUsername)
	}
	if _, created := d.bodies["POST /api/v3/core/users/"]; created {
		t.Error("a second account was created for somebody who already exists")
	}
}
