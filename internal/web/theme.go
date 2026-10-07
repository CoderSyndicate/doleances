package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CoderSyndicate/doleances/internal/apiclient"
	"github.com/CoderSyndicate/doleances/internal/theme"
	"github.com/rs/zerolog/log"
)

// themeCacheTTL is how long a rendered overlay is reused. Every page loads it,
// so it is worth caching; a few seconds is short enough that selecting a theme
// in the console shows up almost at once.
const themeCacheTTL = 15 * time.Second

// ThemeSource provides the active theme as rendered CSS. The backend is the
// only thing that can answer it, since it owns the library.
type ThemeSource interface {
	ActiveThemeCSS(ctx context.Context) ([]byte, error)
}

// ThemeOverlay serves the active theme's token overrides.
//
// It is linked after the static defaults on every page, so it overrides only
// what the theme declares. When the backend is unreachable the handler serves
// an empty overlay rather than an error: the built-in palette is already
// loaded and correct, and a themed site must never blank because a theme could
// not be fetched.
type ThemeOverlay struct {
	source ThemeSource

	mu        sync.Mutex
	cached    []byte
	cachedAt  time.Time
	lastError time.Time
}

// NewThemeOverlay returns a handler serving the active theme's CSS.
func NewThemeOverlay(source ThemeSource) *ThemeOverlay {
	return &ThemeOverlay{source: source}
}

func (o *ThemeOverlay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	css := o.css(r.Context())

	// This body depends on which theme is active — a runtime choice — so it
	// cannot be fingerprinted in its URL the way the embedded assets are.
	// An ETag is what fits: the browser revalidates cheaply, and selecting a
	// different theme changes the tag immediately.
	etag := `"` + contentETag(css) + `"`

	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("ETag", etag)
	// Short-lived: a theme change should reach visitors without a hard reload.
	w.Header().Set("Cache-Control", "public, max-age=15")
	// The two web services are separate origins, and either may link the other's
	// overlay.
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if matchesETag(r, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Write(css) //nolint:errcheck
}

func (o *ThemeOverlay) css(ctx context.Context) []byte {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.cached != nil && time.Since(o.cachedAt) < themeCacheTTL {
		return o.cached
	}

	css, err := o.source.ActiveThemeCSS(ctx)
	if err != nil {
		// Log at most once a minute: every page view would otherwise repeat it.
		if time.Since(o.lastError) > time.Minute {
			log.Warn().Err(err).Msg("cannot fetch the active theme, serving built-in colours")
			o.lastError = time.Now()
		}
		if o.cached != nil {
			return o.cached // a stale theme beats no theme
		}
		return []byte("/* The theme service is unavailable — the built-in palette applies. */\n")
	}

	o.cached = css
	o.cachedAt = time.Now()
	return css
}

// Invalidate drops the cached overlay, so the next request re-fetches. The
// console calls it after changing the library.
func (o *ThemeOverlay) Invalidate() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cached = nil
}

// ThemeAssets serves the shared token stylesheet, the shared component styles
// and the toggle script, all compiled into the binary.
func ThemeAssets(mux *http.ServeMux, version string) {
	serve := func(path, contentType string, content []byte) {
		etag := `"` + contentETag(content) + `"`

		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("ETag", etag)

			// A request carrying the current fingerprint can only be for this
			// exact content, so it never needs revalidating. Without the
			// fingerprint the same URL may outlive its content, so it gets a
			// short life and an ETag to revalidate cheaply against.
			if version != "" && r.URL.Query().Get("v") == version {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "public, max-age=60")
			}

			if matchesETag(r, etag) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Write(content) //nolint:errcheck
		})
	}
	serve("/static/tokens.css", "text/css; charset=utf-8", theme.TokensCSS)
	serve("/static/base.css", "text/css; charset=utf-8", theme.BaseCSS)
	serve("/static/theme.js", "text/javascript; charset=utf-8", theme.ToggleJS)
}

// contentETag is a short hash of a response body.
func contentETag(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])[:16]
}

// matchesETag reports whether the client already holds this exact body.
func matchesETag(r *http.Request, etag string) bool {
	for _, candidate := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		if strings.TrimSpace(candidate) == etag {
			return true
		}
	}
	return false
}

// AssetSource resolves a theme image. Only the backend can answer it, since it
// owns the library.
type AssetSource interface {
	ActiveThemeAsset(ctx context.Context, slot string) (apiclient.Asset, error)
}

// assetCacheTTL matches the overlay's: an operator replacing a logo should see
// it without clearing a cache.
const assetCacheTTL = 30 * time.Second

// ThemeAssets serves the active theme's images, falling back to the built-in
// defaults, at /theme/assets/{slot}.
//
// Failures serve stale content where there is any, and 404 otherwise: a
// missing logo must degrade to a gap in the page, never to an error page.
type ThemeAssetHandler struct {
	source AssetSource

	mu     sync.Mutex
	cached map[string]cachedAsset
}

type cachedAsset struct {
	asset   apiclient.Asset
	fetched time.Time
}

// NewThemeAssetHandler returns a handler serving the active theme's images.
func NewThemeAssetHandler(source AssetSource) *ThemeAssetHandler {
	return &ThemeAssetHandler{source: source, cached: map[string]cachedAsset{}}
}

func (h *ThemeAssetHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	slot := r.PathValue("slot")
	if !theme.IsAssetSlot(slot) {
		http.NotFound(w, r)
		return
	}

	asset, ok := h.asset(r.Context(), slot)
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", asset.ContentType)
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.Header().Set("Content-Security-Policy", assetPolicy(slot, asset.Data))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(asset.Data) //nolint:errcheck
}

// assetPolicy is the Content-Security-Policy one image is served under.
//
// # Why the built-in images are not sandboxed, and uploads are
//
// `sandbox` is what keeps an uploaded SVG from being code execution on this
// origin: it can carry script, it comes from whoever can reach the console,
// and it is served from the same host as everything else. That reasoning does
// not reach the images shipped inside this binary — nobody uploaded those, and
// what they contain is in the repository.
//
// The distinction is not pedantry, because `sandbox` has a second effect
// nobody wanted: a sandboxed resource is an opaque origin, and a browser will
// not paint one as a **favicon**. So the mark was fetched on every page load,
// answered 200 with the right content type and a well-formed document, and the
// tab stayed empty — a header doing exactly what it says, to the one asset
// whose whole job is to be drawn by the browser chrome.
//
// `default-src 'none'` stays either way: an image needs no subresources, and
// an SVG that tries to fetch one is doing something nothing here asks for.
// `style-src 'unsafe-inline'` stays because the mark carries its own colours —
// it has to, since nothing on a page can style a favicon.
func assetPolicy(slot string, data []byte) string {
	const base = "default-src 'none'; style-src 'unsafe-inline'"

	if builtin, _, ok := theme.DefaultAsset(slot); ok && bytes.Equal(builtin, data) {
		// Byte-identical to what this binary ships, so it is what this binary
		// ships. A comparison rather than a flag threaded through the API:
		// there is nothing to get out of step, and an uploaded file that
		// happened to match ours would be ours.
		return base
	}
	return base + "; sandbox"
}

func (h *ThemeAssetHandler) asset(ctx context.Context, slot string) (apiclient.Asset, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if entry, ok := h.cached[slot]; ok && time.Since(entry.fetched) < assetCacheTTL {
		return entry.asset, true
	}

	asset, err := h.source.ActiveThemeAsset(ctx, slot)
	if err != nil {
		if entry, ok := h.cached[slot]; ok {
			return entry.asset, true // a stale logo beats no logo
		}
		log.Debug().Err(err).Str("slot", slot).Msg("no theme image available")
		return apiclient.Asset{}, false
	}

	h.cached[slot] = cachedAsset{asset: asset, fetched: time.Now()}
	return asset, true
}

// Invalidate drops cached images, so the next request re-fetches.
func (h *ThemeAssetHandler) Invalidate() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cached = map[string]cachedAsset{}
}

// AssetVersion is a short fingerprint of everything served from /static.
//
// Embedded assets are cached hard by the browser, and a redeploy alone does
// not invalidate that: the URL has to change. Hashing the bytes rather than
// using the build version means a change is picked up in development too,
// where the version string is always "dev".
func AssetVersion(fsys fs.FS, dir string) string {
	sum := sha256.New()
	for _, shared := range [][]byte{theme.TokensCSS, theme.BaseCSS, theme.ToggleJS} {
		sum.Write(shared) //nolint:errcheck
	}

	// Walk in a fixed order so the same content always yields the same hash.
	err := fs.WalkDir(fsys, dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		sum.Write([]byte(path)) //nolint:errcheck
		sum.Write(content)      //nolint:errcheck
		return nil
	})
	if err != nil {
		// A fingerprint we cannot compute must not stop the service; a
		// timestamp still busts the cache, it just does so on every restart.
		log.Warn().Err(err).Msg("cannot fingerprint static assets")
		return strconv.FormatInt(time.Now().Unix(), 36)
	}
	return hex.EncodeToString(sum.Sum(nil))[:12]
}

// FileSource resolves a file from a theme's package.
type FileSource interface {
	ThemeFile(ctx context.Context, name, path string) (apiclient.Asset, error)
}

// ThemeFileHandler serves the files a theme's own tokens reference with
// url(), at /theme/files/{kind}/{name}/{path...}.
//
// Both web services expose the same shape, so a rewritten url() resolves on
// whichever origin rendered the page — and, because the file comes from this
// server, a theme can never send a visitor's browser to a third party.
type ThemeFileHandler struct {
	source FileSource

	mu     sync.Mutex
	cached map[string]cachedAsset
}

// NewThemeFileHandler returns a handler for a theme's package files.
func NewThemeFileHandler(source FileSource) *ThemeFileHandler {
	return &ThemeFileHandler{source: source, cached: map[string]cachedAsset{}}
}

func (h *ThemeFileHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, file := r.PathValue("name"), r.PathValue("path")
	if name == "" || file == "" {
		http.NotFound(w, r)
		return
	}

	asset, ok := h.file(r.Context(), name, file)
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", asset.ContentType)
	// A package file belongs to a named theme version: replacing it means
	// uploading a new package, so it can be cached hard.
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(asset.Data) //nolint:errcheck
}

func (h *ThemeFileHandler) file(ctx context.Context, name, file string) (apiclient.Asset, bool) {
	key := name + "/" + file

	h.mu.Lock()
	defer h.mu.Unlock()

	if entry, ok := h.cached[key]; ok && time.Since(entry.fetched) < assetCacheTTL {
		return entry.asset, true
	}

	asset, err := h.source.ThemeFile(ctx, name, file)
	if err != nil {
		if entry, ok := h.cached[key]; ok {
			return entry.asset, true // a stale file beats a missing one
		}
		log.Debug().Err(err).Str("file", key).Msg("no theme file available")
		return apiclient.Asset{}, false
	}

	h.cached[key] = cachedAsset{asset: asset, fetched: time.Now()}
	return asset, true
}

// Invalidate drops cached files, so the next request re-fetches.
func (h *ThemeFileHandler) Invalidate() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cached = map[string]cachedAsset{}
}
