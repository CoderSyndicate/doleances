package web

import (
	"encoding/xml"
	"io"
	"io/fs"
	"strings"
	"testing"
)

// TestEveryFlagIsWellFormed, for the reason the theme package's assets are
// checked: a malformed SVG is served as 200 OK with the right content type and
// the right size, and the only symptom is a placeholder somebody has to look
// at a page to see. The flags carry comments, which is where that goes wrong —
// a pair of hyphens cannot appear inside an XML comment.
func TestEveryFlagIsWellFormed(t *testing.T) {
	files, err := fs.Glob(flagFiles, "flags/*.svg")
	if err != nil {
		t.Fatalf("glob the flags: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no flags were found, so this test proves nothing")
	}

	for _, name := range files {
		raw, err := flagFiles.ReadFile(name)
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
				t.Errorf("%s is not well-formed XML: %v", name, err)
				break
			}
		}
	}
}

// TestEveryInterfaceLanguageHasAFlagAndAName.
//
// The selector shows no text, so a language with no flag is a control with
// nothing in it, and one with no endonym is a control a screen reader cannot
// announce. Both are added by hand and neither fails loudly.
func TestEveryInterfaceLanguageHasAFlagAndAName(t *testing.T) {
	for _, code := range []string{"de", "en", "fr"} {
		if !HasFlag(code) {
			t.Errorf("%s has no flag, so its entry in the selector would be empty", code)
		}
		if name := LanguageName(code); name == strings.ToUpper(code) {
			t.Errorf("%s has no endonym, so the selector announces it as a language tag", code)
		}
	}
}
