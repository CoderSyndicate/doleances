package models

// ThemeFile is one theme in the library.
//
// A theme is a design shipped with several colourways, the way a garment comes
// in colours: exactly one structure — spacing, shape and type — and as many
// named colours as the designer supplies. Selecting a theme therefore means
// choosing a theme and one of its colours.
type ThemeFile struct {
	Model

	// Name identifies the theme in the library and in downloads.
	Name string `gorm:"uniqueIndex;size:128" json:"name"`

	// Structure is the DTCG structure document. Empty when the theme keeps
	// the built-in spacing and type.
	Structure string `gorm:"type:text" json:"-"`

	// Active marks the theme in force. At most one row may have it, which the
	// store enforces when selecting.
	Active bool `gorm:"index" json:"active"`

	// ActiveColor is which of the theme's colourways is in force. It is kept
	// per theme, so switching away and back restores the colour that was
	// chosen rather than resetting it.
	ActiveColor string `gorm:"size:128" json:"active_color"`

	// Colourways are linked by the theme's name rather than its id, so a
	// downloaded package can be re-uploaded and keep its colours attached.
	Colors []ThemeColor `gorm:"foreignKey:ThemeName;references:Name;constraint:OnDelete:CASCADE" json:"colors,omitempty"`
}

// ThemeColor is one colourway of a theme: a DTCG document with a light and a
// dark mode.
type ThemeColor struct {
	Model

	ThemeName string `gorm:"size:128;uniqueIndex:idx_theme_color" json:"theme_name"`
	Name      string `gorm:"size:128;uniqueIndex:idx_theme_color" json:"name"`

	// Document is the DTCG colour document as uploaded.
	Document string `gorm:"type:text" json:"-"`
}
