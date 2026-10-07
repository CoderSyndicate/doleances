package config

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Authentication and notification configuration keys.
const (
	// KeyPasskeyRPID overrides the relying party identifier.
	KeyPasskeyRPID = "passkey-rp-id"

	// KeyPasskeyOrigins adds further origins a ceremony may come from.
	KeyPasskeyOrigins = "passkey-origins"

	// KeyPushPublicKey and KeyPushPrivateKey are the VAPID pair.
	KeyPushPublicKey  = "push-public-key"
	KeyPushPrivateKey = "push-private-key"

	// KeyPushSubject is how a push service reaches the operator.
	KeyPushSubject = "push-subject"
)

// Auth is how this register identifies itself to a browser.
type Auth struct {
	// PublicURL is the origin readers use — `--site-url`. Everything else here
	// is derived from it unless overridden, because one wrong answer in three
	// places is three bugs and one setting is one.
	PublicURL string

	// RPID is the relying party identifier: the registrable domain, no scheme
	// and no port.
	//
	// **A passkey is bound to this for ever.** Changing it does not migrate
	// anything — the private halves live on people's devices and cannot be
	// re-keyed — so every credential in the database stops working at once.
	// It has to be the domain the register will still be on in five years.
	RPID string

	// Origins are the full origins a ceremony may come from. A browser signs
	// the page's own origin into its answer and a mismatch is refused, which
	// is what stops a passkey for this register being usable by a site that
	// merely looks like it.
	Origins []string
}

// Push is the Web Push configuration.
//
// **Absent keys switch push off rather than failing startup.** The in-app
// notification list is the channel and the push is a tap on the shoulder: an
// installation with no keys still tells everybody everything, just not before
// they next open the site. Refusing to start would be treating the tap as the
// message.
type Push struct {
	PublicKey  string
	PrivateKey string

	// Subject is a `mailto:` or an `https://` URL by which a push service can
	// reach whoever runs this. It is required by VAPID and it is not
	// decoration: it is how a provider tells an operator their sending is
	// broken instead of silently dropping it.
	Subject string
}

// Configured reports whether push can be sent at all.
func (p Push) Configured() bool { return p.PublicKey != "" && p.PrivateKey != "" }

// RegisterAuthFlags declares the flags the backend needs to be a relying
// party. Only the backend registers these: it is the only service that
// verifies a ceremony or sends a push.
func RegisterAuthFlags(cmd *cobra.Command) {
	f := cmd.PersistentFlags()
	f.String(KeyPasskeyRPID, "",
		"relying party id, if not the host of "+KeySiteURL)
	f.StringSlice(KeyPasskeyOrigins, nil,
		"further origins a passkey ceremony may come from")
	f.String(KeyPushPublicKey, "", "VAPID public key — empty switches Web Push off")
	f.String(KeyPushPrivateKey, "", "VAPID private key — empty switches Web Push off")
	f.String(KeyPushSubject, "",
		"mailto: or https: URL a push service can reach the operator at")
}

// LoadAuth resolves the relying party configuration.
func LoadAuth() (Auth, error) {
	public := LoadSiteURL()
	if public == "" {
		return Auth{}, fmt.Errorf("%s is required: a passkey is bound to its host", KeySiteURL)
	}

	parsed, err := url.Parse(public)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return Auth{}, fmt.Errorf("invalid %s %q: want scheme://host", KeySiteURL, public)
	}
	public = strings.TrimSuffix(parsed.Scheme+"://"+parsed.Host, "/")

	auth := Auth{
		PublicURL: public,
		RPID:      strings.TrimSpace(viper.GetString(KeyPasskeyRPID)),
		Origins:   []string{public},
	}
	if auth.RPID == "" {
		// The hostname, without the port: a relying party id is a domain, and
		// a port in it is refused by every browser.
		auth.RPID = parsed.Hostname()
	}

	for _, origin := range viper.GetStringSlice(KeyPasskeyOrigins) {
		origin = strings.TrimSpace(strings.TrimSuffix(origin, "/"))
		if origin != "" && origin != public {
			auth.Origins = append(auth.Origins, origin)
		}
	}
	return auth, nil
}

// LoadPush reads the Web Push configuration.
func LoadPush() Push {
	return Push{
		PublicKey:  strings.TrimSpace(viper.GetString(KeyPushPublicKey)),
		PrivateKey: strings.TrimSpace(viper.GetString(KeyPushPrivateKey)),
		Subject:    strings.TrimSpace(viper.GetString(KeyPushSubject)),
	}
}
