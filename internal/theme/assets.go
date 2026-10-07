package theme

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// defaultAssets are the images shipped with the binary: the fallback whenever
// the active theme provides none.
//
//go:embed assets/*
var defaultAssets embed.FS

// MaxAssetBytes bounds one uploaded asset. A logo is a few kilobytes; anything
// near this limit is a photograph that does not belong in a theme.
const MaxAssetBytes = 512 << 10 // 512 KiB

// AssetSlot is a named place in the design where an image goes.
//
// Slots are a fixed set rather than free-form filenames, which is what makes
// the fallback meaningful: a template asks for "logo" and always gets one,
// from the active theme or from the built-in defaults.
type AssetSlot struct {
	// Name is how templates and URLs refer to it.
	Name string
	// Description says where it appears, for the upload form.
	Description string
}

// AssetSlots are the images a theme may replace.
var AssetSlots = []AssetSlot{
	{Name: "logo", Description: "The mark in the masthead, beside the site name."},
	{Name: "mark", Description: "A square icon: browser tab, bookmarks, app tiles."},
	{Name: "banner", Description: "The wide image used when a page is shared."},
}

// IsAssetSlot reports whether a name is one of the defined slots.
func IsAssetSlot(name string) bool {
	for _, slot := range AssetSlots {
		if slot.Name == name {
			return true
		}
	}
	return false
}

// allowedAssetTypes are the media types an asset may have.
//
// SVG is included because a logo is usually one, and it carries a caveat: an
// SVG can contain script, so an uploaded one is served with a restrictive
// Content-Security-Policy and never inlined into a page.
var allowedAssetTypes = map[string]string{
	"image/svg+xml": ".svg",
	"image/png":     ".png",
	"image/webp":    ".webp",
	"image/jpeg":    ".jpg",
	"image/gif":     ".gif",
}

// ValidateAsset checks an upload before it is stored.
func ValidateAsset(slot, contentType string, size int) error {
	if !IsAssetSlot(slot) {
		return fmt.Errorf("unknown asset %q: expected one of %s", slot, strings.Join(slotNames(), ", "))
	}
	if _, ok := allowedAssetTypes[normalizeType(contentType)]; !ok {
		return fmt.Errorf("unsupported image type %q", contentType)
	}
	if size <= 0 {
		return fmt.Errorf("the file is empty")
	}
	if size > MaxAssetBytes {
		return fmt.Errorf("the file is %d bytes; the limit is %d", size, MaxAssetBytes)
	}
	return nil
}

func slotNames() []string {
	names := make([]string, 0, len(AssetSlots))
	for _, slot := range AssetSlots {
		names = append(names, slot.Name)
	}
	sort.Strings(names)
	return names
}

// normalizeType drops any parameters, so "image/svg+xml; charset=utf-8"
// matches.
func normalizeType(contentType string) string {
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	return strings.ToLower(strings.TrimSpace(contentType))
}

// DefaultAsset returns the shipped image for a slot, and whether one exists.
//
// Not every slot has a default: there is no built-in banner, and a page that
// wants one has to cope with its absence.
func DefaultAsset(slot string) (data []byte, contentType string, ok bool) {
	if !IsAssetSlot(slot) {
		return nil, "", false
	}

	for mediaType, extension := range allowedAssetTypes {
		data, err := fs.ReadFile(defaultAssets, path.Join("assets", slot+extension))
		if err == nil {
			return data, mediaType, true
		}
	}
	return nil, "", false
}
