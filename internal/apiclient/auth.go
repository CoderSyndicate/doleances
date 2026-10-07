package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// AuthSettings is how the console reaches its identity provider, as the
// backend is willing to describe it.
//
// **Neither secret is in here**, and neither ever will be: the backend does
// not return the OAuth client secret or the authentik API token to anybody.
// HasToken is the whole of what a page is told about the second one.
type AuthSettings struct {
	Provisioned bool   `json:"provisioned"`
	AppName     string `json:"app_name,omitempty"`
	InstanceURL string `json:"instance_url,omitempty"`
	ConsoleURL  string `json:"console_url,omitempty"`
	ClientID    string `json:"client_id,omitempty"`
	HasToken    bool   `json:"has_token"`

	AdminGroup   string `json:"admin_group,omitempty"`
	CuratorGroup string `json:"curator_group,omitempty"`

	ProvisionedBy string `json:"provisioned_by,omitempty"`
	ProvisionedAt string `json:"provisioned_at,omitempty"`

	// The role names, carried from the backend so the console and the
	// directory cannot drift on what a role is called.
	AdminGroupName   string `json:"admin_group_name"`
	CuratorGroupName string `json:"curator_group_name"`
}

// AuthSettings reads the identity provider configuration.
func (c *Client) AuthSettings(ctx context.Context) (AuthSettings, error) {
	var settings AuthSettings
	if err := c.get(ctx, "/v1/auth/settings", &settings); err != nil {
		return AuthSettings{}, err
	}
	return settings, nil
}

// ProvisionedAuth is what the wizard reports having created.
type ProvisionedAuth struct {
	AppName      string `json:"app_name"`
	InstanceURL  string `json:"instance_url"`
	ConsoleURL   string `json:"console_url"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	APIToken     string `json:"api_token"`
	AdminGroup   string `json:"admin_group"`
	CuratorGroup string `json:"curator_group"`

	// ProvisionedBy is the authentik account whose token did it — the nearest
	// thing to a name on an action that happens before anybody can be
	// identified by OIDC.
	ProvisionedBy string `json:"provisioned_by,omitempty"`

	// Reprovision replaces a configuration that already exists. The backend
	// refuses the write without it, and the console sends it only when it was
	// started with --superuser.
	Reprovision bool `json:"reprovision,omitempty"`
}

// SaveAuthSettings records what the wizard provisioned.
func (c *Client) SaveAuthSettings(ctx context.Context, provisioned ProvisionedAuth) (AuthSettings, error) {
	body, err := json.Marshal(provisioned)
	if err != nil {
		return AuthSettings{}, fmt.Errorf("encode the auth settings: %w", err)
	}

	var settings AuthSettings
	err = c.sendJSON(ctx, http.MethodPut, "/v1/auth/settings", body, &settings)
	if err != nil {
		return AuthSettings{}, err
	}
	return settings, nil
}

// AuthCredentials are the two secrets the console needs to authenticate
// people: one to exchange an authorization code, one to read the directory.
//
// See the backend handler for why this exists and why it is a departure from
// exposed services holding no credentials.
type AuthCredentials struct {
	ClientSecret string `json:"client_secret"`
	APIToken     string `json:"api_token"`
}

// AuthCredentials reads them. Called once, at console startup.
func (c *Client) AuthCredentials(ctx context.Context) (AuthCredentials, error) {
	var credentials AuthCredentials
	if err := c.get(ctx, "/v1/auth/credentials", &credentials); err != nil {
		return AuthCredentials{}, err
	}
	return credentials, nil
}
