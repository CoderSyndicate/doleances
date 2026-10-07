package theme

import (
	"sort"
	"strconv"
	"strings"
)

// PackageReadmeFile is the explanation that travels inside a theme package.
//
// It is written on download and **ignored on upload**, so a designer can
// download a theme, edit it, and send the same zip back without first deleting
// a file they did not add. See isArchiveClutter.
const PackageReadmeFile = "README.md"

// PackageReadme explains the package it is packed into.
//
// # Why it is generated rather than written
//
// Almost everything a designer needs to know here is already a constant in
// this file's own package: which extensions are accepted, how large a package
// may be, how many colourways and how many files. A README listing those by
// hand is a second copy of them that nobody updates — and the way somebody
// finds out it drifted is an upload refused for a reason the documentation
// told them could not happen.
//
// So the numbers and the file types below are read from the code that enforces
// them, and the prose is what is left.
func PackageReadme(name string) string {
	var b strings.Builder

	b.WriteString("# " + name + " — a doléances theme\n\n")
	b.WriteString(`This zip is a theme. It is what the console produced for you, and it is the
same shape the console accepts back: edit what you want, re-zip it, upload it.
Nothing here has to be repacked by hand and there is no manifest to keep in
step.
`)

	b.WriteString("\n## What a theme is\n\n")
	b.WriteString("A theme is **one design shipped with several colourways**, the way a " +
		"garment comes in colours.\n\n")
	b.WriteString("- The **structure** — spacing, shape and type — is what makes the design " +
		"that design, so there is exactly one of it and its name is not a choice.\n")
	b.WriteString("- The **colourways** are variations on it, so they are named and there " +
		"may be many.\n\n")
	b.WriteString("Selecting a theme therefore means picking a theme *and* one of its " +
		"colours. Both are chosen in the console, and the choice of colour is " +
		"remembered per theme.\n")

	b.WriteString("\n## The layout\n\n")
	b.WriteString("```\n")
	b.WriteString(aligned([][2]string{
		{PackageReadmeFile, "this file; ignored when you upload"},
		{PackageStructureFile, "spacing, shape and type — one per theme, fixed name"},
		{PackageColorDir + "/<name>.json", "a colourway; name it what you like"},
		{PackageAssetDir + "/…", "images and fonts your tokens reference"},
	}))
	b.WriteString("```\n\n")
	b.WriteString("**Everything here is optional except one colourway.** A package with a " +
		"single `" + PackageColorDir + "/` file is valid: leave out `" +
		PackageStructureFile + "` and the theme keeps the built-in spacing and " +
		"type, and declare only the tokens you want to change — anything you do " +
		"not mention falls through to the defaults. A file that declares one " +
		"colour changes one colour.\n")

	b.WriteString("\n## The token documents\n\n")
	b.WriteString("Both files are **W3C Design Tokens (DTCG)** documents, which is what " +
		"Figma and Tokens Studio already export.\n\n")
	b.WriteString("A colourway carries `light` and `dark` as two top-level groups — one " +
		"file holding both modes:\n\n")
	b.WriteString("```json\n")
	b.WriteString(`{
  "light": { "accent": { "$type": "color", "$value": "#e4ff1a" } },
  "dark":  { "accent": { "$type": "color", "$value": "#c2d913" } }
}
`)
	b.WriteString("```\n\n")
	b.WriteString("The structure has no modes at all, because a corner radius is the same " +
		"in both, and it groups by type instead — `dimension`, `fontFamily`, " +
		"`number`. Keeping the two apart is what lets a designer work on colour " +
		"without touching spacing, and an operator put any colourway over any " +
		"structure.\n")

	b.WriteString("\n## Assets, and what a theme may not do\n\n")
	b.WriteString("A theme supplies **values and files, never code.** These are the file " +
		"types `" + PackageAssetDir + "/` accepts:\n\n")
	b.WriteString(assetTypeList())
	b.WriteString("\nThere is no HTML, no JavaScript and no CSS in that list and there will " +
		"not be. Anything else in `" + PackageAssetDir + "/` is refused at import, " +
		"with the file named.\n")

	b.WriteString("\n### Referencing a file\n\n")
	b.WriteString("Tokens may use `url()`, and a reference must resolve to a file this " +
		"package itself carries:\n\n")
	b.WriteString("```\n")
	b.WriteString(aligned([][2]string{
		{"url(" + PackageAssetDir + "/paper.webp)", "accepted — the package contains it"},
		{"url(data:image/png;base64,…)", "accepted — self-contained, nothing is fetched"},
		{"url(https://cdn.example/x)", "refused — a theme may not reach another server"},
		{"url(//cdn.example/x)", "refused — the same, written without a scheme"},
		{"url(../../etc/passwd)", "refused — it climbs out of the package"},
		{"url(" + PackageAssetDir + "/missing.webp)", "refused — not in this package"},
	}))
	b.WriteString("```\n\n")
	b.WriteString("This is not fussiness. An accepted reference is rewritten at render " +
		"time to a path this site serves, so a page never asks a third party for " +
		"anything — which means an uploaded theme can never hand somebody else the " +
		"address of every person reading the register. A `data:` URI fetches " +
		"nothing, so it is allowed as it is.\n")

	b.WriteString("\n### The logo, the tab icon and the sharing image are not in here\n\n")
	b.WriteString("Those are **named slots**, managed one at a time on the theme page, and " +
		"they deliberately do not travel in the package: uploading a theme replaces " +
		"what the package contains, and a designer who changed a colour should not " +
		"discover they have also removed the register's logo.\n")

	b.WriteString("\n## Limits\n\n")
	b.WriteString("| | |\n|---|---|\n")
	b.WriteString("| package | " + mib(MaxPackageBytes) + " |\n")
	b.WriteString("| one asset | " + mib(MaxAssetBytes) + " |\n")
	b.WriteString("| assets | " + strconv.Itoa(MaxPackageFiles) + " files |\n")
	b.WriteString("| colourways | " + strconv.Itoa(MaxPackageColors) + " |\n")

	b.WriteString("\n## Uploading replaces, it does not merge\n\n")
	b.WriteString("A colourway or a file you removed from this zip stops existing when you " +
		"upload it, rather than lingering in a dropdown nobody maintains. So edit " +
		"*this* package rather than building a new one from the parts you changed.\n")

	b.WriteString("\n## Contrast is checked and never blocks\n\n")
	b.WriteString("Every colourway is measured against WCAG AA in both modes and the " +
		"console shows a warning for any pairing that fails. It is advice: a theme " +
		"that fails it still uploads and still selects. One pairing is worth " +
		"knowing about before you start — text on the accent colour, which is what " +
		"catches white on a hi-vis yellow at about 1.1 to 1.\n")

	return b.String()
}

// aligned lays out a two-column block so the descriptions line up, computed
// rather than spaced by hand: the left column is built from constants, and a
// constant that changes length should not leave a crooked file in every
// download until somebody notices.
func aligned(rows [][2]string) string {
	widest := 0
	for _, row := range rows {
		// Runes, not bytes — the asset line ends in an ellipsis.
		if width := len([]rune(row[0])); width > widest {
			widest = width
		}
	}

	var b strings.Builder
	for _, row := range rows {
		b.WriteString(row[0])
		b.WriteString(strings.Repeat(" ", widest-len([]rune(row[0]))+2))
		b.WriteString(row[1])
		b.WriteString("\n")
	}
	return b.String()
}

// assetTypeList renders the accepted extensions, grouped so a designer can see
// at a glance that fonts are allowed and stylesheets are not.
func assetTypeList() string {
	images := make([]string, 0, len(packageAssetTypes))
	fonts := make([]string, 0, len(packageAssetTypes))
	for extension, contentType := range packageAssetTypes {
		if strings.HasPrefix(contentType, "font/") {
			fonts = append(fonts, "`"+extension+"`")
			continue
		}
		images = append(images, "`"+extension+"`")
	}
	sort.Strings(images)
	sort.Strings(fonts)

	var b strings.Builder
	b.WriteString("- **images** — " + strings.Join(images, ", ") + "\n")
	b.WriteString("- **fonts** — " + strings.Join(fonts, ", ") + "\n")
	return b.String()
}

// mib renders a byte limit the way a person would say it.
func mib(bytes int) string {
	const unit = 1 << 20
	if bytes >= unit && bytes%unit == 0 {
		return strconv.Itoa(bytes/unit) + " MiB"
	}
	return strconv.Itoa(bytes/1024) + " KiB"
}
