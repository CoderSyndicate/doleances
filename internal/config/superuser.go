package config

import (
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// KeySuperuser reopens the setup door on a console that is already configured.
const KeySuperuser = "superuser"

// RegisterSuperuserFlag declares the door back into setup.
//
// # Why this is not `--development`
//
// They look similar and are opposites in the way that matters.
// `--development` disables authentication entirely, for local work, on a
// console nobody is relying on. This disables nothing: it puts the setup
// wizard back on the maintenance port of a console that is otherwise running
// normally, fully authenticated, with real curators using it.
//
// Folding the two together would mean that "the identity provider moved and I
// need to re-point this console" implied "turn off authentication for
// everybody first", which is exactly backwards — the moment an operator most
// needs the rest of the console to stay locked is the moment they are
// changing how it locks.
//
// # What it actually opens
//
// Only the wizard, and only on the maintenance port, which is never published
// and reachable only from inside the deployment. So two deliberate things
// stand between a running register and being re-pointed at a different
// directory: a restart carrying this flag, and somebody on the far side of the
// network boundary. The backend refuses to overwrite a provisioned
// configuration unless the console says plainly that it means to, and the
// console says that only when this flag is on.
//
// It never defaults to true, and every startup that enables it says so at
// WARN — the same rule `--development` carries, for the same reason: a switch
// that is quiet when it is on is a switch somebody leaves on.
func RegisterSuperuserFlag(cmd *cobra.Command) {
	cmd.PersistentFlags().Bool(KeySuperuser, false,
		"reopen the setup wizard on the maintenance port for a console that is already configured")
}

// SuperuserEnabled reports whether the setup door is open.
func SuperuserEnabled() bool { return viper.GetBool(KeySuperuser) }

// WarnIfSuperuser says so at startup, loudly, every time.
//
// Separate from the flag's declaration because it has to be called where a
// logger exists, and separate from `--development`'s warning because they are
// different sentences: one says nobody is being authenticated, the other says
// the door that decides who authenticates anybody is standing open.
func WarnIfSuperuser() {
	if !SuperuserEnabled() {
		return
	}
	log.Warn().Msg(
		"SUPERUSER MODE: the setup wizard is open on the maintenance port and can " +
			"replace this console's identity provider — restart without --superuser when done")
}

// KeyConsoleURL is where staff reach the console.
const KeyConsoleURL = "console-url"

// RegisterConsoleURLFlag declares the console's own public address.
//
// **Distinct from `--site-url`**, which is the public register's address and
// is given to all three services — the console needs that one to link a
// group's name to its page. This one is the console itself, and it exists
// because OAuth needs a redirect URI: authentik matches it strictly, so the
// address registered at provisioning and the address staff are sent back to
// must be the same string.
//
// Only a default for the setup form. What the handshake uses afterwards is the
// address stored when the wizard ran, so an operator who corrects the field is
// corrected everywhere rather than in one of two places.
func RegisterConsoleURLFlag(cmd *cobra.Command) {
	cmd.PersistentFlags().String(KeyConsoleURL, "",
		"where staff reach this console, for the OAuth redirect (not --site-url)")
}

// LoadConsoleURL reads it.
func LoadConsoleURL() string { return viper.GetString(KeyConsoleURL) }
