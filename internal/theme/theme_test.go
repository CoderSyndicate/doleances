package theme

import (
	"math"
	"regexp"
	"strings"
	"testing"
)

func tokenByName(tokens []Token, name string) (Token, bool) {
	for _, t := range tokens {
		if t.Name == name {
			return t, true
		}
	}
	return Token{}, false
}

func TestDefaultTokensAreParsedFromCSS(t *testing.T) {
	tokens := DefaultTokens()
	if len(tokens) == 0 {
		t.Fatal("no tokens parsed from tokens.css")
	}

	accent, ok := tokenByName(tokens, "accent")
	if !ok {
		t.Fatal("no --accent token")
	}
	if accent.Light == "" || accent.Dark == "" {
		t.Errorf("accent = %+v, want both modes filled", accent)
	}
}

func TestEveryDefaultTokenHasBothModes(t *testing.T) {
	// A token defined in only one mode would silently keep the light value in
	// dark, which looks like a rendering bug rather than a missing value.
	for _, token := range DefaultTokens() {
		if strings.TrimSpace(token.Light) == "" {
			t.Errorf("%s has no light value", token.Name)
		}
		if strings.TrimSpace(token.Dark) == "" {
			t.Errorf("%s has no dark value", token.Name)
		}
	}
}

func TestDefaultTokensAreColoursOnly(t *testing.T) {
	// Non-colour tokens live in a later :root block precisely so the DTCG
	// export stays colours: a "6px" exported as $type color is nonsense.
	for _, token := range DefaultTokens() {
		if _, _, _, ok := parseHex(token.Light); !ok {
			t.Errorf("%s = %q, which is not a hex colour", token.Name, token.Light)
		}
	}
}

func TestDefaultPaletteMeetsContrast(t *testing.T) {
	// The shipped palette must pass its own checker, or the warning pills in
	// the console start out meaningless.
	if warnings := CheckContrast(DefaultTokens()); len(warnings) > 0 {
		for _, w := range warnings {
			t.Errorf("default palette: %s %s is %.2f, want at least %.1f", w.Mode, w.Pair, w.Ratio, w.Min)
		}
	}
}

func TestBlackOnYellowNotWhite(t *testing.T) {
	// The rule the whole theme turns on, asserted rather than trusted.
	tokens := DefaultTokens()
	accent, _ := tokenByName(tokens, "accent")
	onAccent, _ := tokenByName(tokens, "on-accent")

	if got := ContrastRatio(onAccent.Light, accent.Light); got < 4.5 {
		t.Errorf("on-accent on accent is %.2f, want at least 4.5", got)
	}
	if got := ContrastRatio("#ffffff", accent.Light); got >= 4.5 {
		t.Errorf("white on accent is %.2f — if this ever passes, the accent is no longer hi-vis yellow", got)
	}
}

func TestRenderOverrideCSSHasThreeStates(t *testing.T) {
	css := string(RenderOverrideCSS([]Token{{Name: "bg", Light: "#ffffff", Dark: "#000000"}}))

	for _, want := range []string{
		":root {",
		"@media (prefers-color-scheme: dark)",
		`:root:not([data-theme="light"])`,
		`:root[data-theme="dark"]`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("rendered CSS is missing %q:\n%s", want, css)
		}
	}
	if !strings.Contains(css, "--bg: #ffffff;") || !strings.Contains(css, "--bg: #000000;") {
		t.Errorf("rendered CSS does not carry both values:\n%s", css)
	}
}

func TestRenderOverrideCSSSkipsEmptyValues(t *testing.T) {
	css := string(RenderOverrideCSS([]Token{
		{Name: "bg", Light: "#ffffff"},
		{Name: "", Light: "#123456"},
	}))

	if strings.Contains(css, "--: ") {
		t.Errorf("a nameless token was rendered:\n%s", css)
	}
	if strings.Count(css, "--bg:") != 1 {
		t.Errorf("a token with no dark value was rendered into the dark blocks:\n%s", css)
	}
}

func TestRenderedOverrideParsesBackToTheSameTokens(t *testing.T) {
	// The overlay has to be shaped exactly like tokens.css, or the parser that
	// reads the defaults could not read an overlay.
	original := []Token{
		{Name: "bg", Light: "#ffffff", Dark: "#000000"},
		{Name: "text", Light: "#111111", Dark: "#eeeeee"},
	}
	css := string(RenderOverrideCSS(original))

	order, light := parseVarBlock(firstMatch(rootLightRe, css))
	_, dark := parseVarBlock(firstMatch(rootDarkRe, css))

	if len(order) != len(original) {
		t.Fatalf("parsed %d tokens, want %d", len(order), len(original))
	}
	for _, want := range original {
		if light[want.Name] != want.Light || dark[want.Name] != want.Dark {
			t.Errorf("%s round-tripped as %q/%q, want %q/%q",
				want.Name, light[want.Name], dark[want.Name], want.Light, want.Dark)
		}
	}
}

func TestDTCGRoundTrip(t *testing.T) {
	original := DefaultTokens()

	parsed, err := FromDTCG(ToDTCG(original))
	if err != nil {
		t.Fatalf("FromDTCG: %v", err)
	}
	if len(parsed) != len(original) {
		t.Fatalf("round-tripped %d tokens, want %d", len(parsed), len(original))
	}
	for i := range original {
		if parsed[i] != original[i] {
			t.Errorf("token %d round-tripped as %+v, want %+v", i, parsed[i], original[i])
		}
	}
}

func TestDTCGIsValidJSONShape(t *testing.T) {
	out := string(ToDTCG(DefaultTokens()))

	for _, want := range []string{`"light": {`, `"dark": {`, `"$type":"color"`, `"$value":"#e4ff1a"`} {
		if !strings.Contains(out, want) {
			t.Errorf("DTCG output is missing %q", want)
		}
	}
}

func TestFromDTCGTolerantOfPartialDocuments(t *testing.T) {
	// A designer who touched only the accent should not have to ship the rest.
	tokens, err := FromDTCG([]byte(`{"light":{"accent":{"$type":"color","$value":"#ff0000"}}}`))
	if err != nil {
		t.Fatalf("FromDTCG: %v", err)
	}
	if len(tokens) != 1 || tokens[0].Name != "accent" || tokens[0].Light != "#ff0000" {
		t.Fatalf("parsed %+v, want a single accent token", tokens)
	}
	if tokens[0].Dark != "" {
		t.Errorf("dark = %q, want it left empty for Merge to fill", tokens[0].Dark)
	}
}

func TestFromDTCGRejectsRubbish(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"not json", "this is not json"},
		{"no colour groups", `{"spacing":{"sm":{"$type":"dimension","$value":"4px"}}}`},
		{"empty document", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := FromDTCG([]byte(tt.in)); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestMergeFillsGapsFromTheDefaults(t *testing.T) {
	merged := Merge(DefaultTokens(), []Token{{Name: "accent", Light: "#ff0000"}})

	accent, ok := tokenByName(merged, "accent")
	if !ok {
		t.Fatal("accent disappeared during merge")
	}
	if accent.Light != "#ff0000" {
		t.Errorf("light accent = %q, want the override", accent.Light)
	}
	// The override said nothing about dark, so the default must survive.
	defaultAccent, _ := tokenByName(DefaultTokens(), "accent")
	if accent.Dark != defaultAccent.Dark {
		t.Errorf("dark accent = %q, want the default %q", accent.Dark, defaultAccent.Dark)
	}
	if len(merged) != len(DefaultTokens()) {
		t.Errorf("merge produced %d tokens, want %d", len(merged), len(DefaultTokens()))
	}
}

func TestMergeKeepsNewTokens(t *testing.T) {
	merged := Merge(DefaultTokens(), []Token{{Name: "brand-extra", Light: "#123456", Dark: "#654321"}})

	if _, ok := tokenByName(merged, "brand-extra"); !ok {
		t.Error("a token the theme introduced was dropped")
	}
}

func TestContrastRatio(t *testing.T) {
	tests := []struct {
		name   string
		a, b   string
		want   float64
		within float64
	}{
		{"black on white", "#000000", "#ffffff", 21, 0.01},
		{"white on black", "#ffffff", "#000000", 21, 0.01},
		{"identical", "#123456", "#123456", 1, 0.01},
		{"short form", "#fff", "#000", 21, 0.01},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContrastRatio(tt.a, tt.b); math.Abs(got-tt.want) > tt.within {
				t.Errorf("ContrastRatio(%s, %s) = %.2f, want %.2f", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestContrastRatioSkipsUnparseableValues(t *testing.T) {
	for _, value := range []string{"var(--accent)", "rgba(0,0,0,.5)", "", "nonsense"} {
		if got := ContrastRatio(value, "#ffffff"); got != 0 {
			t.Errorf("ContrastRatio(%q, #ffffff) = %.2f, want 0 so the checker skips it", value, got)
		}
	}
}

func TestCheckContrastFindsAnUnreadablePairing(t *testing.T) {
	warnings := CheckContrast([]Token{
		{Name: "bg", Light: "#ffffff", Dark: "#ffffff"},
		{Name: "text", Light: "#f0f0f0", Dark: "#f0f0f0"},
	})
	if len(warnings) == 0 {
		t.Fatal("near-white text on white went unreported")
	}
	for _, w := range warnings {
		if w.Pair != "text on bg" {
			t.Errorf("unexpected warning %+v", w)
		}
		if w.Ratio >= w.Min {
			t.Errorf("warning %+v does not actually fail its minimum", w)
		}
	}
}

func TestSwatches(t *testing.T) {
	got := Swatches(DefaultTokens())
	for _, name := range []string{"bg", "surface", "accent", "text"} {
		if got[name] == "" {
			t.Errorf("no swatch for %q", name)
		}
	}
}

func TestBaseCSSHoldsNoRawColour(t *testing.T) {
	// A hex outside tokens.css is a colour no theme can change, which defeats
	// the whole token system.
	hex := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`)
	for i, line := range strings.Split(string(BaseCSS), "\n") {
		if hex.MatchString(line) {
			t.Errorf("base.css:%d declares a raw colour: %s", i+1, strings.TrimSpace(line))
		}
	}
}

func TestBundledThemesAreUsable(t *testing.T) {
	themes, err := BundledThemes()
	if err != nil {
		t.Fatalf("BundledThemes: %v", err)
	}
	if len(themes) == 0 {
		t.Fatal("no themes are bundled")
	}

	for _, bundled := range themes {
		for _, colour := range bundled.Package.ColorNames() {
			t.Run(bundled.Name+"/"+colour, func(t *testing.T) {
				tokens, err := FromDTCG(bundled.Package.Colors[colour])
				if err != nil {
					t.Fatalf("does not parse: %v", err)
				}

				merged := Merge(DefaultTokens(), tokens)
				// A theme we ship should not be the one warning about contrast.
				for _, w := range CheckContrast(merged) {
					t.Errorf("%s %s is %.2f, want at least %.1f", w.Mode, w.Pair, w.Ratio, w.Min)
				}
				// A bundled colourway is expected to be complete, not partial.
				for _, token := range merged {
					if _, ok := tokenByName(tokens, token.Name); !ok {
						t.Errorf("does not declare %q, so it inherits the default", token.Name)
					}
				}
			})
		}
	}
}
