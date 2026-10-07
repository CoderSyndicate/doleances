package config

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// KeyBackendURL points a service at the backend's API.
const KeyBackendURL = "backend-url"

// Client configures a service that depends on the backend.
type Client struct {
	BackendURL string
}

// RegisterClientFlags declares the flags a service needs to reach the backend.
// The console and the frontend register these; the backend does not.
func RegisterClientFlags(defaultURL string) func(*cobra.Command) {
	return func(cmd *cobra.Command) {
		cmd.PersistentFlags().String(KeyBackendURL, defaultURL, "base url of the backend API")
	}
}

// LoadClient reads the resolved client configuration.
func LoadClient() (Client, error) {
	c := Client{BackendURL: viper.GetString(KeyBackendURL)}
	return c, c.Validate()
}

// Validate rejects a client configuration that cannot work.
func (c Client) Validate() error {
	if c.BackendURL == "" {
		return fmt.Errorf("%s is required", KeyBackendURL)
	}
	parsed, err := url.Parse(c.BackendURL)
	if err != nil {
		return fmt.Errorf("invalid %s %q: %w", KeyBackendURL, c.BackendURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid %s %q: want scheme://host", KeyBackendURL, c.BackendURL)
	}
	return nil
}
