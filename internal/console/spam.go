package console

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/CoderSyndicate/doleances/internal/apiclient"
)

func (c *console) registerSpamRoutes(mux *http.ServeMux) {
	mux.Handle("GET /spam", c.localization.Middleware(http.HandlerFunc(c.spamPage)))

	mux.HandleFunc("GET /api/spam", c.listSpam)
	mux.HandleFunc("POST /api/spam/{id}/rescue", c.rescueSpam)
	mux.HandleFunc("DELETE /api/spam/{id}", c.deleteSpam)
	mux.HandleFunc("PUT /api/curation", c.saveCurationSettings)
}

func (c *console) spamPage(w http.ResponseWriter, r *http.Request) {
	c.renderer.Render(w, http.StatusOK, "spam", spamPage{page: c.newPage(r, "spam.title")})
}

func (c *console) listSpam(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	sample, err := c.backend.ListSpam(r.Context(), page)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, sample)
}

func (c *console) rescueSpam(w http.ResponseWriter, r *http.Request) {
	if err := c.backend.RescueSpam(r.Context(), c.actor(r), r.PathValue("id")); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *console) deleteSpam(w http.ResponseWriter, r *http.Request) {
	if err := c.backend.DeleteSpam(r.Context(), c.actor(r), r.PathValue("id")); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *console) saveCurationSettings(w http.ResponseWriter, r *http.Request) {
	var settings apiclient.CurationSettings
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&settings); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}

	if err := c.backend.SaveCurationSettings(r.Context(), c.actor(r), settings); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"saved": true})
}
