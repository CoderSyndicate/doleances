package web

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/rs/zerolog/log"
)

// recurrenceParts is the shape a template needs a broken-up rhythm in.
//
// An interface rather than the concrete type, because the type lives in the
// API client and this package is below it: web has no business importing the
// thing that talks to the backend, and a template helper has no business
// knowing which struct it was handed.
type recurrenceParts interface {
	GetInterval() int
	GetWeekdays() []int
	GetWeek() int
	GetDay() int
	GetMonth() int
	Monthly() bool
	Yearly() bool
}

// Renderer executes the templates of one service.
//
// Each page gets its own template set, parsed from the shared templates plus
// that one page file. A single shared set would not work: every page defines
// "content", and in one set the last one parsed would silently win.
//
// Parsing happens once at startup, because a template that fails to parse
// should take the service down immediately rather than surprise a visitor.
type Renderer struct {
	pages map[string]*template.Template
}

// NewRenderer parses every page matching pageGlob, each together with every
// shared template matching sharedGlob. A page is addressed by its file name
// without the extension: templates/landing.html is "landing".
func NewRenderer(fsys fs.FS, sharedGlob, pageGlob string) (*Renderer, error) {
	shared, err := fs.Glob(fsys, sharedGlob)
	if err != nil {
		return nil, fmt.Errorf("find shared templates: %w", err)
	}
	pages, err := fs.Glob(fsys, pageGlob)
	if err != nil {
		return nil, fmt.Errorf("find page templates: %w", err)
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("no page templates matched %q", pageGlob)
	}

	r := &Renderer{pages: make(map[string]*template.Template, len(pages))}
	for _, page := range pages {
		name := strings.TrimSuffix(path.Base(page), path.Ext(page))
		files := append(append([]string{}, shared...), page)

		tmpl, err := template.New(name).Funcs(helpers()).ParseFS(fsys, files...)
		if err != nil {
			return nil, fmt.Errorf("parse page %s: %w", name, err)
		}
		if tmpl.Lookup("layout") == nil {
			return nil, fmt.Errorf("page %s has no layout template", name)
		}
		r.pages[name] = tmpl
	}
	return r, nil
}

// Render writes a page as a full HTML response.
//
// The page is rendered into a buffer first: a template that fails halfway
// would otherwise leave a half-written page and a 200 status on the wire, with
// no way left to report the error.
func (r *Renderer) Render(w http.ResponseWriter, status int, name string, data any) {
	tmpl, ok := r.pages[name]
	if !ok {
		log.Error().Str("page", name).Msg("no such page template")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		log.Error().Err(err).Str("page", name).Msg("cannot render page")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		log.Debug().Err(err).Str("page", name).Msg("visitor disconnected mid-response")
	}
}

// Defined reports whether a page exists, so a service can check its own
// routing table at startup rather than at the first request.
func (r *Renderer) Defined(name string) bool {
	_, ok := r.pages[name]
	return ok
}

// Page is the data every template receives. A service embeds it in its own
// page data.
//
// Translation is a method rather than a template function so that the
// request's localizer travels with the data: binding a function per request
// would mean cloning the whole template set on every page view.
type Page struct {
	// Title is the translation key for the page title.
	Title string

	// Lang is the language this page is rendered in.
	Lang string

	// Languages are the languages the site offers.
	Languages []string

	// Path is the current request path, for marking the active nav item.
	Path string

	// Nonce authorises this page's own inline scripts under the
	// Content-Security-Policy, and nothing else.
	//
	// **Every inline `<script>` a template renders needs it.** One without is
	// refused by the browser, silently — which is the whole point of the
	// policy and also the way a page breaks when somebody forgets. A test
	// asserts that none is forgotten.
	Nonce string

	localizer *i18n.Localizer
}

// NewPage builds the shared page data from a request.
func NewPage(r *http.Request, l *Localization, titleKey string) Page {
	return Page{
		Title:     titleKey,
		Lang:      LanguageFrom(r.Context()),
		Languages: l.Supported(),
		Path:      r.URL.Path,
		Nonce:     NonceFrom(r.Context()),
		localizer: LocalizerFrom(r.Context()),
	}
}

// T translates a message by id.
//
// A missing translation renders as the id itself rather than as an empty
// space: a visible key is a bug report, a blank is a mystery.
func (p Page) T(id string) string {
	if p.localizer == nil {
		return id
	}
	out, err := p.localizer.Localize(&i18n.LocalizeConfig{MessageID: id})
	if err != nil {
		log.Debug().Str("id", id).Str("lang", p.Lang).Msg("missing translation")
		return id
	}
	return out
}

// Tf translates a message and fills in its template data.
func (p Page) Tf(id string, args ...any) string {
	if p.localizer == nil || len(args)%2 != 0 {
		return id
	}

	data := make(map[string]any, len(args)/2)
	for i := 0; i < len(args); i += 2 {
		key, ok := args[i].(string)
		if !ok {
			return id
		}
		data[key] = args[i+1]
	}

	out, err := p.localizer.Localize(&i18n.LocalizeConfig{MessageID: id, TemplateData: data})
	if err != nil {
		log.Debug().Str("id", id).Str("lang", p.Lang).Msg("missing translation")
		return id
	}
	return out
}

// IsCurrent reports whether a nav path is the one being viewed.
func (p Page) IsCurrent(path string) bool {
	return p.Path == path
}

func helpers() template.FuncMap {
	return template.FuncMap{
		// dict builds a map inline, for passing several values to a partial.
		"dict": func(values ...any) (map[string]any, error) {
			if len(values)%2 != 0 {
				return nil, fmt.Errorf("dict needs an even number of arguments, got %d", len(values))
			}
			out := make(map[string]any, len(values)/2)
			for i := 0; i < len(values); i += 2 {
				key, ok := values[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict keys must be strings, got %T", values[i])
				}
				out[key] = values[i+1]
			}
			return out, nil
		},

		// The lists a recurrence form is built from. They live here rather
		// than on every page's data, because they are the same seven days and
		// the same five positions on every page that ever asks.
		"weekdayNumbers": func() []int { return []int{0, 1, 2, 3, 4, 5, 6} },
		"intervals":      func() []int { return []int{1, 2, 3, 4} },

		// 1 to 4, then -1 for "the last" — which is its own value rather than
		// a fifth, because only some months have a fifth Thursday.
		"positions": func() []int { return []int{1, 2, 3, 4, -1} },

		// A language under its own name, for the selector — which shows a
		// flag and therefore needs a word somewhere for anybody not looking
		// at it. See LanguageName for why it is not a translation key.
		"languageName": LanguageName,
		"hasFlag":      HasFlag,

		"contains": func(haystack []int, needle int) bool {
			return slices.Contains(haystack, needle)
		},

		// Paging arithmetic. A template cannot add, and the alternative is a
		// pair of fields on every paged page's data that only ever hold "one
		// more" and "one less".
		"inc": func(n int) int { return n + 1 },
		"dec": func(n int) int { return n - 1 },

		// weekdayNames turns [2 4] into "Tuesday, Thursday" in the reader's
		// language. Joined here rather than in the template because a template
		// cannot build a string in a loop without a mutable variable, and the
		// workarounds for that are worse than one helper.
		// The rhythm, unpacked for a form. An action being edited arrives as
		// a rule the backend has already broken up; these put each piece back
		// into the control it came from, and answer a usable default when
		// there is no rhythm at all — which is every new announcement.
		"repeatOf": func(parts any) string {
			p, ok := parts.(recurrenceParts)
			switch {
			case ok && p.Yearly():
				return "yearly"
			case ok && p.Monthly():
				return "monthly"
			}
			return "weekly"
		},
		"intervalOf": func(parts any) int {
			if p, ok := parts.(recurrenceParts); ok && p.GetInterval() > 0 {
				return p.GetInterval()
			}
			return 1
		},
		"weekdaysOf": func(parts any) []int {
			if p, ok := parts.(recurrenceParts); ok {
				return p.GetWeekdays()
			}
			return nil
		},
		"monthlyOf": func(parts any) string {
			if p, ok := parts.(recurrenceParts); ok && p.GetDay() > 0 {
				return "day"
			}
			return "weekday"
		},
		"weekOf": func(parts any) int {
			if p, ok := parts.(recurrenceParts); ok && p.GetWeek() != 0 {
				return p.GetWeek()
			}
			return 1
		},
		"weekdayOf": func(parts any) int {
			if p, ok := parts.(recurrenceParts); ok {
				if days := p.GetWeekdays(); len(days) > 0 && p.GetWeek() != 0 {
					return days[0]
				}
			}
			return int(time.Tuesday)
		},
		"dayOf": func(parts any) int {
			if p, ok := parts.(recurrenceParts); ok && p.GetDay() > 0 {
				return p.GetDay()
			}
			return 1
		},
		"monthOf": func(parts any) int {
			if p, ok := parts.(recurrenceParts); ok && p.GetMonth() > 0 {
				return p.GetMonth()
			}
			return int(time.Now().Month())
		},
		// atLeast keeps a zero from a blank draft out of a select that has no
		// zero option — a new announcement would otherwise open showing "0"
		// where a day of the month belongs.
		"atLeast": func(value, floor int) int {
			if value < floor {
				return floor
			}
			return value
		},
		"monthNumbers": func() []int {
			return []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
		},

		// clock takes "2026-09-24T19:00" down to "19:00" for a time input.
		"clock": func(stamp string) string {
			_, hhmm, found := strings.Cut(stamp, "T")
			if !found {
				return ""
			}
			return hhmm
		},

		// `any` plus an interface rather than web.Page, because every service
		// wraps Page in its own struct and a template cannot convert one to
		// the other. The method it needs is promoted through the embedding,
		// so every page satisfies this without knowing it exists.
		"weekdayNames": func(page any, days []int) string {
			translator, ok := page.(interface{ T(string) string })
			if !ok {
				return ""
			}
			names := make([]string, 0, len(days))
			for _, day := range days {
				names = append(names, translator.T(fmt.Sprintf("weekday.%d", day)))
			}
			return strings.Join(names, ", ")
		},
	}
}
