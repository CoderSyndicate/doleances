package config

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Database configuration keys.
const (
	KeyDatabaseDriver   = "database-driver"
	KeyDatabaseDSN      = "database-dsn"
	KeyDatabaseMaxConns = "database-max-conns"
)

// Database configures the connection to the store.
type Database struct {
	// Driver is "postgres" in production, "sqlite" in development.
	Driver string

	// DSN is a PostgreSQL URL or a SQLite file path.
	DSN string

	MaxConns int

	// SettingsKey encrypts the credentials stored in settings rows. Empty is
	// an installation that has not configured one.
	SettingsKey string
}

// RegisterDatabaseFlags declares the database flags. Only services that own
// data register them — the console and the frontend reach the database through
// the backend's API, never directly.
func RegisterDatabaseFlags(cmd *cobra.Command) {
	f := cmd.PersistentFlags()
	f.String(KeyDatabaseDriver, "sqlite", "database engine (postgres, sqlite)")
	f.String(KeyDatabaseDSN, "doleances.db", "postgres connection URL, or sqlite file path")
	f.Int(KeyDatabaseMaxConns, 25, "maximum open database connections")
}

// LoadDatabase reads the resolved database configuration.
func LoadDatabase() (Database, error) {
	d := Database{
		Driver:      viper.GetString(KeyDatabaseDriver),
		DSN:         viper.GetString(KeyDatabaseDSN),
		MaxConns:    viper.GetInt(KeyDatabaseMaxConns),
		SettingsKey: viper.GetString(KeySettingsKey),
	}
	return d, d.Validate()
}

// KeySettingsKey encrypts the credentials the database holds.
const KeySettingsKey = "settings-key"

// RegisterSettingsKeyFlag declares the key the stored credentials are
// encrypted with.
//
// Separate from the database flags although it travels with them, because it
// is not a connection setting: it is the difference between a backup that
// carries this register's credentials and one that carries nothing anybody can
// use. Generate it, never invent it — a passphrase is refused rather than
// hashed into something key-shaped, for the reason internal/secret gives.
//
// Its absence stores credentials in clear and warns at startup, the way absent
// VAPID keys switch push off: refusing to start would take a working register
// down over a credential it had been storing in clear the day before.
func RegisterSettingsKeyFlag(cmd *cobra.Command) {
	cmd.PersistentFlags().String(KeySettingsKey, "",
		"base64 32-byte key encrypting stored credentials (absent: stored in clear)")
}

// Validate rejects a database configuration that cannot work.
func (d Database) Validate() error {
	switch d.Driver {
	case "postgres", "sqlite":
	default:
		return fmt.Errorf("invalid %s %q: want postgres or sqlite", KeyDatabaseDriver, d.Driver)
	}
	if d.DSN == "" {
		return fmt.Errorf("%s is required", KeyDatabaseDSN)
	}
	if d.MaxConns < 1 {
		return fmt.Errorf("invalid %s %d: must be at least 1", KeyDatabaseMaxConns, d.MaxConns)
	}
	return nil
}
