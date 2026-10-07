package theme

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Dimension is one structural token: a radius, a spacing step, a font stack.
//
// Structure is kept apart from colour because the two vary along different
// axes. A palette has a light value and a dark one; a corner radius has one
// value in both. Separating them also lets a designer work on either without
// touching the other, and lets an operator mix any palette with any structure.
type Dimension struct {
	// Name is the custom property without the leading "--".
	Name string `json:"name"`

	// Type is the DTCG token type: dimension, fontFamily, number.
	Type string `json:"type"`

	// Value is the CSS value, already rendered.
	Value string `json:"value"`
}

// structureBlockRe finds the second :root block in tokens.css — the one after
// the palette, which holds the structural tokens.
var structureBlockRe = regexp.MustCompile(`(?s):root\s*\{[^}]*\}.*?:root\s*\{([^}]*)\}\s*$`)

// DefaultStructure parses the structural block of tokens.css.
//
// As with the palette, the CSS is the single source of truth: the Go side
// holds no values of its own.
func DefaultStructure() []Dimension {
	block := firstMatch(structureBlockRe, string(TokensCSS))
	order, values := parseVarBlock(block)

	out := make([]Dimension, 0, len(order))
	for _, name := range order {
		out = append(out, Dimension{
			Name:  name,
			Type:  inferType(name, values[name]),
			Value: values[name],
		})
	}
	return out
}

// inferType guesses the DTCG type of a value, so an export carries something
// a design tool can read rather than a bare string.
func inferType(name, value string) string {
	switch {
	case strings.HasPrefix(name, "font-") && !strings.Contains(name, "size"):
		return "fontFamily"
	case strings.ContainsAny(value, "0123456789") && !strings.ContainsAny(value, "abcdefghijklmnopqrstuvwxyz%"):
		return "number"
	default:
		return "dimension"
	}
}

// MergeStructure layers an override set onto a base, by name. A structure file
// that declares only a radius changes only that.
func MergeStructure(base, override []Dimension) []Dimension {
	byName := make(map[string]Dimension, len(override))
	for _, d := range override {
		byName[d.Name] = d
	}

	out := make([]Dimension, 0, len(base))
	for _, d := range base {
		if o, ok := byName[d.Name]; ok {
			if v := strings.TrimSpace(o.Value); v != "" {
				d.Value = v
			}
			delete(byName, d.Name)
		}
		out = append(out, d)
	}
	for _, d := range override {
		if _, unused := byName[d.Name]; unused && d.Name != "" {
			out = append(out, d)
			delete(byName, d.Name)
		}
	}
	return out
}

// RenderStructureCSS renders structural tokens into a single :root block.
//
// One block, not three: structure does not vary by theme state, so emitting
// dark variants would be noise that could drift out of step with the light
// ones.
func RenderStructureCSS(dimensions []Dimension) []byte {
	var b bytes.Buffer
	b.WriteString("/* Doléances structure overlay — overrides internal/theme/tokens.css. */\n")
	b.WriteString(":root {")
	for _, d := range dimensions {
		if value := strings.TrimSpace(d.Value); value != "" && d.Name != "" {
			fmt.Fprintf(&b, " --%s: %s;", d.Name, value)
		}
	}
	b.WriteString(" }\n")
	return b.Bytes()
}

// --- DTCG for structure ---
//
// Colour uses two top-level groups for its light and dark modes. Structure has
// no modes, so it groups by type instead — which is the plain DTCG shape, and
// what a design tool expects for dimensions and font stacks.

type dtcgToken struct {
	Type  string          `json:"$type"`
	Value json.RawMessage `json:"$value"`
}

// StructureToDTCG serializes structural tokens, grouped by type.
func StructureToDTCG(dimensions []Dimension) []byte {
	groups := map[string][]Dimension{}
	var order []string
	for _, d := range dimensions {
		if _, seen := groups[d.Type]; !seen {
			order = append(order, d.Type)
		}
		groups[d.Type] = append(groups[d.Type], d)
	}
	sort.Strings(order)

	var b bytes.Buffer
	b.WriteString("{\n")
	for i, group := range order {
		fmt.Fprintf(&b, "  %q: {\n", group)
		members := groups[group]
		for j, d := range members {
			value, _ := json.Marshal(encodeValue(d))
			comma := ","
			if j == len(members)-1 {
				comma = ""
			}
			fmt.Fprintf(&b, "    %q: {\"$type\":%q,\"$value\":%s}%s\n", d.Name, d.Type, value, comma)
		}
		if i == len(order)-1 {
			b.WriteString("  }\n")
			continue
		}
		b.WriteString("  },\n")
	}
	b.WriteString("}\n")
	return b.Bytes()
}

// encodeValue gives each type the JSON shape DTCG expects: a font stack is an
// array, a number is a number, everything else is a string.
func encodeValue(d Dimension) any {
	switch d.Type {
	case "fontFamily":
		parts := strings.Split(d.Value, ",")
		stack := make([]string, 0, len(parts))
		for _, part := range parts {
			stack = append(stack, strings.Trim(strings.TrimSpace(part), `"`))
		}
		return stack
	case "number":
		number := json.Number(strings.TrimSpace(d.Value))
		return number
	default:
		return d.Value
	}
}

// StructureFromDTCG parses a structural token document.
//
// Tolerant in the same way as the colour parser: a file declaring one token
// changes one token, and MergeStructure fills the rest from the defaults.
func StructureFromDTCG(data []byte) ([]Dimension, error) {
	var doc map[string]map[string]dtcgToken
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("invalid DTCG JSON: %w", err)
	}

	var groups []string
	for group := range doc {
		groups = append(groups, group)
	}
	sort.Strings(groups)

	var out []Dimension
	for _, group := range groups {
		var names []string
		for name := range doc[group] {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			token := doc[group][name]
			value, err := decodeValue(token)
			if err != nil {
				return nil, fmt.Errorf("token %q: %w", name, err)
			}
			out = append(out, Dimension{Name: name, Type: token.Type, Value: value})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("DTCG document declares no structural tokens")
	}
	return out, nil
}

// decodeValue turns a DTCG value back into the CSS it represents.
func decodeValue(token dtcgToken) (string, error) {
	// A font stack arrives as an array and has to be rejoined — with the
	// quotes CSS requires around any family name containing a space, which
	// the array form drops.
	var stack []string
	if err := json.Unmarshal(token.Value, &stack); err == nil {
		quoted := make([]string, 0, len(stack))
		for _, family := range stack {
			if strings.ContainsAny(family, " \t") {
				family = `"` + family + `"`
			}
			quoted = append(quoted, family)
		}
		return strings.Join(quoted, ", "), nil
	}

	var text string
	if err := json.Unmarshal(token.Value, &text); err == nil {
		return text, nil
	}

	var number json.Number
	if err := json.Unmarshal(token.Value, &number); err == nil {
		return number.String(), nil
	}
	return "", fmt.Errorf("value is neither a string, a number, nor a list")
}

// StructureSample picks the few tokens worth previewing in a library listing,
// the way swatches preview a palette.
func StructureSample(dimensions []Dimension) map[string]string {
	wanted := map[string]bool{"radius": true, "space-md": true, "font-ui": true, "measure": true}

	out := map[string]string{}
	for _, d := range dimensions {
		if wanted[d.Name] {
			out[d.Name] = d.Value
		}
	}
	return out
}
