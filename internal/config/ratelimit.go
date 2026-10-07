package config

import (
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/CoderSyndicate/doleances/internal/web"
)

// The rate-limit settings.
const (
	KeyRateAssets   = "rate-limit-assets"
	KeyRatePages    = "rate-limit-pages"
	KeyRateWrites   = "rate-limit-writes"
	KeyTrustedProxy = "trusted-proxy"
)

// RegisterRateLimitFlags declares what one address may do in a minute.
//
// Configuration rather than constants, for the reason the classifier
// thresholds are: these numbers are placed against a particular audience, and
// a register whose readers sit behind one carrier NAT wants different ones from
// a register that does not. See web.DefaultBounds for what the defaults assume
// and, more importantly, for what a per-address limit can and cannot do.
func RegisterRateLimitFlags(cmd *cobra.Command) {
	defaults := web.DefaultBounds

	cmd.PersistentFlags().Int(KeyRateAssets, defaults.Assets,
		"embedded files and the theme overlay one address may fetch per minute (0 switches it off)")
	cmd.PersistentFlags().Int(KeyRatePages, defaults.Pages,
		"pages one address may read per minute (0 switches it off)")
	cmd.PersistentFlags().Int(KeyRateWrites, defaults.Writes,
		"form posts and API writes one address may make per minute (0 switches it off)")
	cmd.PersistentFlags().StringSlice(KeyTrustedProxy, nil,
		"CIDR ranges whose X-Forwarded-For may say who a request is from "+
			"(default: loopback and the private ranges)")
}

// LoadBounds reads the three allowances.
//
// A value that was never written reads as zero, and zero here means "switched
// off" rather than "never written" — so a key that is entirely absent falls
// back to the default while an explicit zero is obeyed. The distinction
// matters: this project has already shipped a threshold that was silently zero
// because a setting was added and forgotten in the configuration path.
func LoadBounds() web.Bounds {
	return web.Bounds{
		Assets: allowance(KeyRateAssets, web.DefaultBounds.Assets),
		Pages:  allowance(KeyRatePages, web.DefaultBounds.Pages),
		Writes: allowance(KeyRateWrites, web.DefaultBounds.Writes),
	}
}

func allowance(key string, fallback int) int {
	if !viper.IsSet(key) {
		return fallback
	}
	return viper.GetInt(key)
}

// LoadTrustedProxies reads which hops may speak for a reader.
//
// An unreadable entry is refused rather than skipped: an operator who wrote a
// range wrong is one whose limiter would silently put every reader in one
// bucket, and finding that out from a graph is much worse than finding it out
// from a startup error.
func LoadTrustedProxies() (web.TrustedProxies, error) {
	configured := viper.GetStringSlice(KeyTrustedProxy)
	if len(configured) == 0 {
		return web.DefaultTrustedProxies(), nil
	}

	parsed, err := web.ParseTrustedProxies(configured)
	if err != nil {
		return nil, err
	}
	log.Info().Strs("ranges", configured).
		Msg("X-Forwarded-For will be believed from these hops only")
	return parsed, nil
}
