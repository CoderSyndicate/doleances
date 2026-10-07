package corpus

import (
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the corpus is empty")
	}
}

func TestEverySnippetCarriesItsProvenance(t *testing.T) {
	// A passage shown without its source is not evidence of anything, and the
	// site quotes people who cannot correct the record.
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for _, entry := range entries {
		if entry.Source.Reference == "" {
			t.Errorf("%s has no reference", entry.ID)
		}
		if entry.Source.Period == "" {
			t.Errorf("%s has no period", entry.ID)
		}
		if entry.Source.Language == "" {
			t.Errorf("%s has no language", entry.ID)
		}
	}
}

func TestTranslationsAreAttachedToTheirOriginal(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for _, entry := range entries {
		for language, translation := range entry.Translations {
			if language == entry.Source.Language {
				t.Errorf("%s: the original language %q is listed as a translation", entry.ID, language)
			}
			if translation.Text == "" || translation.Title == "" {
				t.Errorf("%s: the %s translation is incomplete", entry.ID, language)
			}
			if translation.Text == entry.Text {
				t.Errorf("%s: the %s translation is identical to the original", entry.ID, language)
			}
		}
	}
}

func TestEveryPassageIsTranslatedIntoEverySiteLanguage(t *testing.T) {
	// A passage the site cannot show a reader in their own language is, for
	// them, closed — which is the thing the translations exist to prevent.
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for _, entry := range entries {
		for _, language := range []string{"en", "fr", "de"} {
			if language == entry.Source.Language {
				continue
			}
			if _, ok := entry.Translations[language]; !ok {
				t.Errorf("%s has no %s translation", entry.ID, language)
			}
		}
	}
}

func TestSnippetIDsAreUnique(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	seen := map[string]bool{}
	for _, entry := range entries {
		if seen[entry.ID] {
			t.Errorf("duplicate snippet id %q", entry.ID)
		}
		seen[entry.ID] = true
	}
}

func TestValidateRejectsIncompleteFiles(t *testing.T) {
	good := Snippet{ID: "x", Title: "T", Text: "body"}

	tests := []struct {
		name string
		file File
		want string
	}{
		{"no source title", File{Source: Source{Language: "fr", Reference: "r"}, Snippets: []Snippet{good}}, "no title"},
		{"no language", File{Source: Source{Title: "t", Reference: "r"}, Snippets: []Snippet{good}}, "what language"},
		{"no reference", File{Source: Source{Title: "t", Language: "fr"}, Snippets: []Snippet{good}}, "provenance"},
		{"no snippets", File{Source: Source{Title: "t", Language: "fr", Reference: "r"}}, "no snippets"},
		{"snippet without id", File{
			Source:   Source{Title: "t", Language: "fr", Reference: "r"},
			Snippets: []Snippet{{Title: "T", Text: "b"}},
		}, "no id"},
		{"snippet without text", File{
			Source:   Source{Title: "t", Language: "fr", Reference: "r"},
			Snippets: []Snippet{{ID: "x", Title: "T"}},
		}, "no text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validate("test.json", "fr", tt.file, map[string]string{})
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestValidateRejectsDuplicateIDsAcrossFiles(t *testing.T) {
	seen := map[string]string{"fr:x": "first.json"}
	file := File{
		Source:   Source{Title: "t", Language: "fr", Reference: "r"},
		Snippets: []Snippet{{ID: "x", Title: "T", Text: "b"}},
	}

	err := validate("second.json", "fr", file, seen)
	if err == nil || !strings.Contains(err.Error(), "already used") {
		t.Errorf("error = %v, want a duplicate-id complaint", err)
	}
}

func TestElisionIsMarkedTheSameWay(t *testing.T) {
	// A snippet that joins non-contiguous lines has to say so, and say so
	// consistently: a typographic ellipsis and a bracketed one look alike to a
	// reader but not to a search.
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for _, entry := range entries {
		texts := map[string]string{entry.Source.Language: entry.Text}
		for language, translation := range entry.Translations {
			texts[language] = translation.Text
		}

		for language, text := range texts {
			for _, wrong := range []string{"[…]", "…", "[..]", "(...)"} {
				if strings.Contains(text, wrong) {
					t.Errorf("%s (%s) uses %q; the elision marker is %q",
						entry.ID, language, wrong, ElisionMarker)
				}
			}
		}

		// Every language must elide in the same places, or a translation is
		// quietly claiming continuity the original does not.
		want := strings.Count(entry.Text, ElisionMarker)
		for language, translation := range entry.Translations {
			if got := strings.Count(translation.Text, ElisionMarker); got != want {
				t.Errorf("%s: the %s translation has %d elisions, the original has %d",
					entry.ID, language, got, want)
			}
		}
	}
}
