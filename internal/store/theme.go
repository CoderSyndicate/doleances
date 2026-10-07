package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/CoderSyndicate/doleances/internal/models"
	"gorm.io/gorm"
)

// ErrThemeNotFound is returned when a named theme is not in the library.
var ErrThemeNotFound = errors.New("theme not found")

// ListThemes returns the library with each theme's colourways, newest first.
func (s *Store) ListThemes(ctx context.Context) ([]models.ThemeFile, error) {
	var themes []models.ThemeFile
	err := s.db.WithContext(ctx).
		Preload("Colors", func(db *gorm.DB) *gorm.DB { return db.Order("name asc") }).
		Order("created_at desc").
		Find(&themes).Error
	if err != nil {
		return nil, fmt.Errorf("list themes: %w", err)
	}
	return themes, nil
}

// GetTheme returns one theme with its colourways.
func (s *Store) GetTheme(ctx context.Context, name string) (models.ThemeFile, error) {
	var theme models.ThemeFile
	err := s.db.WithContext(ctx).
		Preload("Colors", func(db *gorm.DB) *gorm.DB { return db.Order("name asc") }).
		First(&theme, "name = ?", name).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.ThemeFile{}, ErrThemeNotFound
	}
	if err != nil {
		return models.ThemeFile{}, fmt.Errorf("get theme %q: %w", name, err)
	}
	return theme, nil
}

// ActiveTheme returns the theme in force, or ErrThemeNotFound when the
// built-in default is in use.
func (s *Store) ActiveTheme(ctx context.Context) (models.ThemeFile, error) {
	var theme models.ThemeFile
	err := s.db.WithContext(ctx).
		Preload("Colors").
		First(&theme, "active = ?", true).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.ThemeFile{}, ErrThemeNotFound
	}
	if err != nil {
		return models.ThemeFile{}, fmt.Errorf("get active theme: %w", err)
	}
	return theme, nil
}

// SaveTheme stores an uploaded package, replacing any theme of the same name.
//
// Colourways are replaced rather than merged: a package is one artefact, and a
// colour the designer removed should disappear rather than linger in a
// dropdown nobody maintains.
func (s *Store) SaveTheme(ctx context.Context, name, structure string, colors map[string]string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var theme models.ThemeFile
		err := tx.First(&theme, "name = ?", name).Error

		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			theme = models.ThemeFile{Name: name, Structure: structure}
			if err := tx.Create(&theme).Error; err != nil {
				return fmt.Errorf("create theme %q: %w", name, err)
			}
		case err != nil:
			return fmt.Errorf("read theme %q: %w", name, err)
		default:
			theme.Structure = structure
			if err := tx.Save(&theme).Error; err != nil {
				return fmt.Errorf("update theme %q: %w", name, err)
			}
		}

		if err := tx.Where("theme_name = ?", name).Delete(&models.ThemeColor{}).Error; err != nil {
			return fmt.Errorf("clear colourways of %q: %w", name, err)
		}
		for colour, document := range colors {
			row := models.ThemeColor{ThemeName: name, Name: colour, Document: document}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("store colourway %q: %w", colour, err)
			}
		}

		// A colour that no longer exists must not stay selected.
		if _, ok := colors[theme.ActiveColor]; !ok {
			return tx.Model(&models.ThemeFile{}).
				Where("name = ?", name).
				Update("active_color", "").Error
		}
		return nil
	})
}

// SetActiveTheme puts one theme and one of its colourways in force, or clears
// the selection when name is empty so the built-in default applies again.
func (s *Store) SetActiveTheme(ctx context.Context, name, colour string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.ThemeFile{}).
			Where("active = ?", true).
			Update("active", false).Error; err != nil {
			return fmt.Errorf("clear active theme: %w", err)
		}
		if name == "" {
			return nil
		}

		var theme models.ThemeFile
		err := tx.Preload("Colors").First(&theme, "name = ?", name).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrThemeNotFound
		}
		if err != nil {
			return fmt.Errorf("read theme %q: %w", name, err)
		}

		// An unnamed or unknown colour falls back to the theme's first, so a
		// selection can never leave a theme active with no colour to render.
		if colour == "" || !hasColor(theme.Colors, colour) {
			if len(theme.Colors) == 0 {
				return fmt.Errorf("theme %q has no colourway", name)
			}
			colour = theme.Colors[0].Name
		}

		return tx.Model(&models.ThemeFile{}).
			Where("name = ?", name).
			Updates(map[string]any{"active": true, "active_color": colour}).Error
	})
}

func hasColor(colors []models.ThemeColor, name string) bool {
	for _, colour := range colors {
		if colour.Name == name {
			return true
		}
	}
	return false
}

// DeleteTheme removes a theme, its colourways and its files.
func (s *Store) DeleteTheme(ctx context.Context, name string) error {
	// Assets first: an orphaned blob is invisible and never reclaimed.
	if err := s.DeleteThemeAssets(ctx, name); err != nil {
		return err
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("theme_name = ?", name).Delete(&models.ThemeColor{}).Error; err != nil {
			return fmt.Errorf("delete colourways of %q: %w", name, err)
		}

		result := tx.Where("name = ?", name).Delete(&models.ThemeFile{})
		if result.Error != nil {
			return fmt.Errorf("delete theme %q: %w", name, result.Error)
		}
		if result.RowsAffected == 0 {
			return ErrThemeNotFound
		}
		return nil
	})
}

// ThemeExists reports whether a theme is already in the library, which is what
// keeps seeding from overwriting an operator's own upload.
func (s *Store) ThemeExists(ctx context.Context, name string) (bool, error) {
	var count int64
	if err := s.db.WithContext(ctx).Model(&models.ThemeFile{}).
		Where("name = ?", name).Count(&count).Error; err != nil {
		return false, fmt.Errorf("count theme %q: %w", name, err)
	}
	return count > 0, nil
}
