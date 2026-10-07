package frontend

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

// sourceLanguage is the catalogue every other one is measured against.
const sourceLanguage = "en"

// lengthBudget is how much longer than the English source a translation may
// be before it risks breaking the layout.
//
// German runs long by nature, so this is a real constraint rather than a
// formality: a nav item or a button that doubles in length wraps, and the
// design stops working. Longer prose is given more room than short UI strings,
// which have nowhere to go.
const (
	uiStringLimit  = 20 // characters, below which a string is layout-critical
	uiBudgetFactor = 1.6
	proseBudget    = 1.35
)

func catalogues(t *testing.T) map[string]map[string]string {
	t.Helper()

	entries, err := fs.Glob(assets, "locales/*.toml")
	if err != nil {
		t.Fatalf("glob locales: %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("found %d catalogues, expected several", len(entries))
	}

	out := make(map[string]map[string]string, len(entries))
	for _, entry := range entries {
		raw, err := fs.ReadFile(assets, entry)
		if err != nil {
			t.Fatalf("read %s: %v", entry, err)
		}

		messages := map[string]string{}
		if err := toml.Unmarshal(raw, &messages); err != nil {
			t.Fatalf("parse %s: %v", entry, err)
		}

		parts := strings.Split(strings.TrimSuffix(path.Base(entry), ".toml"), ".")
		out[parts[len(parts)-1]] = messages
	}
	return out
}

func TestEveryCatalogueHasEveryKey(t *testing.T) {
	all := catalogues(t)
	source, ok := all[sourceLanguage]
	if !ok {
		t.Fatalf("no %s catalogue", sourceLanguage)
	}

	for lang, messages := range all {
		if lang == sourceLanguage {
			continue
		}
		for key := range source {
			if _, ok := messages[key]; !ok {
				t.Errorf("%s is missing %q", lang, key)
			}
		}
		for key := range messages {
			if _, ok := source[key]; !ok {
				t.Errorf("%s has %q, which %s does not", lang, key, sourceLanguage)
			}
		}
	}
}

func TestNoTranslationIsEmpty(t *testing.T) {
	for lang, messages := range catalogues(t) {
		for key, value := range messages {
			if strings.TrimSpace(value) == "" {
				t.Errorf("%s: %q is empty", lang, key)
			}
		}
	}
}

func TestTranslationsRespectTheLengthBudget(t *testing.T) {
	all := catalogues(t)
	source := all[sourceLanguage]

	for lang, messages := range all {
		if lang == sourceLanguage {
			continue
		}
		for key, translated := range messages {
			original, ok := source[key]
			if !ok {
				continue
			}

			originalLen := utf8.RuneCountInString(original)
			translatedLen := utf8.RuneCountInString(translated)

			budget := proseBudget
			if originalLen <= uiStringLimit {
				// Short strings are labels and buttons: they have nowhere to
				// wrap to, so they get a looser factor but a small absolute
				// allowance.
				budget = uiBudgetFactor
			}

			limit := int(float64(originalLen) * budget)
			if originalLen <= uiStringLimit && limit < originalLen+8 {
				limit = originalLen + 8
			}
			if translatedLen > limit {
				t.Errorf("%s: %q is %d characters, budget is %d (English is %d)\n  en: %s\n  %s: %s",
					lang, key, translatedLen, limit, originalLen, original, lang, translated)
			}
		}
	}
}

func TestSupportedLanguagesAreLoaded(t *testing.T) {
	all := catalogues(t)
	for _, lang := range []string{"en", "fr", "de"} {
		if _, ok := all[lang]; !ok {
			t.Errorf("no %s catalogue", lang)
		}
	}
}

// TestEveryKeyTheSiteAsksForIsDefined.
//
// The catalogues are checked against each other above; this checks them
// against the templates and the handlers, which is the drift that actually
// happens. A missing key renders as an empty string — no error, no log — so the
// only symptom is a blank label somebody has to notice on a page they may not
// visit. Two were already missing when this test was written.
func TestEveryKeyTheSiteAsksForIsDefined(t *testing.T) {
	defined := catalogues(t)[sourceLanguage]

	// A key built from a value at render time cannot be checked by reading the
	// source, so these families are asserted whole instead: every value the
	// templates can produce is one of a fixed small set.
	dynamic := map[string][]string{
		"weekday.%d":  {"0", "1", "2", "3", "4", "5", "6"},
		"month.%d":    {"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12"},
		"position.%d": {"1", "2", "3", "4", "-1"},
		"role.%s":     {"admin", "host"},
	}
	for pattern, values := range dynamic {
		prefix := strings.SplitN(pattern, ".", 2)[0]
		for _, value := range values {
			key := prefix + "." + value
			if _, ok := defined[key]; !ok {
				t.Errorf("%s is asked for at render time and is not defined", key)
			}
		}
	}

	// Every literal key in a template or a handler.
	literal := regexp.MustCompile(`\.T[( ]"([a-z_]+\.[a-z_0-9]+)"`)
	navKey := regexp.MustCompile(`"Key" +"([a-z_]+\.[a-z_0-9]+)"`)
	goKey := regexp.MustCompile(`data\.T\("([a-z_]+\.[a-z_0-9]+)"\)`)

	sources, err := fs.Glob(assets, "templates/*/*.html")
	if err != nil {
		t.Fatalf("glob templates: %v", err)
	}
	local, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob handlers: %v", err)
	}

	asked := map[string][]string{}
	for _, name := range sources {
		raw, err := fs.ReadFile(assets, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		collect(asked, name, string(raw), literal, navKey)
	}
	for _, name := range local {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		collect(asked, name, string(raw), literal, navKey, goKey)
	}

	for key, where := range asked {
		if _, ok := defined[key]; !ok {
			t.Errorf("%q is asked for in %s and is not defined", key, strings.Join(where, ", "))
		}
	}
}

func collect(into map[string][]string, name, body string, patterns ...*regexp.Regexp) {
	for _, pattern := range patterns {
		for _, match := range pattern.FindAllStringSubmatch(body, -1) {
			into[match[1]] = append(into[match[1]], name)
		}
	}
}
