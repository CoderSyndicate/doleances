// Package corpus is the historical doléances the site shows alongside
// present-day ones.
//
// The texts live as reviewable files in the repository — a change to what the
// site quotes belongs in a diff, not in a row somebody typed — and are seeded
// into the database once, at startup. The database is authoritative
// afterwards, so a curator can correct or withdraw a passage without a
// deployment.
//
// Layout:
//
//	library/<language>/<document>.json
//
// The folder says what language a file is written in. `source.language` says
// what language the document was *composed* in, so the file whose folder
// matches it is the original and every other is a translation of it. Snippet
// ids are shared across languages: that is what ties a translation to the
// passage it renders.
package corpus

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// library holds the corpus files compiled into the binary.
//
//go:embed library/*/*.json
var library embed.FS

// Source describes where a set of snippets comes from. Every snippet inherits
// it: a quotation without its provenance is not evidence of anything.
type Source struct {
	Title  string `json:"title"`
	Period string `json:"period"`
	Region string `json:"region"`

	// Language is the language the document was written in — not the language
	// of this file, which is its folder.
	Language string `json:"language"`

	// Latitude and Longitude put the document on the map.
	//
	// The commune, not a building: a cahier was written for a parish, so the
	// honest point is the place it was written for. Optional — a passage
	// without them keeps its Region in words and sits on no point, which is
	// the truth rather than a gap.
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`

	// Reference is the bibliographic citation.
	Reference string `json:"reference"`
	// URL points at the digitised original where there is one.
	URL string `json:"url"`
	// Note is context for whoever maintains the file, not for the site.
	Note string `json:"note"`
}

// ElisionMarker separates passages that are not contiguous in the original.
//
// A snippet may join lines that sit apart in the document; without the marker
// the quotation would read as one continuous breath, which is not what the
// writer said.
const ElisionMarker = "[...]"

// Snippet is one passage.
type Snippet struct {
	// ID is stable, hand-written, and shared across languages: it is both the
	// seeding key and the link between a passage and its translations.
	ID string `json:"id"`

	// Title is a short label, shown above the passage.
	Title string `json:"title"`

	// Text is the passage itself.
	Text string `json:"text"`
}

// File is one corpus document in one language.
type File struct {
	Source   Source    `json:"source"`
	Snippets []Snippet `json:"snippets"`
}

// Entry is one passage in its original language, with whatever translations
// the corpus provides.
type Entry struct {
	Snippet
	Source Source

	// Translations are keyed by language code.
	Translations map[string]Translation
}

// Translation is one passage rendered into another language.
type Translation struct {
	Language string
	Title    string
	Text     string

	// Source is the translated provenance, so a reader is not sent back to a
	// citation in a language they do not read.
	Source Source
}

// Load returns every passage, each carrying its translations.
func Load() ([]Entry, error) {
	names, err := fs.Glob(library, "library/*/*.json")
	if err != nil {
		return nil, fmt.Errorf("find corpus files: %w", err)
	}

	var (
		originals []Entry
		index     = map[string]int{} // snippet id -> position in originals
		pending   []Translation      // translations of ids not yet seen
		pendingID []string
		seen      = map[string]string{} // language:id -> file
	)

	for _, name := range names {
		language := path.Base(path.Dir(name))

		raw, err := fs.ReadFile(library, name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}

		var file File
		if err := json.Unmarshal(raw, &file); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		if err := validate(name, language, file, seen); err != nil {
			return nil, err
		}

		if language == file.Source.Language {
			for _, snippet := range file.Snippets {
				index[snippet.ID] = len(originals)
				originals = append(originals, Entry{
					Snippet:      snippet,
					Source:       file.Source,
					Translations: map[string]Translation{},
				})
			}
			continue
		}

		for _, snippet := range file.Snippets {
			pending = append(pending, Translation{
				Language: language,
				Title:    snippet.Title,
				Text:     snippet.Text,
				Source:   file.Source,
			})
			pendingID = append(pendingID, snippet.ID)
		}
	}

	// Attached in a second pass, because a translation may be read before the
	// original it belongs to — the glob is alphabetical, so "de" comes first.
	for i, translation := range pending {
		id := pendingID[i]
		at, ok := index[id]
		if !ok {
			return nil, fmt.Errorf(
				"%s translation %q has no original: no file in library/%s/ declares it",
				translation.Language, id, originals[0].Source.Language)
		}
		originals[at].Translations[translation.Language] = translation
	}
	return originals, nil
}

// validate refuses a corpus file that would produce unusable entries. A
// missing source or a duplicated id should stop the service at startup, not
// surface as a mis-attributed quotation on the landing page.
func validate(name, language string, file File, seen map[string]string) error {
	switch {
	case strings.TrimSpace(language) == "":
		return fmt.Errorf("%s: not inside a language folder", name)
	case strings.TrimSpace(file.Source.Title) == "":
		return fmt.Errorf("%s: the source has no title", name)
	case strings.TrimSpace(file.Source.Language) == "":
		return fmt.Errorf("%s: the source does not say what language it was written in", name)
	case strings.TrimSpace(file.Source.Reference) == "":
		return fmt.Errorf("%s: the source has no reference — a quotation needs its provenance", name)
	case len(file.Snippets) == 0:
		return fmt.Errorf("%s: no snippets", name)
	}

	for _, snippet := range file.Snippets {
		switch {
		case strings.TrimSpace(snippet.ID) == "":
			return fmt.Errorf("%s: a snippet has no id", name)
		case strings.TrimSpace(snippet.Text) == "":
			return fmt.Errorf("%s: snippet %q has no text", name, snippet.ID)
		case strings.TrimSpace(snippet.Title) == "":
			return fmt.Errorf("%s: snippet %q has no title", name, snippet.ID)
		}

		key := language + ":" + snippet.ID
		if other, duplicate := seen[key]; duplicate {
			return fmt.Errorf("%s: snippet id %q is already used in %s", name, snippet.ID, other)
		}
		seen[key] = name
	}
	return nil
}
