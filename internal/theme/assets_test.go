package theme

import (
	"encoding/xml"
	"io"
	"io/fs"
	"path"
	"strings"
	"testing"
)

// TestEveryBuiltInAssetIsWellFormed.
//
// # The failure this exists for was completely silent
//
// A comment in logo.svg was written with a CSS custom property spelled in
// full, leading dashes and all. A pair of hyphens cannot appear inside an XML
// comment, so the document was malformed from that line on — and nothing in
// this project had an opinion about it. The route answered 200, with
// Content-Type image/svg+xml, with the right number of bytes, and with its
// security headers. Every one of those is what a working asset looks like.
// The only symptom was a broken-image placeholder in the masthead, which is
// something a person has to look at a page to notice.
//
// An embedded asset is bytes the compiler copies without reading. This is the
// cheapest thing that reads them.
func TestEveryBuiltInAssetIsWellFormed(t *testing.T) {
	assets, err := fs.Glob(defaultAssets, "assets/*.svg")
	if err != nil {
		t.Fatalf("glob the assets: %v", err)
	}
	if len(assets) == 0 {
		t.Fatal("no assets were found, so this test proves nothing")
	}

	for _, name := range assets {
		raw, err := defaultAssets.ReadFile(name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}

		decoder := xml.NewDecoder(strings.NewReader(string(raw)))
		for {
			_, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("%s is not well-formed XML, so a browser will draw its "+
					"broken-image placeholder however well the route answers: %v", name, err)
				break
			}
		}
	}
}

// TestTheTabIconCarriesItsOwnColours.
//
// currentColor in a favicon is not "the palette" — it is black, because black
// is what the color property initialises to and a favicon has no document to
// inherit from. Drawn that way the mark was invisible against a dark tab bar,
// which is most browsers in dark mode.
//
// This is the one image here shown on a surface we do not control, in a colour
// nobody tells us, so it has to bring its own ground and its own ink. The
// logo is deliberately not held to this: it sits on our own masthead.
func TestTheTabIconCarriesItsOwnColours(t *testing.T) {
	raw, err := defaultAssets.ReadFile(path.Join("assets", "mark.svg"))
	if err != nil {
		t.Fatalf("read the mark: %v", err)
	}

	// Only the drawing is checked. The comment above it explains the rule and
	// names currentColor to do so, which is not the same as using it.
	drawing := string(raw)
	if start := strings.Index(drawing, "-->"); start >= 0 {
		drawing = drawing[start+len("-->"):]
	}

	if strings.Contains(drawing, "currentColor") {
		t.Error("the tab icon is drawn in currentColor, which resolves to black " +
			"in a favicon and disappears against a dark tab bar")
	}
	if !strings.Contains(drawing, "fill=") {
		t.Error("the tab icon declares no fill, so it has no colours of its own")
	}
}
