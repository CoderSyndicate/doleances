package theme

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
)

// bundled holds the theme packages compiled into the binary, in the same
// layout an uploaded zip uses — one folder per theme.
//
//go:embed library/*/structure.json library/*/colors/*.json
var bundled embed.FS

// Bundled is one theme shipped with the application.
type Bundled struct {
	// Name is how the theme appears in the library.
	Name string

	// Package is its structure, colourways and assets.
	Package Package
}

// BundledThemes returns the themes compiled into the binary, so a fresh
// installation has something to choose besides the built-in default.
func BundledThemes() ([]Bundled, error) {
	entries, err := fs.ReadDir(bundled, "library")
	if err != nil {
		return nil, fmt.Errorf("read bundled themes: %w", err)
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	out := make([]Bundled, 0, len(names))
	for _, name := range names {
		pkg, err := ReadPackageFS(bundled, "library/"+name)
		if err != nil {
			return nil, fmt.Errorf("bundled theme %q: %w", name, err)
		}
		out = append(out, Bundled{Name: name, Package: pkg})
	}
	return out, nil
}
