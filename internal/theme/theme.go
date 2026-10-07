// Package theme is the colour system every service renders from.
//
// The embedded tokens.css is the single source of truth: the Go side never
// re-declares a colour value, it parses this file. An uploaded theme file
// overrides the same token names, and the static defaults stay the always
// present fallback — if the database or the overlay endpoint is unavailable,
// the UI renders built-in colours rather than blanking.
package theme

import _ "embed"

// TokensCSS is the default palette: the three theme-state blocks.
//
//go:embed tokens.css
var TokensCSS []byte

// BaseCSS is the shared component styling, written entirely against the
// tokens so that no service holds a colour of its own.
//
//go:embed base.css
var BaseCSS []byte

// ToggleJS drives the light/dark button.
//
//go:embed theme.js
var ToggleJS []byte
