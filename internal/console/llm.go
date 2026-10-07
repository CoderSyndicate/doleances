package console

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/CoderSyndicate/doleances/internal/apiclient"
)

// developmentActor is what changes are recorded against while authentication
// is disabled. Once OIDC is wired, the curator's identity replaces it.
const developmentActor = "development (unauthenticated)"

func (c *console) registerLLMRoutes(mux *http.ServeMux) {
	mux.Handle("GET /settings/llm", c.localization.Middleware(http.HandlerFunc(c.llmPage)))

	mux.HandleFunc("GET /api/llm", c.getLLM)
	mux.HandleFunc("PUT /api/llm", c.saveLLM)
	mux.HandleFunc("DELETE /api/llm/key", c.clearLLMKey)
	mux.HandleFunc("POST /api/llm/test", c.testLLM)
}

func (c *console) llmPage(w http.ResponseWriter, r *http.Request) {
	c.renderer.Render(w, http.StatusOK, "llm", llmPage{page: c.newPage(r, "llm.title")})
}

func (c *console) getLLM(w http.ResponseWriter, r *http.Request) {
	settings, err := c.backend.GetLLMSettings(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (c *console) saveLLM(w http.ResponseWriter, r *http.Request) {
	var update apiclient.LLMSettingsUpdate
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&update); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}

	if err := c.backend.SaveLLMSettings(r.Context(), c.actor(r), update); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"saved": true})
}

func (c *console) clearLLMKey(w http.ResponseWriter, r *http.Request) {
	if err := c.backend.ClearLLMAPIKey(r.Context(), c.actor(r)); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *console) testLLM(w http.ResponseWriter, r *http.Request) {
	result, err := c.backend.TestLLM(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// actor is the identity a change is recorded against.
//
// The OIDC subject rather than the username, because it is stable: somebody
// renamed in the directory is the same person, and an audit log that followed
// the rename would stop answering "who decided this" for every row written
// before it. The username rides along in the log line beside it, where being
// readable matters more than being permanent.
//
// The audit log is the counterweight to curators holding accept/reject power
// over other people's words. Until this, it named nobody.
func (c *console) actor(r *http.Request) string {
	if c.development {
		return developmentActor
	}
	if who := whoIs(r.Context()); who != nil && who.Subject != "" {
		return who.Subject
	}
	// The gate does not admit anybody without an identity, so this is a
	// programming error rather than a state. Named so it is obvious in a log.
	return "unidentified (this is a bug)"
}
