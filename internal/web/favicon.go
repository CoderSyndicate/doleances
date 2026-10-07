package web

import (
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/theme"
)

// The sizes browsers ask for. 32 is the tab; 180 is what iOS puts on a home
// screen, and is the size Apple's own guidance names.
const (
	faviconSize    = 32
	touchIconSize  = 180
	faviconMaxAge  = "public, max-age=3600"
	faviconSlot    = "mark"
	rasterCacheTTL = assetCacheTTL
)

// FaviconHandler serves the tab icon as a raster, derived from the theme's
// own SVG mark.
//
// # Why this exists when the mark is already served as SVG
//
// The page declares `<link rel="icon" type="image/svg+xml">` and that is the
// right declaration — browsers that take it get one file at any size that
// follows an uploaded theme. Safari takes it and then draws its generic letter
// tile anyway: the run logs show it fetching /theme/assets/mark twelve times
// while asking, in the same minutes, for /favicon.ico, /apple-touch-icon.png
// and /apple-touch-icon-precomposed.png — all three 404. A browser asking for
// a format and being refused it is not one that can be argued with.
//
// Both are served now. The SVG stays the source of truth and the source of the
// raster, so an operator who uploads their own mark gets a favicon of it
// without this project shipping a PNG of its own.
type FaviconHandler struct {
	assets *ThemeAssetHandler

	mu     sync.Mutex
	cached map[int]rasterised
}

type rasterised struct {
	png     []byte
	ico     []byte
	drawn   time.Time
	fetched bool
}

// NewFaviconHandler derives tab icons from the same asset handler that serves
// the images, so an invalidated logo invalidates the favicon with it.
func NewFaviconHandler(assets *ThemeAssetHandler) *FaviconHandler {
	return &FaviconHandler{assets: assets, cached: map[int]rasterised{}}
}

// Register declares the three addresses browsers look at without being told.
//
// They are fixed, unversioned paths by convention — a browser constructs them
// itself — so they are registered as literals rather than built from a
// constant, which is also what keeps them visible to the console's route test.
func (h *FaviconHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /favicon.ico", h.serveICO)
	mux.HandleFunc("GET /apple-touch-icon.png", h.servePNG)
	// Older iOS asks for this one and does not fall back to the other.
	mux.HandleFunc("GET /apple-touch-icon-precomposed.png", h.servePNG)
}

func (h *FaviconHandler) serveICO(w http.ResponseWriter, r *http.Request) {
	raster, ok := h.raster(r, faviconSize)
	if !ok || len(raster.ico) == 0 {
		http.NotFound(w, r)
		return
	}
	h.write(w, "image/x-icon", raster.ico)
}

func (h *FaviconHandler) servePNG(w http.ResponseWriter, r *http.Request) {
	raster, ok := h.raster(r, touchIconSize)
	if !ok || len(raster.png) == 0 {
		http.NotFound(w, r)
		return
	}
	h.write(w, "image/png", raster.png)
}

func (h *FaviconHandler) write(w http.ResponseWriter, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", faviconMaxAge)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(body) //nolint:errcheck
}

// raster renders the mark once per size and holds it for as long as the asset
// itself is held.
//
// Rasterising is cheap and it is not free, and a favicon is requested by every
// browser on every first visit. The lifetime matches the asset cache's, so an
// operator who uploads a new mark sees the tab icon change on the same
// schedule as the masthead rather than on one of its own.
func (h *FaviconHandler) raster(r *http.Request, size int) (rasterised, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if entry, ok := h.cached[size]; ok && time.Since(entry.drawn) < rasterCacheTTL {
		return entry, entry.fetched
	}

	asset, ok := h.assets.asset(r.Context(), faviconSlot)
	if !ok {
		return rasterised{}, false
	}

	png, err := theme.PNG(asset.Data, size)
	if err != nil {
		// A mark this rasteriser cannot read is not a reason to fail a page.
		// The SVG link still works in the browsers that take it, and the rest
		// get no tab icon, which is what they had before this existed.
		log.Warn().Err(err).Int("size", size).
			Msg("the tab icon could not be rastered; serving none")
		h.cached[size] = rasterised{drawn: time.Now()}
		return rasterised{}, false
	}

	entry := rasterised{png: png, drawn: time.Now(), fetched: true}
	if size == faviconSize {
		if ico, err := theme.ICO(png, size); err == nil {
			entry.ico = ico
		} else {
			log.Warn().Err(err).Msg("the tab icon could not be wrapped as an .ico")
		}
	}
	h.cached[size] = entry
	return entry, true
}

// Invalidate drops the rendered icons, so the next request redraws them.
func (h *FaviconHandler) Invalidate() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cached = map[int]rasterised{}
}
