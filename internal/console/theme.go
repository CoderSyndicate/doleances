package console

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/CoderSyndicate/doleances/internal/apiclient"
	"github.com/CoderSyndicate/doleances/internal/theme"
)

// maxThemeUpload bounds an uploaded theme file. A palette is a few kilobytes;
// anything approaching this is not a design token document.
const maxThemeUpload = 1 << 20 // 1 MiB

// The console manages the theme library through the backend's API — it holds
// no database credentials of its own. These handlers exist so the browser
// talks only to the console, which is the only origin it is authenticated
// against.
func (c *console) registerThemeRoutes(mux *http.ServeMux) {
	mux.Handle("GET /settings/theme", c.localization.Middleware(http.HandlerFunc(c.themePage)))

	mux.HandleFunc("GET /api/themes", c.listThemes)
	mux.HandleFunc("PUT /api/themes/active", c.setActiveTheme)
	mux.HandleFunc("POST /api/themes/import", c.importTheme)
	mux.HandleFunc("DELETE /api/themes/{name}", c.deleteTheme)
	// A wildcard has to be a whole path segment, so the download name cannot
	// carry the .dtcg.json suffix — the Content-Disposition header supplies it.
	mux.HandleFunc("GET /theme/download/{name}", c.downloadTheme)

	mux.HandleFunc("GET /api/themes/{name}/assets", c.listThemeAssets)
	mux.HandleFunc("PUT /api/themes/{name}/assets/{slot}", c.uploadThemeAsset)
	mux.HandleFunc("DELETE /api/themes/{name}/assets/{slot}", c.deleteThemeAsset)
}

func (c *console) themePage(w http.ResponseWriter, r *http.Request) {
	c.renderer.Render(w, http.StatusOK, "theme", themePage{page: c.newPage(r, "theme.title")})
}

func (c *console) listThemes(w http.ResponseWriter, r *http.Request) {
	themes, err := c.backend.ListThemes(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"themes": themes})
}

func (c *console) setActiveTheme(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}

	if err := c.backend.SetActiveTheme(r.Context(), body.Name, body.Color); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	// The overlay this service serves is now stale.
	c.overlay.Invalidate()
	writeJSON(w, http.StatusOK, map[string]any{"name": body.Name, "color": body.Color})
}

func (c *console) importTheme(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxThemeUpload); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	defer file.Close() //nolint:errcheck

	document, err := io.ReadAll(io.LimitReader(file, maxThemeUpload))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}

	name := themeNameFromUpload(r.FormValue("name"), header.Filename)
	activate := r.URL.Query().Get("activate") == "1"
	if err := c.backend.UploadTheme(r.Context(), name, document, activate); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	if activate {
		c.overlay.Invalidate()
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "active": activate})
}

func (c *console) deleteTheme(w http.ResponseWriter, r *http.Request) {
	if err := c.backend.DeleteTheme(r.Context(), r.PathValue("name")); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	c.overlay.Invalidate()
	w.WriteHeader(http.StatusNoContent)
}

func (c *console) downloadTheme(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	document, err := c.backend.DownloadTheme(r.Context(), name)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}

	// A download is a package, so it can be edited and uploaded again.
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.theme.zip"`)
	w.Write(document) //nolint:errcheck
}

// themeNameFromUpload prefers an explicit name and otherwise derives one from
// the file, so "vest-high-contrast.dtcg.json" becomes "vest-high-contrast".
func themeNameFromUpload(explicit, filename string) string {
	if name := strings.TrimSpace(explicit); name != "" {
		return name
	}

	// Strip the suffixes an upload tends to carry, innermost last:
	// "placard.theme.zip" is the theme "placard".
	name := path.Base(filename)
	for _, suffix := range []string{".zip", ".json", ".theme", ".dtcg", ".structure", ".tokens"} {
		name = strings.TrimSuffix(name, suffix)
	}
	if name = strings.TrimSpace(name); name == "" || name == "." {
		return "uploaded-theme"
	}
	return name
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body) //nolint:errcheck
}

func writeJSONError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// maxAssetUpload bounds an uploaded image. It matches the backend's own limit,
// so an oversized file is refused here rather than crossing the wire twice.
const maxAssetUpload = theme.MaxAssetBytes

func (c *console) listThemeAssets(w http.ResponseWriter, r *http.Request) {
	assets, err := c.backend.ListThemeAssets(r.Context(), r.PathValue("name"))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}

	// Named slots are editable here; package files are shown read-only,
	// because they arrive and leave with the package.
	slots := make([]apiclient.ThemeAssetSummary, 0, len(assets))
	files := make([]apiclient.ThemeAssetSummary, 0, len(assets))
	for _, asset := range assets {
		if theme.IsAssetSlot(asset.Slot) {
			slots = append(slots, asset)
			continue
		}
		files = append(files, asset)
	}
	writeJSON(w, http.StatusOK, map[string]any{"assets": slots, "files": files})
}

func (c *console) uploadThemeAsset(w http.ResponseWriter, r *http.Request) {
	name, slot := r.PathValue("name"), r.PathValue("slot")

	if err := r.ParseMultipartForm(maxAssetUpload); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	defer file.Close() //nolint:errcheck

	// Read one byte past the limit so an oversized file is refused rather than
	// silently truncated into a corrupt image.
	data, err := io.ReadAll(io.LimitReader(file, maxAssetUpload+1))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = mime.TypeByExtension(path.Ext(header.Filename))
	}
	if err := theme.ValidateAsset(slot, contentType, len(data)); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err)
		return
	}

	if err := c.backend.UploadThemeAsset(r.Context(), name, slot, contentType, data); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	c.assets.Invalidate()
	writeJSON(w, http.StatusOK, map[string]any{"slot": slot, "size": len(data)})
}

func (c *console) deleteThemeAsset(w http.ResponseWriter, r *http.Request) {
	err := c.backend.DeleteThemeAsset(r.Context(), r.PathValue("name"), r.PathValue("slot"))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	c.assets.Invalidate()
	w.WriteHeader(http.StatusNoContent)
}
