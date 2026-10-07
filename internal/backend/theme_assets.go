package backend

import (
	"context"
	"errors"
	"net/http"

	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/store"
	"github.com/CoderSyndicate/doleances/internal/theme"
	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"
)

func (a *API) registerThemeAssetRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-theme-assets",
		Method:      http.MethodGet,
		Path:        "/v1/theme-assets/{name}",
		Summary:     "List a theme's images",
		Tags:        []string{"Themes"},
	}, a.listThemeAssets)

	huma.Register(api, huma.Operation{
		OperationID: "save-theme-asset",
		Method:      http.MethodPut,
		Path:        "/v1/theme-assets/{name}/{slot}",
		Summary:     "Replace one of a theme's images",
		Tags:        []string{"Themes"},
	}, a.saveThemeAsset)

	huma.Register(api, huma.Operation{
		OperationID: "delete-theme-asset",
		Method:      http.MethodDelete,
		Path:        "/v1/theme-assets/{name}/{slot}",
		Summary:     "Remove an image, falling back to the built-in default",
		Tags:        []string{"Themes"},
	}, a.deleteThemeAsset)

	huma.Register(api, huma.Operation{
		OperationID: "get-theme-file",
		Method:      http.MethodGet,
		Path:        "/v1/theme-files/{name}/{path...}",
		Summary:     "Serve one of a theme's package files",
		Description: "The files a theme's own tokens reference with url(). Served from this origin so a " +
			"theme can never point a visitor's browser at a third party.",
		Tags: []string{"Themes"},
	}, a.themeFile)

	huma.Register(api, huma.Operation{
		OperationID: "get-active-theme-asset",
		Method:      http.MethodGet,
		Path:        "/v1/theme-asset/{slot}",
		Summary:     "Serve an image of the active theme",
		Description: "Resolves the active theme's image, falling back to the built-in default. " +
			"This is what the web services proxy on /theme/assets/{slot}.",
		Tags: []string{"Themes"},
	}, a.activeThemeAsset)
}

// ThemeAssetSummary describes one image without its contents.
type ThemeAssetSummary struct {
	Slot string `json:"slot"`
	// Path mirrors Slot, so a package file can be listed by the path its
	// url() references.
	Path        string `json:"path"`
	Description string `json:"description"`
	ContentType string `json:"content_type,omitempty"`
	Size        int    `json:"size,omitempty"`

	// Custom is whether this theme provides the image, as opposed to
	// inheriting the built-in default.
	Custom bool `json:"custom"`

	// HasDefault is whether a built-in exists to fall back to.
	HasDefault bool `json:"has_default"`
}

// ThemeAssetsOutput lists every slot, filled or not.
type ThemeAssetsOutput struct {
	Body struct {
		Assets []ThemeAssetSummary `json:"assets"`
	}
}

func (a *API) listThemeAssets(ctx context.Context, in *ThemeNameInput) (*ThemeAssetsOutput, error) {
	var stored []models.ThemeAsset
	slotted := map[string]models.ThemeAsset{}
	if in.Name != builtInName {
		assets, err := a.store.ListThemeAssets(ctx, in.Name)
		if err != nil {
			log.Error().Err(err).Str("theme", in.Name).Msg("cannot list theme assets")
			return nil, huma.Error500InternalServerError("cannot list the theme's images")
		}
		stored = assets
		for _, asset := range assets {
			if theme.IsAssetSlot(asset.Slot) {
				slotted[asset.Slot] = asset
			}
		}
	}

	// Every slot is listed, filled or not: the console shows what a theme
	// could replace, not only what it has. Package files follow, so an
	// operator can see what a theme's own CSS is drawing on.
	out := &ThemeAssetsOutput{}
	for _, asset := range stored {
		if theme.IsAssetSlot(asset.Slot) {
			continue
		}
		out.Body.Assets = append(out.Body.Assets, ThemeAssetSummary{
			Slot:        asset.Slot,
			Path:        asset.Slot,
			ContentType: asset.ContentType,
			Size:        asset.Size,
			Custom:      true,
		})
	}
	for _, slot := range theme.AssetSlots {
		_, _, hasDefault := theme.DefaultAsset(slot.Name)
		summary := ThemeAssetSummary{
			Slot:        slot.Name,
			Description: slot.Description,
			HasDefault:  hasDefault,
		}
		if asset, ok := slotted[slot.Name]; ok {
			summary.Custom = true
			summary.ContentType = asset.ContentType
			summary.Size = asset.Size
		}
		out.Body.Assets = append(out.Body.Assets, summary)
	}
	return out, nil
}

// SaveThemeAssetInput is an uploaded image.
type SaveThemeAssetInput struct {
	Name        string `path:"name"`
	Slot        string `path:"slot"`
	ContentType string `header:"Content-Type"`
	RawBody     []byte
}

func (a *API) saveThemeAsset(ctx context.Context, in *SaveThemeAssetInput) (*EmptyOutput, error) {
	if in.Name == builtInName {
		return nil, huma.Error422UnprocessableEntity(
			"the built-in theme's images are compiled in; upload to a theme of your own")
	}
	if _, err := a.store.GetTheme(ctx, in.Name); errors.Is(err, store.ErrThemeNotFound) {
		return nil, huma.Error404NotFound("no such theme")
	}
	if err := theme.ValidateAsset(in.Slot, in.ContentType, len(in.RawBody)); err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}

	err := a.store.SaveThemeAsset(ctx, models.ThemeAsset{
		ThemeName:   in.Name,
		Slot:        in.Slot,
		ContentType: in.ContentType,
		Data:        in.RawBody,
		Size:        len(in.RawBody),
	})
	if err != nil {
		log.Error().Err(err).Str("theme", in.Name).Str("slot", in.Slot).Msg("cannot save theme asset")
		return nil, huma.Error500InternalServerError("cannot store the image")
	}

	log.Info().Str("theme", in.Name).Str("slot", in.Slot).Int("bytes", len(in.RawBody)).Msg("theme asset stored")
	return &EmptyOutput{}, nil
}

// ThemeAssetSlotInput addresses one image of one theme.
type ThemeAssetSlotInput struct {
	Name string `path:"name"`
	Slot string `path:"slot"`
}

func (a *API) deleteThemeAsset(ctx context.Context, in *ThemeAssetSlotInput) (*EmptyOutput, error) {
	err := a.store.DeleteThemeAsset(ctx, in.Name, in.Slot)
	if errors.Is(err, store.ErrAssetNotFound) {
		return nil, huma.Error404NotFound("this theme has no image in that slot")
	}
	if err != nil {
		log.Error().Err(err).Str("theme", in.Name).Str("slot", in.Slot).Msg("cannot delete theme asset")
		return nil, huma.Error500InternalServerError("cannot remove the image")
	}
	log.Info().Str("theme", in.Name).Str("slot", in.Slot).Msg("theme asset removed")
	return &EmptyOutput{}, nil
}

// AssetSlotInput addresses a slot of whichever theme is active.
type AssetSlotInput struct {
	Slot string `path:"slot"`
}

// AssetOutput carries an image.
type AssetOutput struct {
	ContentType string `header:"Content-Type"`
	// CacheControl is short: an operator replacing a logo expects to see it.
	CacheControl string `header:"Cache-Control"`
	// ContentSecurityPolicy neutralises an uploaded SVG. An SVG may contain
	// script, and it is served from the same origin as the site, so the policy
	// is what keeps an image upload from becoming code execution.
	ContentSecurityPolicy string `header:"Content-Security-Policy"`
	ContentTypeOptions    string `header:"X-Content-Type-Options"`
	Body                  []byte
}

// activeThemeAsset resolves an image: the active theme's, then the built-in
// default, then nothing.
func (a *API) activeThemeAsset(ctx context.Context, in *AssetSlotInput) (*AssetOutput, error) {
	if !theme.IsAssetSlot(in.Slot) {
		return nil, huma.Error404NotFound("no such image")
	}

	out := &AssetOutput{
		CacheControl:          "public, max-age=60",
		ContentSecurityPolicy: "default-src 'none'; style-src 'unsafe-inline'; sandbox",
		ContentTypeOptions:    "nosniff",
	}

	if active, err := a.store.ActiveTheme(ctx); err == nil {
		asset, err := a.store.ThemeAsset(ctx, active.Name, in.Slot)
		switch {
		case err == nil:
			out.ContentType = asset.ContentType
			out.Body = asset.Data
			return out, nil
		case !errors.Is(err, store.ErrAssetNotFound):
			// Fall through to the default rather than failing the page: a
			// missing logo must never take a site down.
			log.Warn().Err(err).Str("slot", in.Slot).Msg("cannot read theme asset, using the default")
		}
	} else if !errors.Is(err, store.ErrThemeNotFound) {
		log.Warn().Err(err).Msg("cannot read the active theme, using default images")
	}

	data, contentType, ok := theme.DefaultAsset(in.Slot)
	if !ok {
		return nil, huma.Error404NotFound("no image in that slot, and no default")
	}
	out.ContentType = contentType
	out.Body = data
	return out, nil
}

// ThemeFileInput addresses one file inside one theme's package.
type ThemeFileInput struct {
	Name string `path:"name"`
	Path string `path:"path"`
}

func (a *API) themeFile(ctx context.Context, in *ThemeFileInput) (*AssetOutput, error) {
	asset, err := a.store.ThemeAsset(ctx, in.Name, in.Path)
	if errors.Is(err, store.ErrAssetNotFound) {
		return nil, huma.Error404NotFound("no such file in that theme")
	}
	if err != nil {
		log.Error().Err(err).Str("theme", in.Name).Str("file", in.Path).Msg("cannot read theme file")
		return nil, huma.Error500InternalServerError("cannot read the file")
	}

	return &AssetOutput{
		ContentType: asset.ContentType,
		// A package file belongs to a named version of a theme, so replacing
		// it means uploading a new package — it can be cached hard.
		CacheControl:          "public, max-age=3600",
		ContentSecurityPolicy: "default-src 'none'; style-src 'unsafe-inline'; sandbox",
		ContentTypeOptions:    "nosniff",
		Body:                  asset.Data,
	}, nil
}
