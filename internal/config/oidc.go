package config

import (
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// KeyOIDCIssuer is where the identity provider lives, when a deployment
// already knows.
const KeyOIDCIssuer = "oidc-issuer"

// RegisterOIDCIssuerFlag declares the authentik instance's address.
//
// # It configures a form field, not the console
//
// This is **not** where the console reads its identity provider from: that is
// the settings row the wizard wrote, because an operator who corrected the
// field during setup must be corrected everywhere rather than in one of two
// places. Nothing here is consulted after provisioning, and changing it on a
// running installation changes nothing.
//
// What it does is fill the field in. A deployment that already knows its own
// authentik — which is every deployment driven by automation — should not make
// a person read it off another screen and retype it into the wizard, where a
// typo is a provider registered against the wrong host and a sign-in that
// fails somewhere far away.
//
// No default, because a wrong guess here is worse than an empty field: an
// address somebody did not type is one they will not check.
//
// The environment counterpart is `DOLEANCES_OIDC_ISSUER`, which is the usual
// bootstrap doing its work rather than anything special to this key — see
// Bootstrap for the prefix and the `-` to `_` replacement that makes it a
// legal shell identifier.
func RegisterOIDCIssuerFlag(cmd *cobra.Command) {
	cmd.PersistentFlags().String(KeyOIDCIssuer, "",
		"authentik's address, prefilled into the setup wizard (DOLEANCES_OIDC_ISSUER)")
}

// LoadOIDCIssuer reads it.
func LoadOIDCIssuer() string { return viper.GetString(KeyOIDCIssuer) }
