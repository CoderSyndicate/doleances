package models

// ThemeAsset is a file belonging to a theme.
//
// Two kinds of file live here. A **named slot** — logo, mark, banner — is one
// the templates reference by name, so the HTML knows which image is the logo.
// A **package file** is anything a theme's own CSS references by url(), and
// its name is simply its path inside the package.
//
// Assets live in the database alongside the theme files, for the same reason:
// the backend already owns that storage, and a logo is small. When assets grow
// past that — photographs, many of them — this is what moves behind the
// `storage` abstraction, and the model is shaped so that move touches only the
// store.
type ThemeAsset struct {
	Model

	// ThemeName ties the asset to a theme in the library.
	ThemeName string `gorm:"size:128;uniqueIndex:idx_theme_asset" json:"theme_name"`

	// Slot is a named slot (logo, mark, banner) or a package file path
	// ("background.webp", "type/serif.woff2").
	Slot string `gorm:"size:256;uniqueIndex:idx_theme_asset" json:"slot"`

	ContentType string `gorm:"size:128" json:"content_type"`

	// Data is the image itself. Bounded at upload — see theme.MaxAssetBytes.
	Data []byte `json:"-"`

	// Size is kept alongside so a listing need not load every image.
	Size int `json:"size"`
}
