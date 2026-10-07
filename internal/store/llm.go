package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/CoderSyndicate/doleances/internal/models"
	"gorm.io/gorm"
)

// LLMSettings returns the configured settings, or the defaults when nothing
// has been saved yet.
func (s *Store) LLMSettings(ctx context.Context) (models.LLMSettings, error) {
	var settings models.LLMSettings
	err := s.db.WithContext(ctx).First(&settings, "id = ?", models.LLMSettingsID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.DefaultLLMSettings(), nil
	}
	if err == nil {
		// A row written before a key was configured is read as itself, so an
		// installation can adopt one without re-entering a credential it may
		// no longer have. A row written with a *different* key is an error
		// rather than a string nobody can use: see secret.Box.Open.
		settings.APIKey, err = s.secrets.Open(settings.APIKey)
		if err != nil {
			return models.LLMSettings{}, fmt.Errorf("read the llm api key: %w", err)
		}
	}
	if err != nil {
		return models.LLMSettings{}, fmt.Errorf("read llm settings: %w", err)
	}
	return settings, nil
}

// SaveLLMSettings writes the settings row.
//
// An empty APIKey means "leave the stored key alone": the console never
// receives the key, so it cannot send it back, and a save that cleared it
// every time would make every other field uneditable.
func (s *Store) SaveLLMSettings(ctx context.Context, settings models.LLMSettings) error {
	settings.ID = models.LLMSettingsID

	existing, err := s.LLMSettings(ctx)
	if err != nil {
		return err
	}
	if settings.APIKey == "" {
		settings.APIKey = existing.APIKey
	}

	// Sealed on the way in, which is also the migration: a key stored before
	// this deployment had one is read as plaintext above and written back
	// encrypted here, the next time anybody saves anything.
	sealed, err := s.secrets.Seal(settings.APIKey)
	if err != nil {
		return fmt.Errorf("seal the llm api key: %w", err)
	}
	settings.APIKey = sealed

	if err := s.db.WithContext(ctx).Save(&settings).Error; err != nil {
		return fmt.Errorf("save llm settings: %w", err)
	}
	return nil
}

// ClearLLMAPIKey removes the stored credential.
func (s *Store) ClearLLMAPIKey(ctx context.Context) error {
	err := s.db.WithContext(ctx).Model(&models.LLMSettings{}).
		Where("id = ?", models.LLMSettingsID).
		Update("api_key", "").Error
	if err != nil {
		return fmt.Errorf("clear llm api key: %w", err)
	}
	return nil
}

// LLMSettingsExist reports whether settings have been stored.
//
// It is what distinguishes a first start — where the deployment's flags and
// environment seed the database — from every later one, where the database is
// authoritative and configuration must not silently overwrite what an operator
// set in the console.
func (s *Store) LLMSettingsExist(ctx context.Context) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&models.LLMSettings{}).
		Where("id = ?", models.LLMSettingsID).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("count llm settings: %w", err)
	}
	return count > 0, nil
}
