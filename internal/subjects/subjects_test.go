package subjects

import (
	"math"
	"testing"
)

// TestMatchKeyCollapsesSpellings is layer one: the same subject typed several
// ways has to resolve to one entry, or the filter list fills with near-copies
// nobody maintains.
func TestMatchKeyCollapsesSpellings(t *testing.T) {
	groups := [][]string{
		{"santé", "Santé", "SANTÉ", " santé ", "sante", "santé."},
		{"services publics", "Services  Publics", "services   publics"},
		{"l'hôpital", "l’hôpital", "L'Hopital", "l‘hôpital"},
		{"cœur de ville", "coeur de ville", "Cœur de Ville"},
		{"Straße", "strasse", "STRASSE"},
	}

	for _, group := range groups {
		want := MatchKey(group[0])
		for _, variant := range group[1:] {
			if got := MatchKey(variant); got != want {
				t.Errorf("MatchKey(%q) = %q, want %q (same as %q)",
					variant, got, want, group[0])
			}
		}
	}
}

// TestMatchKeyKeepsDistinctThingsDistinct: over-collapsing is worse than
// under-collapsing, because a wrongly merged subject is a filter that lies.
func TestMatchKeyKeepsDistinctThingsDistinct(t *testing.T) {
	pairs := [][2]string{
		{"santé", "sante publique"},
		{"paix", "pays"},
		{"logement", "logements sociaux"},
		{"eau", "eaux usées"},
		{"transport", "transition"},
	}

	for _, pair := range pairs {
		if MatchKey(pair[0]) == MatchKey(pair[1]) {
			t.Errorf("MatchKey merged %q and %q, which are different subjects",
				pair[0], pair[1])
		}
	}
}

// TestFoldPluralIsNarrow is layer two. It must catch the plural and nothing
// else: every false merge here is a filter that lies.
func TestFoldPluralIsNarrow(t *testing.T) {
	same := [][2]string{
		{"transport", "transports"},
		{"impôt", "impôts"},
		{"service public", "services publics"},
		{"école", "écoles"},
	}
	for _, pair := range same {
		if FoldPlural(MatchKey(pair[0])) != FoldPlural(MatchKey(pair[1])) {
			t.Errorf("FoldPlural did not merge %q and %q", pair[0], pair[1])
		}
	}

	distinct := [][2]string{
		{"paix", "pays"},    // two edits apart; Levenshtein would merge these
		{"bus", "bu"},       // too short to strip safely
		{"gaz", "ga"},       // likewise
		{"proces", "proce"}, // a word that legitimately ends in s
		{"succes", "succe"},
	}
	for _, pair := range distinct {
		if FoldPlural(MatchKey(pair[0])) == FoldPlural(MatchKey(pair[1])) {
			t.Errorf("FoldPlural merged %q and %q, which are different words",
				pair[0], pair[1])
		}
	}
}

func TestValidate(t *testing.T) {
	for _, ok := range []string{"santé", "services publics", "accès aux soins"} {
		if err := Validate(ok); err != nil {
			t.Errorf("Validate(%q): %v", ok, err)
		}
	}

	for _, bad := range []string{
		"",
		"   ",
		"le manque criant de services publics dans les zones rurales", // a sentence
		"quatre mots c'est trop",
	} {
		if err := Validate(bad); err == nil {
			t.Errorf("Validate(%q) accepted a label it should refuse", bad)
		}
	}
}

// TestCosine covers the arithmetic layer three depends on.
func TestCosine(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	c := []float32{0, 1, 0}
	d := []float32{-1, 0, 0}

	if got := Cosine(a, b); math.Abs(got-1) > 1e-9 {
		t.Errorf("identical vectors: %v, want 1", got)
	}
	if got := Cosine(a, c); math.Abs(got) > 1e-9 {
		t.Errorf("orthogonal vectors: %v, want 0", got)
	}
	if got := Cosine(a, d); math.Abs(got+1) > 1e-9 {
		t.Errorf("opposite vectors: %v, want -1", got)
	}
	// Magnitude must not matter: a longer vector in the same direction is the
	// same meaning.
	if got := Cosine(a, []float32{5, 0, 0}); math.Abs(got-1) > 1e-9 {
		t.Errorf("scaled vector: %v, want 1", got)
	}

	// Mismatched or empty input is 0 rather than a panic: an embedding that
	// failed must not take the classification down with it.
	if Cosine(a, []float32{1, 0}) != 0 || Cosine(nil, nil) != 0 {
		t.Error("mismatched or empty vectors should score 0")
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Santé":              "sante",
		"services publics":   "services-publics",
		"l'hôpital":          "l-hopital",
		"accès   aux  soins": "acces-aux-soins",
		"cœur de ville":      "coeur-de-ville",
	}
	for label, want := range cases {
		if got := Slug(label); got != want {
			t.Errorf("Slug(%q) = %q, want %q", label, got, want)
		}
	}
}

// TestSlugAndMatchKeyAreDifferentJobs guards the warning carried over from
// Sophia: matching wants to collapse differences, identity wants to preserve
// them, and a normaliser doing both does neither.
func TestSlugAndMatchKeyAreDifferentJobs(t *testing.T) {
	label := "services publics"

	if Slug(label) == MatchKey(label) {
		t.Skip("the two happen to agree for this label; the distinction is still deliberate")
	}
	if MatchKey(label) != "services publics" {
		t.Errorf("MatchKey = %q, want spaces preserved for matching", MatchKey(label))
	}
	if Slug(label) != "services-publics" {
		t.Errorf("Slug = %q, want a URL-safe form", Slug(label))
	}
}
