package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/CoderSyndicate/doleances/internal/models"
	"gorm.io/gorm"
)

// ErrAssetNotFound is returned when a theme has no image in that slot.
var ErrAssetNotFound = errors.New("theme asset not found")

// ThemeAsset returns one image of one theme.
func (s *Store) ThemeAsset(ctx context.Context, themeName, slot string) (models.ThemeAsset, error) {
	var asset models.ThemeAsset
	err := s.db.WithContext(ctx).
		First(&asset, "theme_name = ? AND slot = ?", themeName, slot).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.ThemeAsset{}, ErrAssetNotFound
	}
	if err != nil {
		return models.ThemeAsset{}, fmt.Errorf("read asset %s/%s: %w", themeName, slot, err)
	}
	return asset, nil
}

// ListThemeAssets returns a theme's images without their contents, so a
// listing does not load every blob.
func (s *Store) ListThemeAssets(ctx context.Context, themeName string) ([]models.ThemeAsset, error) {
	var assets []models.ThemeAsset
	err := s.db.WithContext(ctx).
		Select("id", "created_at", "updated_at", "theme_name", "slot", "content_type", "size").
		Where("theme_name = ?", themeName).
		Order("slot asc").
		Find(&assets).Error
	if err != nil {
		return nil, fmt.Errorf("list assets of %s: %w", themeName, err)
	}
	return assets, nil
}

// SaveThemeAsset stores an image, replacing whatever occupied that slot.
func (s *Store) SaveThemeAsset(ctx context.Context, asset models.ThemeAsset) error {
	existing, err := s.ThemeAsset(ctx, asset.ThemeName, asset.Slot)
	switch {
	case errors.Is(err, ErrAssetNotFound):
		if err := s.db.WithContext(ctx).Create(&asset).Error; err != nil {
			return fmt.Errorf("create asset %s/%s: %w", asset.ThemeName, asset.Slot, err)
		}
		return nil
	case err != nil:
		return err
	}

	existing.ContentType = asset.ContentType
	existing.Data = asset.Data
	existing.Size = asset.Size
	if err := s.db.WithContext(ctx).Save(&existing).Error; err != nil {
		return fmt.Errorf("update asset %s/%s: %w", asset.ThemeName, asset.Slot, err)
	}
	return nil
}

// DeleteThemeAsset removes one image, so the slot falls back to the built-in
// default again.
func (s *Store) DeleteThemeAsset(ctx context.Context, themeName, slot string) error {
	result := s.db.WithContext(ctx).
		Where("theme_name = ? AND slot = ?", themeName, slot).
		Delete(&models.ThemeAsset{})
	if result.Error != nil {
		return fmt.Errorf("delete asset %s/%s: %w", themeName, slot, result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrAssetNotFound
	}
	return nil
}

// DeleteThemeAssets removes every image of a theme, for when the theme itself
// is deleted — orphaned blobs would otherwise accumulate invisibly.
func (s *Store) DeleteThemeAssets(ctx context.Context, themeName string) error {
	err := s.db.WithContext(ctx).
		Where("theme_name = ?", themeName).
		Delete(&models.ThemeAsset{}).Error
	if err != nil {
		return fmt.Errorf("delete assets of %s: %w", themeName, err)
	}
	return nil
}

// ReplacePackageAssets swaps a theme's package files for a new set, leaving
// its named slots alone.
//
// Replacing rather than merging is deliberate: a package is one artefact, and
// a file the designer removed should disappear rather than linger invisibly
// behind a url() nobody writes any more.
func (s *Store) ReplacePackageAssets(ctx context.Context, themeName string, assets []models.ThemeAsset, slots map[string]bool) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing []models.ThemeAsset
		err := tx.Select("id", "slot").
			Where("theme_name = ?", themeName).
			Find(&existing).Error
		if err != nil {
			return fmt.Errorf("read existing assets: %w", err)
		}

		for _, asset := range existing {
			if slots[asset.Slot] {
				continue // a named slot is not part of the package
			}
			if err := tx.Delete(&models.ThemeAsset{}, "id = ?", asset.ID).Error; err != nil {
				return fmt.Errorf("remove stale asset %q: %w", asset.Slot, err)
			}
		}

		for _, asset := range assets {
			asset.ThemeName = themeName
			if err := tx.Create(&asset).Error; err != nil {
				return fmt.Errorf("store asset %q: %w", asset.Slot, err)
			}
		}
		return nil
	})
}
