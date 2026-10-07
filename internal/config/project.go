package config

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// KeyProjectURL points visitors at the source of the software they are using.
const KeyProjectURL = "project-url"

// DefaultProjectURL is where this software is developed. It is a default
// rather than a constant so that a fork rehosting the register points at its
// own source instead of advertising somebody else's.
const DefaultProjectURL = "https://github.com/CoderSyndicate/doleances"

// Project describes the software behind a deployment.
//
// This is a "static-dynamic" value in the sense of the architecture rules: it
// comes from configuration and is fixed for the lifetime of a deployment, so
// it is rendered into the page rather than fetched by it.
type Project struct {
	URL string
}

// RegisterProjectFlags declares the flags for the public-facing services.
func RegisterProjectFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().String(KeyProjectURL, DefaultProjectURL,
		"where the source of this software can be found; empty hides the link")
}

// LoadProject reads the resolved project configuration.
func LoadProject() (Project, error) {
	p := Project{URL: strings.TrimSpace(viper.GetString(KeyProjectURL))}
	return p, p.Validate()
}

// Validate rejects a URL that would render as a broken link. An empty value is
// allowed and simply hides the link: an operator who does not publish their
// source should not be forced to claim they do.
func (p Project) Validate() error {
	if p.URL == "" {
		return nil
	}
	parsed, err := url.Parse(p.URL)
	if err != nil {
		return fmt.Errorf("invalid %s %q: %w", KeyProjectURL, p.URL, err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("invalid %s %q: want an http or https url", KeyProjectURL, p.URL)
	}
	if parsed.Host == "" {
		return fmt.Errorf("invalid %s %q: want scheme://host", KeyProjectURL, p.URL)
	}
	return nil
}
