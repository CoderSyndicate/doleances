// Package web is the shared plumbing for the two server-rendered services:
// translation, template rendering and embedded static assets.
package web

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

// LanguageCookie remembers a visitor's explicit choice.
const LanguageCookie = "doleances_lang"

// Localization holds the loaded message catalogues.
//
// Translations live in TOML files outside the code, so they can be exported,
// handed to a translator and imported back without anyone touching Go.
type Localization struct {
	bundle    *i18n.Bundle
	supported []string
	fallback  string
}

// NewLocalization loads every catalogue under dir in fsys. Files are named
// active.<lang>.toml, which is what `goi18n merge` produces.
func NewLocalization(fsys fs.FS, dir, fallback string) (*Localization, error) {
	tag, err := language.Parse(fallback)
	if err != nil {
		return nil, fmt.Errorf("invalid fallback language %q: %w", fallback, err)
	}

	bundle := i18n.NewBundle(tag)
	bundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)

	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read locales in %s: %w", dir, err)
	}

	var supported []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".toml") {
			continue
		}
		if _, err := bundle.LoadMessageFileFS(fsys, path.Join(dir, name)); err != nil {
			return nil, fmt.Errorf("load %s: %w", name, err)
		}
		if lang := languageFromFilename(name); lang != "" {
			supported = append(supported, lang)
		}
	}
	if len(supported) == 0 {
		return nil, fmt.Errorf("no translation catalogues found in %s", dir)
	}

	return &Localization{bundle: bundle, supported: supported, fallback: fallback}, nil
}

// languageFromFilename extracts "fr" from "active.fr.toml".
func languageFromFilename(name string) string {
	parts := strings.Split(strings.TrimSuffix(name, ".toml"), ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-1]
}

// Supported lists the languages the site is translated into.
func (l *Localization) Supported() []string { return l.supported }

// Fallback is the language used when nothing else matches.
func (l *Localization) Fallback() string { return l.fallback }

type contextKey int

const localizerKey contextKey = iota

// carrier is what the middleware puts in the context: the localizer and the
// language it was resolved to.
type carrier struct {
	localizer *i18n.Localizer
	lang      string
}

// Middleware resolves the visitor's language and attaches a localizer to the
// request, so no handler has to think about language again.
//
// Resolution order: an explicit ?lang= choice, then a remembered cookie, then
// the browser's Accept-Language, then the fallback.
func (l *Localization) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang, explicit := l.resolve(r)
		if explicit {
			// Remember the choice, so it survives the next click.
			http.SetCookie(w, &http.Cookie{
				Name:     LanguageCookie,
				Value:    lang,
				Path:     "/",
				MaxAge:   int((365 * 24 * 3600)),
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
		}

		ctx := context.WithValue(r.Context(), localizerKey, carrier{
			localizer: i18n.NewLocalizer(l.bundle, lang, l.fallback),
			lang:      lang,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// resolve reports the language to use and whether the visitor asked for it
// explicitly.
func (l *Localization) resolve(r *http.Request) (string, bool) {
	if lang := r.URL.Query().Get("lang"); l.isSupported(lang) {
		return lang, true
	}
	if cookie, err := r.Cookie(LanguageCookie); err == nil && l.isSupported(cookie.Value) {
		return cookie.Value, false
	}
	if lang := l.fromAcceptLanguage(r.Header.Get("Accept-Language")); lang != "" {
		return lang, false
	}
	return l.fallback, false
}

func (l *Localization) isSupported(lang string) bool {
	for _, s := range l.supported {
		if s == lang {
			return true
		}
	}
	return false
}

// fromAcceptLanguage picks the best supported match for the browser's header.
func (l *Localization) fromAcceptLanguage(header string) string {
	if header == "" {
		return ""
	}
	tags, _, err := language.ParseAcceptLanguage(header)
	if err != nil {
		return ""
	}
	for _, tag := range tags {
		base, _ := tag.Base()
		if l.isSupported(base.String()) {
			return base.String()
		}
	}
	return ""
}

// LocalizerFrom returns the request's localizer, or nil outside a localized
// request.
func LocalizerFrom(ctx context.Context) *i18n.Localizer {
	c, _ := ctx.Value(localizerKey).(carrier)
	return c.localizer
}

// LanguageFrom returns the language resolved for the request.
func LanguageFrom(ctx context.Context) string {
	c, _ := ctx.Value(localizerKey).(carrier)
	return c.lang
}
