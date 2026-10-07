package theme

import (
	"encoding/json"
	"strings"
	"testing"
)

// dtcgTypes are the types the W3C format defines. A token typed outside this
// set is not something a design tool can read.
var dtcgTypes = map[string]bool{
	"color": true, "dimension": true, "fontFamily": true, "fontWeight": true,
	"duration": true, "cubicBezier": true, "number": true,
	"strokeStyle": true, "border": true, "transition": true,
	"shadow": true, "gradient": true, "typography": true,
}

func TestDefaultStructureIsParsed(t *testing.T) {
	structure := DefaultStructure()
	if len(structure) == 0 {
		t.Fatal("no structural tokens parsed from tokens.css")
	}

	for _, d := range structure {
		if d.Value == "" {
			t.Errorf("%s has no value", d.Name)
		}
		if !dtcgTypes[d.Type] {
			t.Errorf("%s is typed %q, which is not a DTCG type", d.Name, d.Type)
		}
	}
}

func TestStructureHoldsNoColour(t *testing.T) {
	// Colour and structure are selected independently; a colour that leaked
	// into the structure block could not be changed by a palette.
	for _, d := range DefaultStructure() {
		if strings.HasPrefix(d.Value, "#") {
			t.Errorf("%s = %q is a colour, and belongs in the palette", d.Name, d.Value)
		}
	}
}

func TestPaletteAndStructureDoNotOverlap(t *testing.T) {
	// The two overlays are rendered separately; a name in both would mean the
	// later one silently wins.
	colours := map[string]bool{}
	for _, t := range DefaultTokens() {
		colours[t.Name] = true
	}
	for _, d := range DefaultStructure() {
		if colours[d.Name] {
			t.Errorf("%q is declared as both a colour and a structural token", d.Name)
		}
	}
}

func TestStructureDTCGRoundTrip(t *testing.T) {
	original := DefaultStructure()

	parsed, err := StructureFromDTCG(StructureToDTCG(original))
	if err != nil {
		t.Fatalf("StructureFromDTCG: %v", err)
	}
	if len(parsed) != len(original) {
		t.Fatalf("round-tripped %d tokens, want %d", len(parsed), len(original))
	}

	byName := map[string]Dimension{}
	for _, d := range parsed {
		byName[d.Name] = d
	}
	for _, want := range original {
		got, ok := byName[want.Name]
		if !ok {
			t.Errorf("%s was lost in the round trip", want.Name)
			continue
		}
		if got.Value != want.Value || got.Type != want.Type {
			t.Errorf("%s round-tripped as %q/%q, want %q/%q", want.Name, got.Type, got.Value, want.Type, want.Value)
		}
	}
}

func TestStructureDTCGIsValidJSON(t *testing.T) {
	var doc map[string]map[string]json.RawMessage
	if err := json.Unmarshal(StructureToDTCG(DefaultStructure()), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if _, ok := doc["fontFamily"]; !ok {
		t.Error("no fontFamily group — font stacks are the point of a structure theme")
	}
}

func TestRenderStructureCSSIsOneBlock(t *testing.T) {
	// Structure has no modes, so emitting dark variants would be noise that
	// could drift out of step with the light ones.
	css := string(RenderStructureCSS([]Dimension{{Name: "radius", Value: "0"}}))

	if strings.Count(css, ":root") != 1 {
		t.Errorf("rendered %d :root blocks, want 1:\n%s", strings.Count(css, ":root"), css)
	}
	if strings.Contains(css, "data-theme") || strings.Contains(css, "prefers-color-scheme") {
		t.Errorf("structure CSS carries theme-state selectors:\n%s", css)
	}
	if !strings.Contains(css, "--radius: 0;") {
		t.Errorf("token missing from output:\n%s", css)
	}
}

func TestMergeStructureFillsGaps(t *testing.T) {
	merged := MergeStructure(DefaultStructure(), []Dimension{{Name: "radius", Value: "0"}})

	var radius, measure string
	for _, d := range merged {
		switch d.Name {
		case "radius":
			radius = d.Value
		case "measure":
			measure = d.Value
		}
	}
	if radius != "0" {
		t.Errorf("radius = %q, want the override", radius)
	}
	if measure == "" {
		t.Error("measure was dropped; a partial structure file must keep the defaults")
	}
}

func TestStructureFromDTCGRejectsRubbish(t *testing.T) {
	for _, in := range []string{"not json", "{}", `{"dimension":{}}`} {
		if _, err := StructureFromDTCG([]byte(in)); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}

func TestBundledStructureThemesAreUsable(t *testing.T) {
	themes, err := BundledThemes()
	if err != nil {
		t.Fatalf("BundledThemes: %v", err)
	}
	if len(themes) == 0 {
		t.Fatal("no theme is bundled")
	}

	for _, bundled := range themes {
		if len(bundled.Package.Structure) == 0 {
			continue // a theme may keep the built-in structure
		}

		t.Run(bundled.Name, func(t *testing.T) {
			dimensions, err := StructureFromDTCG(bundled.Package.Structure)
			if err != nil {
				t.Fatalf("does not parse: %v", err)
			}

			// A structure that smuggles in a colour would override the
			// colourway the operator chose.
			for _, d := range dimensions {
				if strings.HasPrefix(d.Value, "#") {
					t.Errorf("%s = %q is a colour", d.Name, d.Value)
				}
				if !dtcgTypes[d.Type] {
					t.Errorf("%s is typed %q, which is not a DTCG type", d.Name, d.Type)
				}
			}
			if len(MergeStructure(DefaultStructure(), dimensions)) != len(DefaultStructure()) {
				t.Error("the theme introduces tokens the stylesheet does not use")
			}
		})
	}
}

func TestEveryBundledThemeHasAColourway(t *testing.T) {
	// A theme with no colourway cannot be selected, so it would sit in the
	// library doing nothing.
	themes, err := BundledThemes()
	if err != nil {
		t.Fatalf("BundledThemes: %v", err)
	}

	for _, bundled := range themes {
		if len(bundled.Package.Colors) == 0 {
			t.Errorf("%s has no colourway", bundled.Name)
		}
	}
}
