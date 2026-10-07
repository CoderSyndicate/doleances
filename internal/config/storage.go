package config

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Storage configuration keys.
//
// The role is "storage", never the technology behind it: S3 is one possible
// backend, and naming the code after it would weld a deployment decision into
// the source.
const (
	KeyStorageURL          = "storage-url"
	KeySnapshotRetention   = "snapshot-retention"
	KeySnapshotFullEnabled = "snapshot-full-enabled"
)

// Storage configures where generated artefacts are kept.
type Storage struct {
	// URL is a gocloud.dev/blob URL: file:///var/lib/doleances/storage or
	// s3://bucket?region=… (any S3-compatible provider). The scheme selects
	// the driver, so the backing service is configuration rather than code.
	URL string

	// SnapshotRetention is how many snapshots of each kind to keep. Older
	// ones are pruned when a new one is created. Zero keeps everything.
	SnapshotRetention int

	// SnapshotFullEnabled gates the snapshot containing personal data. It is
	// off by default: an operator turns it on deliberately, so that reaching
	// the console is not by itself enough to dump the participant database.
	SnapshotFullEnabled bool
}

// RegisterStorageFlags declares the storage flags. Only the backend owns
// storage, so only the backend registers these.
func RegisterStorageFlags(cmd *cobra.Command) {
	f := cmd.PersistentFlags()
	f.String(KeyStorageURL, "file://./storage", "storage location as a URL (file:// or s3://)")
	f.Int(KeySnapshotRetention, 10, "snapshots to keep per kind; 0 keeps every one")
	f.Bool(KeySnapshotFullEnabled, false, "allow the full snapshot, which contains personal data")
}

// LoadStorage reads the resolved storage configuration.
func LoadStorage() (Storage, error) {
	s := Storage{
		URL:                 viper.GetString(KeyStorageURL),
		SnapshotRetention:   viper.GetInt(KeySnapshotRetention),
		SnapshotFullEnabled: viper.GetBool(KeySnapshotFullEnabled),
	}
	return s, s.Validate()
}

// Validate rejects a configuration that would fail later, at the moment
// somebody tries to download a snapshot.
func (s Storage) Validate() error {
	if strings.TrimSpace(s.URL) == "" {
		return fmt.Errorf("%s is required", KeyStorageURL)
	}
	if !strings.Contains(s.URL, "://") {
		return fmt.Errorf("invalid %s %q: want a URL such as file://./storage", KeyStorageURL, s.URL)
	}
	if s.SnapshotRetention < 0 {
		return fmt.Errorf("invalid %s %d: must not be negative", KeySnapshotRetention, s.SnapshotRetention)
	}
	return nil
}
