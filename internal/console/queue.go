package console

import (
	"encoding/json"
	"io"
	"net/http"
)

func (c *console) registerQueueRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/curation/queue", c.listCurationQueue)
	mux.HandleFunc("POST /api/curation/{id}/accept", c.acceptMessage)
	mux.HandleFunc("POST /api/curation/{id}/reject", c.rejectMessage)

	// Subject questions live on the queue page rather than on a settings page
	// of their own: they are answered by reading the doléance that raised
	// them, and that is what a curator already has in front of them here.
	mux.HandleFunc("GET /api/curation/subjects", c.listSubjectQuestions)
	mux.HandleFunc("POST /api/curation/subjects/{id}/merge", c.mergeSubjects)
	mux.HandleFunc("POST /api/curation/subjects/{id}/dismiss", c.keepSubjectsApart)

	mux.HandleFunc("GET /api/curation/entities", c.listEntityProposals)
	mux.HandleFunc("POST /api/curation/entities/{id}/confirm", c.confirmEntity)
	mux.HandleFunc("POST /api/curation/entities/{id}/reject", c.rejectEntity)

	mux.HandleFunc("GET /api/curation/vocabulary", c.listVocabulary)
	mux.HandleFunc("POST /api/curation/subjects/{id}/rename", c.renameSubject)

	mux.HandleFunc("GET /api/curation/vocabulary/search", c.searchSubjects)
	mux.HandleFunc("GET /api/curation/vocabulary/detached", c.listDetached)
	mux.HandleFunc("GET /api/curation/vocabulary/{id}", c.getSubject)
	mux.HandleFunc("GET /api/curation/wikidata", c.searchWikidata)
	mux.HandleFunc("POST /api/curation/vocabulary/{id}/entity", c.attachEntity)
	mux.HandleFunc("DELETE /api/curation/vocabulary/{id}/entity", c.detachEntity)
	mux.HandleFunc("DELETE /api/curation/vocabulary/{id}/relations/{other}", c.unlinkSubjects)
}

func (c *console) searchSubjects(w http.ResponseWriter, r *http.Request) {
	found, err := c.backend.SearchSubjects(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, found)
}

func (c *console) listDetached(w http.ResponseWriter, r *http.Request) {
	found, err := c.backend.ListDetachedSubjects(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, found)
}

func (c *console) getSubject(w http.ResponseWriter, r *http.Request) {
	subject, err := c.backend.GetSubject(r.Context(), r.PathValue("id"))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, subject)
}

// searchWikidata proxies the lookup. The console's browser never contacts
// Wikidata itself: a page that did would hand it the address of everyone
// curating, which is the same trade the reverse geocoder refuses.
func (c *console) searchWikidata(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	candidates, err := c.backend.SearchWikidata(r.Context(),
		query.Get("q"), query.Get("language"))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": candidates})
}

func (c *console) attachEntity(w http.ResponseWriter, r *http.Request) {
	var body struct {
		QID      string `json:"qid"`
		Label    string `json:"label"`
		Relation string `json:"relation"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}

	err := c.backend.AttachEntity(r.Context(), c.actor(r), r.PathValue("id"),
		body.QID, body.Label, body.Relation)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *console) detachEntity(w http.ResponseWriter, r *http.Request) {
	if err := c.backend.DetachEntity(r.Context(), c.actor(r), r.PathValue("id")); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *console) unlinkSubjects(w http.ResponseWriter, r *http.Request) {
	err := c.backend.UnlinkSubjects(r.Context(), c.actor(r),
		r.PathValue("id"), r.PathValue("other"))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *console) listEntityProposals(w http.ResponseWriter, r *http.Request) {
	proposals, err := c.backend.ListEntityProposals(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, proposals)
}

func (c *console) confirmEntity(w http.ResponseWriter, r *http.Request) {
	reason := decisionReason(r)
	if err := c.backend.ConfirmEntity(r.Context(), c.actor(r), r.PathValue("id"), reason); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *console) rejectEntity(w http.ResponseWriter, r *http.Request) {
	reason := decisionReason(r)
	if err := c.backend.RejectEntity(r.Context(), c.actor(r), r.PathValue("id"), reason); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *console) listVocabulary(w http.ResponseWriter, r *http.Request) {
	vocabulary, err := c.backend.ListVocabulary(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, vocabulary)
}

func (c *console) renameSubject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label  string `json:"label"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}

	err := c.backend.RenameSubject(r.Context(), c.actor(r), r.PathValue("id"),
		body.Label, body.Reason)
	if err != nil {
		// The backend refuses a label longer than three words, and that is an
		// answer to the curator rather than a fault. It reaches them as the
		// message it is.
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *console) listSubjectQuestions(w http.ResponseWriter, r *http.Request) {
	questions, err := c.backend.ListSubjectQuestions(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, questions)
}

func (c *console) mergeSubjects(w http.ResponseWriter, r *http.Request) {
	reason := decisionReason(r)
	if err := c.backend.MergeSubjects(r.Context(), c.actor(r), r.PathValue("id"), reason); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *console) keepSubjectsApart(w http.ResponseWriter, r *http.Request) {
	reason := decisionReason(r)
	if err := c.backend.KeepSubjectsApart(r.Context(), c.actor(r), r.PathValue("id"), reason); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *console) listCurationQueue(w http.ResponseWriter, r *http.Request) {
	queue, err := c.backend.ListCurationQueue(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, queue)
}

func (c *console) acceptMessage(w http.ResponseWriter, r *http.Request) {
	reason := decisionReason(r)
	if err := c.backend.AcceptMessage(r.Context(), c.actor(r), r.PathValue("id"), reason); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *console) rejectMessage(w http.ResponseWriter, r *http.Request) {
	reason := decisionReason(r)
	if err := c.backend.RejectMessage(r.Context(), c.actor(r), r.PathValue("id"), reason); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decisionReason reads the curator's justification. A malformed body is not
// worth refusing a decision over: the reason is a note, the decision is the act.
func decisionReason(r *http.Request) string {
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	return body.Reason
}
