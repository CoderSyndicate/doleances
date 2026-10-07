package console

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/CoderSyndicate/doleances/internal/snapshot"
)

// maxRestoreUpload bounds a package being restored. It matches the reader's
// own limit, so an oversized file is refused here rather than crossing the
// wire twice.
const maxRestoreUpload = snapshot.MaxPackageBytes

func (c *console) registerSnapshotRoutes(mux *http.ServeMux) {
	mux.Handle("GET /snapshots", c.localization.Middleware(http.HandlerFunc(c.snapshotPage)))

	mux.HandleFunc("GET /api/snapshots", c.listSnapshots)
	mux.HandleFunc("POST /api/snapshots", c.createSnapshot)
	mux.HandleFunc("DELETE /api/snapshots/{tag}", c.deleteSnapshot)
	mux.HandleFunc("POST /api/snapshots/restore", c.restoreSnapshot)
	// A wildcard has to be a whole path segment, so the download name carries
	// no .zip suffix — the Content-Disposition header supplies it.
	mux.HandleFunc("GET /snapshots/download/{tag}", c.downloadSnapshot)
}

type snapshotPageData struct{ page }

func (c *console) snapshotPage(w http.ResponseWriter, r *http.Request) {
	c.renderer.Render(w, http.StatusOK, "snapshots",
		snapshotPageData{page: c.newPage(r, "snapshots.title")})
}

func (c *console) listSnapshots(w http.ResponseWriter, r *http.Request) {
	catalogue, err := c.backend.ListSnapshots(r.Context(), r.URL.Query().Get("kind"))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, catalogue)
}

func (c *console) createSnapshot(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind  string `json:"kind"`
		Tag   string `json:"tag"`
		Notes string `json:"notes"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}

	created, err := c.backend.CreateSnapshot(r.Context(), body.Kind, body.Tag, body.Notes, c.actor(r))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, created)
}

func (c *console) deleteSnapshot(w http.ResponseWriter, r *http.Request) {
	if err := c.backend.DeleteSnapshot(r.Context(), r.PathValue("tag"), c.actor(r)); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *console) downloadSnapshot(w http.ResponseWriter, r *http.Request) {
	tag := r.PathValue("tag")

	data, err := c.backend.DownloadSnapshot(r.Context(), tag, c.actor(r))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+tag+`.zip"`)
	w.Write(data) //nolint:errcheck
}

func (c *console) restoreSnapshot(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	defer file.Close() //nolint:errcheck

	// Read one byte past the limit so an oversized package is refused rather
	// than silently truncated into an archive that restores partially.
	data, err := io.ReadAll(io.LimitReader(file, maxRestoreUpload+1))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	if len(data) > maxRestoreUpload {
		writeJSONError(w, http.StatusRequestEntityTooLarge,
			errTooLarge{limit: maxRestoreUpload})
		return
	}

	result, err := c.backend.RestoreSnapshot(r.Context(), data, c.actor(r))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// errTooLarge reports the limit rather than a bare status, so the console can
// say what would have been acceptable.
type errTooLarge struct{ limit int }

func (e errTooLarge) Error() string {
	return "the package is larger than this installation accepts"
}
