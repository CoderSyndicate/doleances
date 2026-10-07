package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// ErrAlreadyProvisioned means the identity provider is already configured.
//
// It exists so the one write that can repoint this console at a different
// directory cannot happen by accident, or by anybody who merely reached the
// endpoint. See SaveAuthSettings.
var ErrAlreadyProvisioned = errors.New("the identity provider is already configured")

// AuthSettings reads the identity provider configuration.
//
// Both secrets are decrypted on the way out. A row written with a different
// settings key is an error rather than a string nobody can use — the console
// would otherwise authenticate with a base64 blob and fail somewhere far away,
// with a message about the wrong thing.
func (s *Store) AuthSettings(ctx context.Context) (models.AuthSettings, error) {
	var settings models.AuthSettings
	err := s.db.WithContext(ctx).First(&settings, "id = ?", models.AuthSettingsID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.DefaultAuthSettings(), nil
	}
	if err != nil {
		return models.AuthSettings{}, fmt.Errorf("read auth settings: %w", err)
	}

	if settings.ClientSecret, err = s.secrets.Open(settings.ClientSecret); err != nil {
		return models.AuthSettings{}, fmt.Errorf("read the oauth client secret: %w", err)
	}
	if settings.APIToken, err = s.secrets.Open(settings.APIToken); err != nil {
		return models.AuthSettings{}, fmt.Errorf("read the authentik api token: %w", err)
	}
	return settings, nil
}

// SaveAuthSettings records what the wizard provisioned.
//
// # It refuses to overwrite a provisioned installation
//
// This is the one write in the project that can point the console's
// authentication at a different directory, which is to say hand administration
// of this register to whoever asked. The wizard that calls it runs before
// anybody can be identified, so there is no caller to check — the guard has to
// be the state of the row.
//
// So a second write is refused unless `reprovision` is explicitly true, which
// the console only sends when it was started with the flag that reopens the
// setup door. Two deliberate acts — a restart with a flag, and a person on the
// maintenance port — stand between a running register and being repointed.
func (s *Store) SaveAuthSettings(ctx context.Context, settings models.AuthSettings, reprovision bool) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing models.AuthSettings
		err := tx.First(&existing, "id = ?", models.AuthSettingsID).Error
		switch {
		case err == nil:
			if existing.Provisioned && !reprovision {
				return ErrAlreadyProvisioned
			}
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return fmt.Errorf("read auth settings: %w", err)
		}

		settings.ID = models.AuthSettingsID
		if settings.ProvisionedAt == nil {
			now := time.Now()
			settings.ProvisionedAt = &now
		}

		// Sealed on the way in. The two most dangerous values this register
		// stores — one that lets somebody sign in as the console, one that
		// lets somebody make users in the operator's directory.
		if settings.ClientSecret, err = s.secrets.Seal(settings.ClientSecret); err != nil {
			return fmt.Errorf("seal the oauth client secret: %w", err)
		}
		if settings.APIToken, err = s.secrets.Seal(settings.APIToken); err != nil {
			return fmt.Errorf("seal the authentik api token: %w", err)
		}

		if err := tx.Save(&settings).Error; err != nil {
			return fmt.Errorf("save auth settings: %w", err)
		}
		return nil
	})
}

// AuthProvisioned reports whether the wizard has run.
//
// Separate from AuthSettings and deliberately cheap: it is asked on the path
// that decides whether to register the setup route at all, and it must not
// need the settings key to answer. An installation whose key is wrong should
// still know that it is configured — otherwise a mistyped key would reopen the
// setup door, which is the one thing that must never follow from a
// configuration mistake.
func (s *Store) AuthProvisioned(ctx context.Context) (bool, error) {
	var settings models.AuthSettings
	err := s.db.WithContext(ctx).Select("provisioned").
		First(&settings, "id = ?", models.AuthSettingsID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read auth settings: %w", err)
	}
	return settings.Provisioned, nil
}
