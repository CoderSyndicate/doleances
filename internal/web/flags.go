package web

import (
	"embed"
	"net/http"
	"strings"
)

// flagFiles are the flags the language selector draws, embedded like every
// other asset so each service still ships as one binary.
//
//go:embed flags/*.svg
var flagFiles embed.FS

// FlagsHandler serves the flags to both web services.
//
// # Shared, because both of them draw the same selector
//
// A copy in each service's own static directory would be two copies of three
// files to keep in step, and the one that drifts is always the console's,
// which fewer people look at.
//
// # Files rather than markup inlined into the page
//
// The project's rule about assets is the first reason: a flag is an image, it
// belongs in a file with an extension, and an editor should colour it. The
// second is particular to SVG — the Union Jack needs a clip path, a clip path
// needs an id, and an id in a document that may draw the same flag twice is a
// duplicate id. Served as its own document, it is nobody else's problem.
func FlagsHandler() (http.Handler, error) {
	return StaticHandler(flagFiles, "flags")
}

// HasFlag says whether a language has one.
//
// Nothing today answers false — there are three interface languages and three
// files — but the interface languages are a build-time fact and the flags are
// files somebody has to remember to add. A missing one should leave a gap the
// template can fill with the language's own name, not a broken image.
func HasFlag(code string) bool {
	_, err := flagFiles.Open("flags/" + code + ".svg")
	return err == nil
}

// languageNames are the endonyms: what each language calls itself.
//
// Deliberately **not** translation keys. "Deutsch" is Deutsch on the French
// interface too, because somebody looking for their own language is looking
// for the word they would use, not for our word for them — which is the one
// word on this page they may well not read.
var languageNames = map[string]string{
	"de": "Deutsch",
	"en": "English",
	"fr": "Français",
}

// LanguageName names a language in its own language.
//
// An unknown code answers with itself, upper-cased: a two-letter tag is a poor
// label and a better one than nothing, and this is the string a screen reader
// announces for a control that otherwise shows only a picture.
func LanguageName(code string) string {
	if name, ok := languageNames[code]; ok {
		return name
	}
	return strings.ToUpper(code)
}
