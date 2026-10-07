package theme

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Token is one colour of the design system, in its light and dark values.
//
// The token set is the serializable palette: tokens.css seeds the defaults,
// and an uploaded DTCG document maps onto the same names.
type Token struct {
	// Name is the custom property without the leading "--".
	Name  string `json:"name"`
	Light string `json:"light"`
	Dark  string `json:"dark"`
}

var (
	rootLightRe = regexp.MustCompile(`(?s):root\s*\{(.*?)\}`)
	rootDarkRe  = regexp.MustCompile(`(?s):root\[data-theme="dark"\]\s*\{(.*?)\}`)
	varDeclRe   = regexp.MustCompile(`--([A-Za-z0-9\-]+)\s*:\s*([^;]+);`)
)

// DefaultTokens parses tokens.css into the ordered token set: light from the
// bare :root block, dark from the explicit one.
//
// Parsing rather than restating keeps tokens.css the single source of truth —
// the Go side never holds a colour value of its own.
func DefaultTokens() []Token {
	css := string(TokensCSS)
	order, light := parseVarBlock(firstMatch(rootLightRe, css))
	_, dark := parseVarBlock(firstMatch(rootDarkRe, css))

	out := make([]Token, 0, len(order))
	for _, name := range order {
		out = append(out, Token{Name: name, Light: light[name], Dark: dark[name]})
	}
	return out
}

func firstMatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// parseVarBlock returns the custom-property names in declaration order plus a
// lookup. Declarations that are not custom properties — color-scheme, say —
// simply do not match.
func parseVarBlock(block string) ([]string, map[string]string) {
	var order []string
	values := map[string]string{}

	for _, decl := range varDeclRe.FindAllStringSubmatch(block, -1) {
		name, value := decl[1], strings.TrimSpace(decl[2])
		if _, seen := values[name]; !seen {
			order = append(order, name)
		}
		values[name] = value
	}
	return order, values
}

// Merge layers an override token set onto a base, by name.
//
// A theme file that declares only a few tokens changes only those: everything
// it leaves out keeps the built-in value, which is what makes a partial theme
// safe to upload.
func Merge(base, override []Token) []Token {
	byName := make(map[string]Token, len(override))
	for _, t := range override {
		byName[t.Name] = t
	}

	out := make([]Token, 0, len(base))
	for _, t := range base {
		if o, ok := byName[t.Name]; ok {
			if v := strings.TrimSpace(o.Light); v != "" {
				t.Light = v
			}
			if v := strings.TrimSpace(o.Dark); v != "" {
				t.Dark = v
			}
			delete(byName, t.Name)
		}
		out = append(out, t)
	}

	// Tokens the theme introduces that the defaults do not have, in the order
	// the theme declared them.
	for _, t := range override {
		if _, unused := byName[t.Name]; unused && t.Name != "" {
			out = append(out, t)
			delete(byName, t.Name)
		}
	}
	return out
}

// RenderOverrideCSS renders a token set as the same three theme-state blocks
// tokens.css uses.
//
// Loaded after tokens.css, these win on order at equal specificity, so they
// override what they declare and let everything else fall through to the
// built-in defaults.
func RenderOverrideCSS(tokens []Token) []byte {
	var b bytes.Buffer
	b.WriteString("/* Doléances theme overlay — overrides internal/theme/tokens.css. */\n")

	block := func(selector string, pick func(Token) string) {
		b.WriteString(selector)
		b.WriteString(" {")
		for _, t := range tokens {
			if value := strings.TrimSpace(pick(t)); value != "" && t.Name != "" {
				fmt.Fprintf(&b, " --%s: %s;", t.Name, value)
			}
		}
		b.WriteString(" }\n")
	}

	block(":root", func(t Token) string { return t.Light })
	b.WriteString("@media (prefers-color-scheme: dark) {\n  ")
	block(`:root:not([data-theme="light"])`, func(t Token) string { return t.Dark })
	b.WriteString("}\n")
	block(`:root[data-theme="dark"]`, func(t Token) string { return t.Dark })

	return b.Bytes()
}

// Swatches picks the few tokens worth previewing in a list of themes.
func Swatches(tokens []Token) map[string]string {
	wanted := map[string]bool{"bg": true, "surface": true, "accent": true, "text": true}

	out := map[string]string{}
	for _, t := range tokens {
		if wanted[t.Name] {
			out[t.Name] = t.Light
		}
	}
	return out
}

// --- W3C Design Tokens (DTCG) round-trip ---
//
// Two colour groups, "light" and "dark", each mapping a token name to
// {"$type":"color","$value":"#hex"}. The DTCG core spec has no standard for
// modes yet; two top-level groups in one document is the de-facto pattern that
// Figma and Tokens Studio already produce, so one file carries both modes and
// export → edit → import round-trips exactly.

type dtcgColor struct {
	Type  string `json:"$type"`
	Value string `json:"$value"`
}

// ToDTCG serializes tokens to a DTCG document, preserving token order so the
// output is stable between downloads.
func ToDTCG(tokens []Token) []byte {
	var b bytes.Buffer
	b.WriteString("{\n")

	group := func(name string, pick func(Token) string, last bool) {
		fmt.Fprintf(&b, "  %q: {\n", name)
		for i, t := range tokens {
			encoded, _ := json.Marshal(dtcgColor{Type: "color", Value: pick(t)})
			separator := ","
			if i == len(tokens)-1 {
				separator = ""
			}
			fmt.Fprintf(&b, "    %q: %s%s\n", t.Name, encoded, separator)
		}
		if last {
			b.WriteString("  }\n")
			return
		}
		b.WriteString("  },\n")
	}

	group("light", func(t Token) string { return t.Light }, false)
	group("dark", func(t Token) string { return t.Dark }, true)
	b.WriteString("}\n")
	return b.Bytes()
}

// FromDTCG parses a DTCG document back into tokens.
//
// It is deliberately tolerant: a token present in only one mode keeps the
// other side empty, and Merge then fills it from the defaults. Only a document
// that is unparseable, or that names no colour at all, is an error.
func FromDTCG(data []byte) ([]Token, error) {
	var doc map[string]map[string]dtcgColor
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("invalid DTCG JSON: %w", err)
	}

	light, dark := doc["light"], doc["dark"]
	seen := map[string]bool{}
	var order []string
	for _, group := range []map[string]dtcgColor{light, dark} {
		for name := range group {
			if !seen[name] {
				seen[name] = true
				order = append(order, name)
			}
		}
	}
	if len(order) == 0 {
		return nil, fmt.Errorf(`DTCG document declares no colour tokens (expected "light" and "dark" groups)`)
	}

	// Map iteration is unordered, so sort against the default order to keep
	// downloads stable and diffs readable.
	order = inDefaultOrder(order)

	out := make([]Token, 0, len(order))
	for _, name := range order {
		out = append(out, Token{Name: name, Light: light[name].Value, Dark: dark[name].Value})
	}
	return out, nil
}

// inDefaultOrder sorts names into the built-in token order, with anything
// unrecognised appended alphabetically after it.
func inDefaultOrder(names []string) []string {
	rank := map[string]int{}
	for i, t := range DefaultTokens() {
		rank[t.Name] = i
	}

	known := make([]string, 0, len(names))
	var unknown []string
	for _, name := range names {
		if _, ok := rank[name]; ok {
			known = append(known, name)
			continue
		}
		unknown = append(unknown, name)
	}

	sortByRank(known, rank)
	sortStrings(unknown)
	return append(known, unknown...)
}

func sortByRank(names []string, rank map[string]int) {
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && rank[names[j]] < rank[names[j-1]]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
}

func sortStrings(names []string) {
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
}
