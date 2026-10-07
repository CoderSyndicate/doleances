package console

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// The console's catalogues are held to the same rules as the frontend's: every
// language carries every key, and a translation stays close enough to the
// English length that the layout survives it.
const (
	sourceLanguage = "en"
	uiStringLimit  = 20
	uiBudgetFactor = 1.6
	proseBudget    = 1.35
)

func catalogues(t *testing.T) map[string]map[string]string {
	t.Helper()

	entries, err := fs.Glob(assets, "locales/*.toml")
	if err != nil {
		t.Fatalf("glob locales: %v", err)
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
	source := all[sourceLanguage]

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

			budget := proseBudget
			if len([]rune(original)) <= uiStringLimit {
				budget = uiBudgetFactor
			}
			limit := int(float64(len([]rune(original))) * budget)
			if len([]rune(original)) <= uiStringLimit && limit < len([]rune(original))+8 {
				limit = len([]rune(original)) + 8
			}

			if len([]rune(translated)) > limit {
				t.Errorf("%s: %q is %d characters, budget is %d\n  en: %s\n  %s: %s",
					lang, key, len([]rune(translated)), limit, original, lang, translated)
			}
		}
	}
}

// TestEveryKeyTheConsoleAsksForIsDefined.
//
// The catalogues are checked against each other above; this checks them against
// the templates and the handlers, which is the drift that actually happens. A
// missing key renders as the key itself — no error, no log — so the only
// symptom is a column heading reading `MANAGE.CONTACT_NAME` on a page somebody
// has to visit to notice.
//
// That is not hypothetical: two of them had been on the group listing since it
// was written, and the person who found them was the operator using it.
func TestEveryKeyTheConsoleAsksForIsDefined(t *testing.T) {
	defined := catalogues(t)[sourceLanguage]

	// A template asks for a key with `.T`; the nav partial asks with a "Key"
	// argument; a handler asks in Go. The scripts never ask directly — every
	// string they use is rendered into the page by a template — so scanning
	// the templates covers them too.
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\.T[( ]"([a-z_]+\.[a-z_0-9]+)"`),
		regexp.MustCompile(`"Key" +"([a-z_]+\.[a-z_0-9]+)"`),
		regexp.MustCompile(`\.T\("([a-z_]+\.[a-z_0-9]+)"\)`),
	}

	templates, err := fs.Glob(assets, "templates/*/*.html")
	if err != nil {
		t.Fatalf("glob templates: %v", err)
	}
	handlers, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob handlers: %v", err)
	}
	if len(templates) == 0 {
		t.Fatal("no templates were scanned, so this test proves nothing")
	}

	asked := map[string][]string{}
	collect := func(name, body string) {
		for _, pattern := range patterns {
			for _, match := range pattern.FindAllStringSubmatch(body, -1) {
				asked[match[1]] = append(asked[match[1]], name)
			}
		}
	}

	for _, name := range templates {
		raw, err := fs.ReadFile(assets, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		collect(name, string(raw))
	}
	for _, name := range handlers {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		collect(name, string(raw))
	}
	if len(asked) == 0 {
		t.Fatal("no keys were found at all, so this test proves nothing")
	}

	for key, where := range asked {
		if _, ok := defined[key]; !ok {
			t.Errorf("%q is asked for in %s and is not defined", key, strings.Join(where, ", "))
		}
	}
}
