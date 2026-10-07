package theme

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// A theme package is a zip:
//
//	structure.json       spacing, shape and type — one per theme, fixed name
//	colors/<name>.json   colourways — as many as the designer wants
//	assets/…             images and fonts the tokens reference
//
// A theme is one design shipped with several colourways, the way a garment
// comes in colours. The structure is what makes it that design, so there is
// exactly one and its name is not a choice; the colourways are variations on
// it, so they are named and plural. Selecting a theme therefore means picking
// a theme and one of its colours.
//
// There is no standard for any of this. DTCG has no asset token type (a File
// type is under consideration), and every ecosystem that needed packaging —
// WordPress, VS Code, browser extensions — invented its own zip with a
// manifest. Ours is deliberately the smallest thing that works: two known
// names, no manifest.
//
// Tokens may reference the package's own assets with url(). Those references
// are validated at import and rewritten at render time to a path this server
// serves, so a theme can never point a visitor's browser at a third party.
const (
	// PackageStructureFile is the structure document's fixed name.
	PackageStructureFile = "structure.json"

	// PackageColorDir holds the colourways, one file each.
	PackageColorDir = "colors"

	// PackageAssetDir is the folder assets are read from, and the prefix a
	// token must use to reference them.
	PackageAssetDir = "assets"

	// MaxPackageBytes bounds an uploaded package.
	MaxPackageBytes = 8 << 20 // 8 MiB

	// MaxPackageFiles bounds how many assets one theme may carry.
	MaxPackageFiles = 64

	// MaxPackageColors bounds how many colourways one theme may carry.
	MaxPackageColors = 32
)

// Package is an unpacked theme package.
type Package struct {
	// Structure is the DTCG structure document. Empty when the theme keeps
	// the built-in spacing and type.
	Structure []byte

	// Colors are the colourways, keyed by name — "parchment", "nuit".
	Colors map[string][]byte

	// Assets are the files, keyed by their path inside assets/.
	Assets map[string]Asset
}

// Asset is one file from a package.
type Asset struct {
	Path        string
	ContentType string
	Data        []byte
}

// packageAssetTypes are what a package may carry: what CSS can reference, and
// nothing that executes.
//
// No HTML, JS or CSS: a theme supplies values and files, never code.
var packageAssetTypes = map[string]string{
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".webp":  "image/webp",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".avif":  "image/avif",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".otf":   "font/otf",
	".ttf":   "font/ttf",
}

// ReadPackage unpacks and validates a theme package.
func ReadPackage(data []byte) (Package, error) {
	if len(data) > MaxPackageBytes {
		return Package{}, fmt.Errorf("the package is %d bytes; the limit is %d", len(data), MaxPackageBytes)
	}

	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Package{}, fmt.Errorf("not a readable zip: %w", err)
	}

	pkg := newPackage()
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}

		name := path.Clean(entry.Name)
		// A zip may carry "../" or absolute paths; neither may reach the store.
		if strings.HasPrefix(name, "..") || path.IsAbs(name) {
			return Package{}, fmt.Errorf("the package contains an unsafe path: %q", entry.Name)
		}
		if isArchiveClutter(name) {
			continue
		}

		content, err := readEntry(entry, entryLimit(name))
		if err != nil {
			return Package{}, err
		}
		if err := pkg.add(name, content); err != nil {
			return Package{}, err
		}
	}
	return pkg, pkg.validate()
}

// ReadPackageFS reads a theme package from a directory tree, which is how the
// bundled themes ship: the same layout, unzipped.
func ReadPackageFS(fsys fs.FS, dir string) (Package, error) {
	pkg := newPackage()

	err := fs.WalkDir(fsys, dir, func(p string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		name := strings.TrimPrefix(p, dir+"/")
		if isArchiveClutter(name) {
			return nil
		}

		content, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		return pkg.add(name, content)
	})
	if err != nil {
		return Package{}, err
	}
	return pkg, pkg.validate()
}

func newPackage() Package {
	return Package{Colors: map[string][]byte{}, Assets: map[string]Asset{}}
}

func isArchiveClutter(name string) bool {
	// Archives made on a Mac carry a metadata folder nobody wants.
	if strings.HasPrefix(name, "__MACOSX/") || path.Base(name) == ".DS_Store" {
		return true
	}

	// The explanation this project packs into every download. Skipped rather
	// than refused, because the alternative is that every theme downloaded
	// from the console fails to upload again until somebody works out which
	// file to delete — and the round trip is the whole point of the package
	// being the same shape in both directions.
	return name == PackageReadmeFile
}

func entryLimit(name string) int {
	if strings.HasPrefix(name, PackageAssetDir+"/") {
		return MaxAssetBytes
	}
	return MaxPackageBytes
}

// add files one entry into the package by where it sits.
func (p *Package) add(name string, content []byte) error {
	switch {
	case name == PackageStructureFile:
		p.Structure = content
		return nil

	case strings.HasPrefix(name, PackageColorDir+"/"):
		colour := strings.TrimSuffix(strings.TrimPrefix(name, PackageColorDir+"/"), ".json")
		if colour == "" || strings.Contains(colour, "/") {
			return fmt.Errorf("colourway %q must be a single .json file in %s/", name, PackageColorDir)
		}
		if len(p.Colors) >= MaxPackageColors {
			return fmt.Errorf("the package holds more than %d colourways", MaxPackageColors)
		}
		p.Colors[colour] = content
		return nil

	case strings.HasPrefix(name, PackageAssetDir+"/"):
		asset, err := newAsset(strings.TrimPrefix(name, PackageAssetDir+"/"), content)
		if err != nil {
			return err
		}
		if len(p.Assets) >= MaxPackageFiles {
			return fmt.Errorf("the package holds more than %d assets", MaxPackageFiles)
		}
		p.Assets[asset.Path] = asset
		return nil

	default:
		return fmt.Errorf(
			"unexpected file %q: a package holds %s, %s/ and %s/",
			name, PackageStructureFile, PackageColorDir, PackageAssetDir)
	}
}

// validate refuses a package that could not be selected.
func (p Package) validate() error {
	if len(p.Colors) == 0 {
		return fmt.Errorf("the package has no colourway: add at least one file under %s/", PackageColorDir)
	}
	return nil
}

// ColorNames lists a package's colourways in a stable order.
func (p Package) ColorNames() []string {
	names := make([]string, 0, len(p.Colors))
	for name := range p.Colors {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func readEntry(entry *zip.File, limit int) ([]byte, error) {
	file, err := entry.Open()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", entry.Name, err)
	}
	defer file.Close() //nolint:errcheck

	// One byte past the limit, so an oversized entry is refused rather than
	// silently truncated. A zip declares its own sizes, and a declared size
	// is not evidence of anything.
	content, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", entry.Name, err)
	}
	if len(content) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", entry.Name, limit)
	}
	return content, nil
}

func newAsset(name string, content []byte) (Asset, error) {
	contentType, ok := packageAssetTypes[strings.ToLower(path.Ext(name))]
	if !ok {
		return Asset{}, fmt.Errorf("unsupported asset %q: a package carries images and fonts", name)
	}
	if len(content) > MaxAssetBytes {
		return Asset{}, fmt.Errorf("%s is larger than %d bytes", name, MaxAssetBytes)
	}
	return Asset{Path: name, ContentType: contentType, Data: content}, nil
}

// AssetNames lists a package's assets in a stable order.
func (p Package) AssetNames() []string {
	names := make([]string, 0, len(p.Assets))
	for name := range p.Assets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// urlRe finds a CSS url(...) reference and captures what is inside it.
var urlRe = regexp.MustCompile(`url\(\s*['"]?([^'")]+)['"]?\s*\)`)

// ValidateAssetReferences checks that every url() in a token points at a file
// the package actually carries.
//
// This is what makes url() safe to allow: a reference can only ever resolve
// inside the package, so no uploaded theme can reach a third-party server and
// hand it the address of everyone who loads a page.
func (p Package) ValidateAssetReferences(values []string) error {
	for _, value := range values {
		for _, match := range urlRe.FindAllStringSubmatch(value, -1) {
			target := strings.TrimSpace(match[1])

			switch {
			case strings.HasPrefix(target, "data:"):
				// Self-contained, nothing to fetch, nobody to tell.
				continue
			case !strings.HasPrefix(target, PackageAssetDir+"/"):
				return fmt.Errorf(
					"url(%s) does not point inside the package: a theme may only reference its own %s/ files",
					target, PackageAssetDir)
			}

			name := strings.TrimPrefix(path.Clean(target), PackageAssetDir+"/")
			if _, ok := p.Assets[name]; !ok {
				return fmt.Errorf("url(%s) refers to a file the package does not contain", target)
			}
		}
	}
	return nil
}

// RewriteAssetURLs turns the package-relative references in a rendered
// stylesheet into paths this server serves.
//
// prefix is where the theme's files are published, without a trailing slash.
func RewriteAssetURLs(css []byte, prefix string) []byte {
	return urlRe.ReplaceAllFunc(css, func(match []byte) []byte {
		groups := urlRe.FindSubmatch(match)
		target := strings.TrimSpace(string(groups[1]))
		if !strings.HasPrefix(target, PackageAssetDir+"/") {
			return match
		}

		name := strings.TrimPrefix(path.Clean(target), PackageAssetDir+"/")
		return []byte("url(" + prefix + "/" + name + ")")
	})
}

// LooksLikePackage reports whether an upload is a zip rather than a bare DTCG
// document, so one upload path can accept either.
func LooksLikePackage(data []byte) bool {
	// "PK\x03\x04" is the local file header every zip begins with. An empty
	// archive starts "PK\x05\x06", and would carry no tokens anyway.
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K' && data[2] == 3 && data[3] == 4
}
