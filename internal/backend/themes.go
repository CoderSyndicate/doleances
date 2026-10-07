package backend

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/store"
	"github.com/CoderSyndicate/doleances/internal/theme"
	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"
)

// builtInName is how the compiled-in default appears in the library. It is not
// a stored row: it cannot be deleted, and it is what everything falls back to.
const builtInName = "built-in"

// builtInColor is the built-in theme's only colourway.
const builtInColor = "default"

// The backend owns the theme library because it owns every write. The console
// manages it through these endpoints and holds no database credentials.
//
// A theme is one design with several colourways: one structure, many colours.
// Selecting means choosing a theme and one of its colours.
func (a *API) registerThemeRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-themes",
		Method:      http.MethodGet,
		Path:        "/v1/themes",
		Summary:     "List the theme library",
		Description: "Each theme with its colourways. The built-in default comes first and the active one is marked.",
		Tags:        []string{"Themes"},
	}, a.listThemes)

	huma.Register(api, huma.Operation{
		OperationID: "set-active-theme",
		Method:      http.MethodPut,
		Path:        "/v1/themes/active",
		Summary:     "Select the active theme and colourway",
		Description: "An empty name restores the built-in default. An empty colour takes the theme's first.",
		Tags:        []string{"Themes"},
	}, a.setActiveTheme)

	huma.Register(api, huma.Operation{
		OperationID: "get-theme",
		Method:      http.MethodGet,
		Path:        "/v1/themes/{name}",
		Summary:     "Download a theme as a package",
		Description: "A zip holding structure.json, colors/ and assets/ — the same shape an upload takes.",
		Tags:        []string{"Themes"},
	}, a.getTheme)

	huma.Register(api, huma.Operation{
		OperationID: "save-theme",
		Method:      http.MethodPut,
		Path:        "/v1/themes/{name}",
		Summary:     "Add or replace a theme",
		Tags:        []string{"Themes"},
	}, a.saveTheme)

	huma.Register(api, huma.Operation{
		OperationID: "delete-theme",
		Method:      http.MethodDelete,
		Path:        "/v1/themes/{name}",
		Summary:     "Remove a theme from the library",
		Tags:        []string{"Themes"},
	}, a.deleteTheme)

	huma.Register(api, huma.Operation{
		OperationID: "render-theme-css",
		Method:      http.MethodGet,
		Path:        "/v1/theme.css",
		Summary:     "Render the active theme as CSS",
		Description: "The active theme's structure and chosen colourway, merged onto the built-in defaults.",
		Tags:        []string{"Themes"},
	}, a.renderActiveCSS)
}

// ThemeSummary describes one theme in the library.
type ThemeSummary struct {
	Name    string `json:"name"`
	Active  bool   `json:"active"`
	BuiltIn bool   `json:"built_in"`

	// Colors are the colourways the theme offers, and ActiveColor the one
	// chosen — which the console shows as a dropdown on the theme's row.
	Colors      []string `json:"colors"`
	ActiveColor string   `json:"active_color"`

	// HasStructure is whether the theme sets its own spacing and type.
	HasStructure bool `json:"has_structure"`

	// Files is how many package assets it carries.
	Files int `json:"files"`

	Swatches map[string]string       `json:"swatches,omitempty"`
	Sample   map[string]string       `json:"sample,omitempty"`
	Warnings []theme.ContrastWarning `json:"warnings,omitempty"`
}

// ThemesOutput is the library listing.
type ThemesOutput struct {
	Body struct {
		Themes []ThemeSummary `json:"themes"`
	}
}

func (a *API) listThemes(ctx context.Context, _ *struct{}) (*ThemesOutput, error) {
	stored, err := a.store.ListThemes(ctx)
	if err != nil {
		log.Error().Err(err).Msg("cannot list themes")
		return nil, huma.Error500InternalServerError("cannot list the themes")
	}

	anyActive := false
	summaries := make([]ThemeSummary, 0, len(stored)+1)
	for _, file := range stored {
		anyActive = anyActive || file.Active
		summaries = append(summaries, a.summarise(ctx, file))
	}

	out := &ThemesOutput{}
	out.Body.Themes = append([]ThemeSummary{builtInSummary(!anyActive)}, summaries...)
	return out, nil
}

// summarise describes a stored theme for the library listing. A theme that no
// longer parses is listed rather than hidden, so somebody can see it and
// delete it.
func (a *API) summarise(ctx context.Context, file models.ThemeFile) ThemeSummary {
	summary := ThemeSummary{
		Name:         file.Name,
		Active:       file.Active,
		ActiveColor:  file.ActiveColor,
		HasStructure: file.Structure != "",
	}
	for _, colour := range file.Colors {
		summary.Colors = append(summary.Colors, colour.Name)
	}
	if summary.ActiveColor == "" && len(summary.Colors) > 0 {
		summary.ActiveColor = summary.Colors[0]
	}

	if assets, err := a.store.ListThemeAssets(ctx, file.Name); err == nil {
		summary.Files = len(assets)
	}

	// Preview the colourway that is (or would be) shown.
	for _, colour := range file.Colors {
		if colour.Name != summary.ActiveColor {
			continue
		}
		tokens, err := theme.FromDTCG([]byte(colour.Document))
		if err != nil {
			log.Warn().Err(err).Str("theme", file.Name).Str("colour", colour.Name).
				Msg("stored colourway does not parse")
			break
		}
		merged := theme.Merge(theme.DefaultTokens(), tokens)
		summary.Swatches = theme.Swatches(merged)
		summary.Warnings = theme.CheckContrast(merged)
	}

	if file.Structure != "" {
		if dimensions, err := theme.StructureFromDTCG([]byte(file.Structure)); err == nil {
			summary.Sample = theme.StructureSample(theme.MergeStructure(theme.DefaultStructure(), dimensions))
		}
	}
	return summary
}

func builtInSummary(active bool) ThemeSummary {
	return ThemeSummary{
		Name:         builtInName,
		Active:       active,
		BuiltIn:      true,
		Colors:       []string{builtInColor},
		ActiveColor:  builtInColor,
		HasStructure: true,
		Swatches:     theme.Swatches(theme.DefaultTokens()),
		Sample:       theme.StructureSample(theme.DefaultStructure()),
		Warnings:     theme.CheckContrast(theme.DefaultTokens()),
	}
}

// ThemeNameInput addresses one theme.
type ThemeNameInput struct {
	Name string `path:"name" doc:"Theme name, or \"built-in\" for the compiled-in default"`
}

// ThemeDocumentOutput carries a theme package.
type ThemeDocumentOutput struct {
	ContentType string `header:"Content-Type"`
	Body        []byte
}

// getTheme hands back a package: the same shape an upload takes, so a download
// can be edited and uploaded again without repacking by hand.
func (a *API) getTheme(ctx context.Context, in *ThemeNameInput) (*ThemeDocumentOutput, error) {
	out := &ThemeDocumentOutput{ContentType: "application/zip"}

	// The built-in default is downloadable as the starting template, so a
	// designer begins from the real token set rather than a blank page.
	if in.Name == builtInName {
		archive, err := packThemeZip(
			in.Name,
			theme.StructureToDTCG(theme.DefaultStructure()),
			map[string][]byte{builtInColor: theme.ToDTCG(theme.DefaultTokens())},
			nil,
		)
		if err != nil {
			return nil, huma.Error500InternalServerError("cannot build the template")
		}
		out.Body = archive
		return out, nil
	}

	file, err := a.store.GetTheme(ctx, in.Name)
	if errors.Is(err, store.ErrThemeNotFound) {
		return nil, huma.Error404NotFound("no such theme")
	}
	if err != nil {
		log.Error().Err(err).Str("theme", in.Name).Msg("cannot read theme")
		return nil, huma.Error500InternalServerError("cannot read the theme")
	}

	colors := map[string][]byte{}
	for _, colour := range file.Colors {
		colors[colour.Name] = []byte(colour.Document)
	}

	assets, err := a.store.ListThemeAssets(ctx, in.Name)
	if err != nil {
		log.Error().Err(err).Str("theme", in.Name).Msg("cannot read theme assets")
		return nil, huma.Error500InternalServerError("cannot read the theme's files")
	}
	packed := make([]models.ThemeAsset, 0, len(assets))
	for _, listed := range assets {
		full, err := a.store.ThemeAsset(ctx, in.Name, listed.Slot)
		if err != nil {
			continue
		}
		packed = append(packed, full)
	}

	archive, err := packThemeZip(in.Name, []byte(file.Structure), colors, packed)
	if err != nil {
		log.Error().Err(err).Str("theme", in.Name).Msg("cannot pack theme")
		return nil, huma.Error500InternalServerError("cannot pack the theme")
	}
	out.Body = archive
	return out, nil
}

// packThemeZip builds a package in the layout ReadPackage expects.
func packThemeZip(name string, structure []byte, colors map[string][]byte, assets []models.ThemeAsset) ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	write := func(name string, content []byte) error {
		entry, err := w.Create(name)
		if err != nil {
			return err
		}
		_, err = entry.Write(content)
		return err
	}

	// What this package is and what may go in it, travelling with it — the
	// same reason a snapshot carries one. A designer who opens this zip in six
	// months should not have to find the console to learn what a colourway is.
	if err := write(theme.PackageReadmeFile, []byte(theme.PackageReadme(name))); err != nil {
		return nil, err
	}

	if len(structure) > 0 {
		if err := write(theme.PackageStructureFile, structure); err != nil {
			return nil, err
		}
	}
	for name, document := range colors {
		if err := write(theme.PackageColorDir+"/"+name+".json", document); err != nil {
			return nil, err
		}
	}
	for _, asset := range assets {
		if theme.IsAssetSlot(asset.Slot) {
			continue // a named slot is managed on its own, not part of the package
		}
		if err := write(theme.PackageAssetDir+"/"+asset.Slot, asset.Data); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// SaveThemeInput is an uploaded package.
type SaveThemeInput struct {
	Name     string `path:"name"`
	Activate bool   `query:"activate" doc:"Select the theme as soon as it is stored"`
	RawBody  []byte
}

// SaveThemeOutput reports what was imported.
type SaveThemeOutput struct {
	Body struct {
		Name     string                  `json:"name"`
		Colors   []string                `json:"colors"`
		Files    int                     `json:"files"`
		Active   bool                    `json:"active"`
		Warnings []theme.ContrastWarning `json:"warnings,omitempty"`
	}
}

func (a *API) saveTheme(ctx context.Context, in *SaveThemeInput) (*SaveThemeOutput, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || name == builtInName {
		return nil, huma.Error422UnprocessableEntity(
			"a theme needs a name of its own, and cannot be called " + builtInName)
	}

	pkg, err := theme.ReadPackage(in.RawBody)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}

	// Parse before storing: an unreadable document should be refused at upload
	// rather than discovered when somebody selects it.
	out := &SaveThemeOutput{}
	var values []string

	if len(pkg.Structure) > 0 {
		dimensions, err := theme.StructureFromDTCG(pkg.Structure)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity(theme.PackageStructureFile + ": " + err.Error())
		}
		for _, d := range dimensions {
			values = append(values, d.Value)
		}
	}

	colors := map[string]string{}
	for _, colour := range pkg.ColorNames() {
		tokens, err := theme.FromDTCG(pkg.Colors[colour])
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("colourway " + colour + ": " + err.Error())
		}
		colors[colour] = string(pkg.Colors[colour])
		out.Body.Colors = append(out.Body.Colors, colour)
		for _, t := range tokens {
			values = append(values, t.Light, t.Dark)
		}
		if out.Body.Warnings == nil {
			out.Body.Warnings = theme.CheckContrast(theme.Merge(theme.DefaultTokens(), tokens))
		}
	}

	// Every url() a token uses must point inside the package. That is what
	// makes url() safe to allow at all: a reference can never reach a
	// third-party server, so no uploaded theme can be handed the address of
	// everyone who loads a page.
	if err := pkg.ValidateAssetReferences(values); err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}

	if err := a.store.SaveTheme(ctx, name, string(pkg.Structure), colors); err != nil {
		log.Error().Err(err).Str("theme", name).Msg("cannot save theme")
		return nil, huma.Error500InternalServerError("cannot save the theme")
	}
	if err := a.storePackageAssets(ctx, name, pkg); err != nil {
		log.Error().Err(err).Str("theme", name).Msg("cannot store package assets")
		return nil, huma.Error500InternalServerError("theme saved but its files could not be stored")
	}
	if in.Activate {
		if err := a.store.SetActiveTheme(ctx, name, ""); err != nil {
			log.Error().Err(err).Str("theme", name).Msg("cannot activate theme")
			return nil, huma.Error500InternalServerError("theme saved but could not be activated")
		}
	}

	log.Info().Str("theme", name).Strs("colours", out.Body.Colors).
		Int("files", len(pkg.Assets)).Bool("active", in.Activate).Msg("theme imported")

	out.Body.Name = name
	out.Body.Files = len(pkg.Assets)
	out.Body.Active = in.Activate
	return out, nil
}

// EmptyOutput is a response with no body.
type EmptyOutput struct{}

func (a *API) deleteTheme(ctx context.Context, in *ThemeNameInput) (*EmptyOutput, error) {
	if in.Name == builtInName {
		return nil, huma.Error422UnprocessableEntity("the built-in default cannot be deleted: it is the fallback")
	}

	err := a.store.DeleteTheme(ctx, in.Name)
	if errors.Is(err, store.ErrThemeNotFound) {
		return nil, huma.Error404NotFound("no such theme")
	}
	if err != nil {
		log.Error().Err(err).Str("theme", in.Name).Msg("cannot delete theme")
		return nil, huma.Error500InternalServerError("cannot delete the theme")
	}
	log.Info().Str("theme", in.Name).Msg("theme deleted")
	return &EmptyOutput{}, nil
}

// SetActiveThemeInput selects a theme and one of its colourways.
type SetActiveThemeInput struct {
	Actor string `header:"X-Actor"`
	Body  struct {
		Name  string `json:"name" doc:"Theme name, or \"built-in\" to restore the compiled-in default"`
		Color string `json:"color,omitempty" doc:"Colourway; empty takes the theme's first"`
	}
}

func (a *API) setActiveTheme(ctx context.Context, in *SetActiveThemeInput) (*EmptyOutput, error) {
	name := strings.TrimSpace(in.Body.Name)
	if name == builtInName {
		name = "" // no stored theme is active; the default applies
	}

	err := a.store.SetActiveTheme(ctx, name, strings.TrimSpace(in.Body.Color))
	if errors.Is(err, store.ErrThemeNotFound) {
		return nil, huma.Error404NotFound("no such theme")
	}
	if err != nil {
		log.Error().Err(err).Str("theme", name).Msg("cannot select theme")
		return nil, huma.Error500InternalServerError("cannot select the theme")
	}
	log.Info().Str("theme", in.Body.Name).Str("colour", in.Body.Color).Msg("active theme changed")
	return &EmptyOutput{}, nil
}

// ThemeCSSOutput carries a rendered stylesheet.
type ThemeCSSOutput struct {
	ContentType string `header:"Content-Type"`
	Body        []byte
}

// renderActiveCSS renders the active theme: its structure and the chosen
// colourway, both merged onto the built-in defaults.
//
// A failure in either falls back to the built-in rather than to a blank page.
func (a *API) renderActiveCSS(ctx context.Context, _ *struct{}) (*ThemeCSSOutput, error) {
	out := &ThemeCSSOutput{ContentType: "text/css; charset=utf-8"}

	file, err := a.store.ActiveTheme(ctx)
	if errors.Is(err, store.ErrThemeNotFound) {
		out.Body = []byte("/* No theme selected — the built-in colours, spacing and type apply. */\n")
		return out, nil
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot read the active theme")
		out.Body = []byte("/* The theme could not be read — the built-in default applies. */\n")
		return out, nil
	}

	prefix := themeFilePrefix(file.Name)
	var rendered []byte
	rendered = append(rendered, a.colorCSS(file, prefix)...)
	rendered = append(rendered, a.structureCSS(file, prefix)...)
	out.Body = rendered
	return out, nil
}

func (a *API) colorCSS(file models.ThemeFile, prefix string) []byte {
	for _, colour := range file.Colors {
		if colour.Name != file.ActiveColor && file.ActiveColor != "" {
			continue
		}

		tokens, err := theme.FromDTCG([]byte(colour.Document))
		if err != nil {
			log.Warn().Err(err).Str("theme", file.Name).Str("colour", colour.Name).
				Msg("active colourway does not parse, serving defaults")
			return []byte("/* The colourway could not be parsed — the built-in colours apply. */\n")
		}
		css := theme.RenderOverrideCSS(theme.Merge(theme.DefaultTokens(), tokens))
		return theme.RewriteAssetURLs(css, prefix)
	}
	return []byte("/* The theme has no colourway — the built-in colours apply. */\n")
}

func (a *API) structureCSS(file models.ThemeFile, prefix string) []byte {
	if file.Structure == "" {
		return []byte("/* The theme sets no structure — the built-in spacing and type apply. */\n")
	}

	dimensions, err := theme.StructureFromDTCG([]byte(file.Structure))
	if err != nil {
		log.Warn().Err(err).Str("theme", file.Name).Msg("structure does not parse, serving defaults")
		return []byte("/* The structure could not be parsed — the built-in spacing and type apply. */\n")
	}
	css := theme.RenderStructureCSS(theme.MergeStructure(theme.DefaultStructure(), dimensions))
	return theme.RewriteAssetURLs(css, prefix)
}

// themeFilePrefix is where a theme's package files are published. The web
// services proxy the same shape, so a rewritten url() resolves on whichever
// origin the page was served from.
func themeFilePrefix(name string) string {
	return "/theme/files/" + url.PathEscape(name)
}

// storePackageAssets replaces a theme's package files, leaving its named slots
// — logo, mark, banner — untouched.
func (a *API) storePackageAssets(ctx context.Context, name string, pkg theme.Package) error {
	slots := map[string]bool{}
	for _, slot := range theme.AssetSlots {
		slots[slot.Name] = true
	}

	assets := make([]models.ThemeAsset, 0, len(pkg.Assets))
	for _, filePath := range pkg.AssetNames() {
		asset := pkg.Assets[filePath]
		if slots[filePath] {
			// A package file may not shadow a named slot, which the templates
			// reach for by name.
			continue
		}
		assets = append(assets, models.ThemeAsset{
			Slot:        asset.Path,
			ContentType: asset.ContentType,
			Data:        asset.Data,
			Size:        len(asset.Data),
		})
	}
	return a.store.ReplacePackageAssets(ctx, name, assets, slots)
}

// seedThemes puts the bundled packages into the library on startup, so a fresh
// installation has something to choose besides the built-in default.
//
// Seeding never overwrites: a theme somebody uploaded under a bundled name is
// theirs, and a restart must not undo it.
func (a *API) seedThemes(ctx context.Context) error {
	bundled, err := theme.BundledThemes()
	if err != nil {
		return err
	}

	for _, file := range bundled {
		exists, err := a.store.ThemeExists(ctx, file.Name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}

		colors := map[string]string{}
		for name, document := range file.Package.Colors {
			colors[name] = string(document)
		}
		if err := a.store.SaveTheme(ctx, file.Name, string(file.Package.Structure), colors); err != nil {
			return err
		}
		if err := a.storePackageAssets(ctx, file.Name, file.Package); err != nil {
			return err
		}
		log.Info().Str("theme", file.Name).Int("colours", len(colors)).
			Msg("bundled theme added to the library")
	}
	return nil
}
