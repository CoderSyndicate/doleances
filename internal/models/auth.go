package models

import "time"

// AuthSettingsID is the fixed identifier of the single settings row.
const AuthSettingsID = "auth"

// DefaultAppName is what this register calls itself in somebody else's
// directory, unless the operator says otherwise.
const DefaultAppName = "doleances"

// The two role suffixes.
//
// **The names they build are a contract, not a detail.** They are what
// authentik stores, what the console maps to permissions, and what somebody
// reading the audit log has to recognise. Renaming one later means editing a
// live directory by hand while people are using it — which is why the prefix
// is asked for once, at setup, and stored rather than recomputed.
//
// The prefix exists because one authentik instance may hold more than one of
// these registers, or a test alongside the real thing. Without it the second
// installation would find the first one's groups already there, join them, and
// quietly give its curators power over somebody else's register.
const (
	// RoleSuffixAdmin may add and remove people and change their roles.
	RoleSuffixAdmin = "_admin"

	// RoleSuffixCurator may accept and reject — messages, groups, actions and
	// the subject questions.
	RoleSuffixCurator = "_curator"
)

// AdminGroupName and CuratorGroupName build the two group names from the
// application name.
func AdminGroupName(app string) string   { return app + RoleSuffixAdmin }
func CuratorGroupName(app string) string { return app + RoleSuffixCurator }

// ValidAppName reports whether a name is safe to use as a slug and as a group
// name prefix.
//
// Narrow on purpose: it becomes an authentik application slug, two group names
// and part of an OIDC issuer URL, and a name that was legal in one of those and
// not the others would fail somewhere far from where it was typed.
func ValidAppName(name string) bool {
	if name == "" || len(name) > 48 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case (r == '-' || r == '_') && i > 0 && i < len(name)-1:
		default:
			return false
		}
	}
	return true
}

// AuthSettings is how the console reaches the identity provider its staff
// authenticate against.
//
// # Why this lives in the backend
//
// The console holds no database: *"everything it shows and every decision it
// records goes through the backend's API"*. So the settings its own login
// depends on live here, exactly as the LLM settings do, and for the same
// reason — the backend owns every write.
//
// # Both secrets are encrypted at rest
//
// `ClientSecret` and `APIToken` go through internal/secret on the way in and
// out. A database backup carries neither in a form anybody can use, which is
// the caveat the LLM key's comment used to carry and no longer has to.
//
// The API token is the more dangerous of the two by some distance: it can
// create people in the operator's directory. That is said on the page that
// asks for it rather than only here.
type AuthSettings struct {
	Model

	// Provisioned says the wizard has run to completion.
	//
	// It is what closes the setup door. A console that is provisioned does not
	// register the wizard route at all — absent rather than refusing, the same
	// way switching groups off removes their routes rather than answering 404
	// from a handler.
	Provisioned bool `json:"provisioned"`

	// AppName is what this register is called in the directory: the
	// application's slug, and the prefix both group names are built from.
	//
	// Stored because it cannot be recomputed. An operator who set up a second
	// installation under a different name must not have this one start looking
	// for groups it never created.
	AppName string `gorm:"size:64" json:"app_name,omitempty"`

	// InstanceURL is the authentik instance, without the API path.
	InstanceURL string `gorm:"size:512" json:"instance_url,omitempty"`

	// ConsoleURL is where staff actually reach this console.
	//
	// **Not `--site-url`**, which is the *public register's* address and is
	// given to all three services: the console needs it to link a group's name
	// to its page. Using it here would register a redirect at the frontend's
	// address and then send staff to it, which fails at sign-in with an error
	// only visible to whoever tried.
	//
	// Stored rather than derived because authentik matches a redirect URI
	// strictly: the one registered at provisioning and the one sent at every
	// later sign-in have to be the same string, and two places that computed
	// it would be two places to disagree.
	ConsoleURL string `gorm:"size:512" json:"console_url,omitempty"`

	// ClientID and ClientSecret are the OAuth2 credentials of the provider the
	// wizard created. The secret is never returned by the API and never
	// reaches a template.
	ClientID     string `gorm:"size:256" json:"client_id,omitempty"`
	ClientSecret string `gorm:"size:1024" json:"-"`

	// APIToken is what the console manages people with. Never returned,
	// never rendered; the console is told only whether one is stored.
	APIToken string `gorm:"size:1024" json:"-"`

	// AdminGroup and CuratorGroup are the identifiers of the two groups, kept
	// so membership can be changed without looking them up by name on every
	// request. The names are the contract; these are the handles.
	AdminGroup   string `gorm:"size:64" json:"admin_group,omitempty"`
	CuratorGroup string `gorm:"size:64" json:"curator_group,omitempty"`

	// ProvisionedAt and ProvisionedBy record who opened the door and when.
	//
	// The wizard is the one privileged action in this project that happens
	// before anybody can be identified — there is no OIDC yet when it runs, so
	// it cannot be audited the way every later change is. Recording the
	// authentik account whose token was used is the nearest thing to a name,
	// and it is better than the row being silent about how this register came
	// to trust the directory it trusts.
	ProvisionedAt *time.Time `json:"provisioned_at,omitempty"`
	ProvisionedBy string     `gorm:"size:256" json:"provisioned_by,omitempty"`
}

// DefaultAuthSettings is what a fresh installation starts from: nothing
// configured, and a wizard waiting on the maintenance port.
func DefaultAuthSettings() AuthSettings {
	return AuthSettings{Model: Model{ID: AuthSettingsID}}
}

// HasToken reports whether an API token is stored, which is all the console is
// ever told about it.
func (a AuthSettings) HasToken() bool { return a.APIToken != "" }
