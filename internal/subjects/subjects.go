// Package subjects turns what a model called a thing into what the register
// calls it.
//
// The classifier proposes subjects in its own words, in the language the
// doléance was written in. A fixed vocabulary would be tidier and would also
// decide in advance what people are allowed to be aggrieved about, which is not
// a decision this project should make. The cost is that the same subject
// arrives spelled several ways, in several languages, and something has to
// resolve them — otherwise the Register's filter list becomes a pile rather
// than a list.
//
// Resolution is layered and exits on the first match, cheapest first:
//
//  1. the match key, an indexed lookup
//  2. plural folding, a second lookup
//  3. embedding similarity, which costs a call
//
// This package holds the first two and the arithmetic for the third. The
// lookups and the call belong to their own layers.
package subjects

import (
	"fmt"
	"math"
	"strings"
	"unicode"
)

// MaxWords is how long a subject label may be.
//
// Three words is a subject; a sentence is an opinion about one. The cap is
// enforced on the server so a chatty model cannot turn the filter list into
// prose, whatever the prompt asked for.
const MaxWords = 3

// MaxLabelRunes bounds a single word too, so "antidisestablishmentarianism"
// passes and a paragraph with no spaces does not.
const MaxLabelRunes = 64

// Validate reports whether a proposed label may become a subject.
func Validate(label string) error {
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		return fmt.Errorf("subjects: empty label")
	}
	if len([]rune(trimmed)) > MaxLabelRunes {
		return fmt.Errorf("subjects: %q is longer than %d characters", trimmed, MaxLabelRunes)
	}
	if words := len(strings.Fields(trimmed)); words > MaxWords {
		return fmt.Errorf("subjects: %q is %d words, at most %d are allowed",
			trimmed, words, MaxWords)
	}
	return nil
}

// MatchKey renders a label in the form two spellings of the same thing share.
//
// A port of the normaliser in Sophia, with its warning intact: this
// is the **tolerant matching** key and must never be used to generate an
// identifier. The two jobs pull in opposite directions — matching wants to
// collapse differences, identity wants to preserve them — and a normaliser
// doing both ends up doing neither.
func MatchKey(label string) string {
	s := strings.ToLower(strings.TrimSpace(label))
	s = apostrophes.Replace(s)
	// Ligatures before accents: decomposition does not split œ, so folding
	// accents first would leave it behind.
	s = ligatures.Replace(s)
	s = foldAccents(s)
	s = strings.Join(strings.Fields(s), " ")
	return strings.Trim(s, " \t\"'.,;:!?()[]{}«»")
}

// apostrophes unifies the several characters people and models use for one.
var apostrophes = strings.NewReplacer(
	"’", "'", // right single quotation mark
	"ʼ", "'", // modifier letter apostrophe
	"‘", "'",
	"`", "'",
	"´", "'",
)

// ligatures are spelled out, because accent folding will not split them.
var ligatures = strings.NewReplacer(
	"œ", "oe", "Œ", "oe",
	"æ", "ae", "Æ", "ae",
	"ß", "ss",
)

// accentTable is explicit rather than a Unicode decomposition, so the
// behaviour is readable and the package keeps to the standard library.
var accentTable = map[rune]rune{
	'à': 'a', 'á': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'å': 'a', 'ā': 'a', 'ă': 'a',
	'è': 'e', 'é': 'e', 'ê': 'e', 'ë': 'e', 'ē': 'e', 'ĕ': 'e', 'ę': 'e', 'ė': 'e',
	'ì': 'i', 'í': 'i', 'î': 'i', 'ï': 'i', 'ī': 'i', 'į': 'i',
	'ò': 'o', 'ó': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o', 'ø': 'o', 'ō': 'o',
	'ù': 'u', 'ú': 'u', 'û': 'u', 'ü': 'u', 'ū': 'u', 'ů': 'u',
	'ý': 'y', 'ÿ': 'y',
	'ñ': 'n', 'ń': 'n',
	'ç': 'c', 'ć': 'c', 'č': 'c',
	'š': 's', 'ś': 's',
	'ž': 'z', 'ź': 'z', 'ż': 'z',
	'ł': 'l', 'đ': 'd', 'ř': 'r', 'ť': 't', 'ň': 'n',
}

func foldAccents(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	for _, r := range s {
		if folded, ok := accentTable[r]; ok {
			b.WriteRune(folded)
			continue
		}
		// Anything that is not a letter, digit, space or hyphen is dropped:
		// punctuation inside a label is noise for matching.
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) || r == '-' || r == '\'' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// FoldPlural is layer two: a second key with a trailing plural marker removed.
//
// It is deliberately narrow, and deliberately **not** edit distance. At these
// lengths Levenshtein merges real words — in French "paix" and "pays" are two
// edits apart, "impôt" and "impôts" one — and a wrongly merged subject is a
// filter that lies about what people wrote. Stripping a final s or x is
// predictable; fuzzy matching is where merges nobody can explain come from.
//
// German plurals are not handled: they are irregular enough that any rule
// simple enough to trust would catch almost nothing. Those pairs fall through
// to the embedding layer, which is where they belong.
func FoldPlural(matchKey string) string {
	words := strings.Fields(matchKey)
	for i, word := range words {
		words[i] = singular(word)
	}
	return strings.Join(words, " ")
}

// singularExceptions are words that end in s or x while already being
// singular. No rule distinguishes them from a plural — "procès" and "impôts"
// look identical to one — so they are named. The list is short on purpose: it
// holds the words a register of grievances actually uses, and anything missing
// falls through to the embedding layer rather than being merged wrongly.
var singularExceptions = map[string]bool{
	// French singulars ending in -s or -x
	"pays": true, "temps": true, "corps": true, "proces": true, "succes": true,
	"acces": true, "congres": true, "univers": true, "cours": true, "fois": true,
	"prix": true, "choix": true, "croix": true, "voix": true, "poids": true,
	"progres": true, "repas": true, "pas": true, "mois": true, "bois": true,
	"dos": true, "os": true, "bras": true, "cas": true, "gaz": true,
	"sens": true, "fils": true, "puits": true, "taux": true, "flux": true,
	// English and borrowed
	"bus": true, "virus": true, "campus": true, "consensus": true, "news": true,
	"series": true, "species": true, "status": true, "census": true,
}

func singular(word string) string {
	if singularExceptions[word] {
		return word
	}

	runes := []rune(word)
	if len(runes) < 4 {
		// Too short to lose a letter safely: "bus" and "bu" are not the same
		// word, and neither are "gaz" and "ga".
		return word
	}

	switch runes[len(runes)-1] {
	case 's', 'x':
		// A word already ending in a double s keeps it: "process", "business".
		if runes[len(runes)-2] == 's' {
			return word
		}
		return string(runes[:len(runes)-1])
	}
	return word
}

// Cosine is the similarity between two embeddings, from -1 to 1.
//
// Layer three compares a proposed label against the vectors already stored. It
// is computed here rather than in the database because the engine-neutral rule
// forbids pgvector, and because a thousand subjects at 1024 dimensions is four
// megabytes — small enough to hold and compare in memory without ceremony.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dot, normA, normB float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		normA += x * x
		normB += y * y
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// Slug renders a label as a URL-safe identifier.
//
// Distinct from MatchKey and used for a different purpose: this one appears in
// query strings and has to stay stable for a subject's whole life, while the
// match key exists only to find near-spellings and may be recomputed at will.
func Slug(label string) string {
	key := MatchKey(label)

	var b strings.Builder
	b.Grow(len(key))
	for _, r := range key {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '\'':
			b.WriteRune('-')
		}
	}

	slug := strings.Trim(b.String(), "-")
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	return slug
}
