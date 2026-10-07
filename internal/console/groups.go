package console

import (
	"net/http"
	"strconv"

	"github.com/rs/zerolog/log"
)

// groupsPage is the group curation queue.
//
// It is a page of its own rather than a section of the message queue. A group
// is a different decision from a doléance: what is being judged is whether
// people in a place really mean to meet, and the evidence is a name, a
// description and a set of addresses rather than somebody's grievance. Mixing
// the two would put a curator in two frames of mind on one screen.
type groupsPage struct {
	page

	// SiteBase turns a published group's name into a link to its public page.
	//
	// It used to be a link to the management page, under `--development` only,
	// because the console could not build one otherwise: the management token
	// was a hash and nothing recovered it. Both are gone — a group is managed
	// by being one of its admins, which a curator is not — so what is left is
	// the page everybody can read, for the groups that have one.
	//
	// Empty when nobody told this console where the site is. A link built from
	// a guess would look right and go nowhere.
	SiteBase string
}

func (c *console) registerGroupRoutes(mux *http.ServeMux) {
	// Nothing at all when groups are off. Not a disabled page, not an empty
	// one: the navigation entry is gone and the address answers 404, because
	// a queue that can never fill teaches curators to stop looking.
	if !c.groups {
		return
	}

	mux.Handle("GET /groups", c.localization.Middleware(http.HandlerFunc(c.groupQueue)))
	mux.HandleFunc("GET /api/curation/groups", c.listGroupQueue)
	mux.HandleFunc("POST /api/curation/groups/{id}/accept", c.acceptGroup)
	mux.HandleFunc("POST /api/curation/groups/{id}/reject", c.rejectGroup)

	// Edits to groups that are already on the map. A separate list on the same
	// page: a curator answering "should this group exist?" and one answering
	// "should this group change?" are doing different work, and the second
	// needs the current version beside the proposed one.
	mux.HandleFunc("GET /api/curation/group-revisions", c.listGroupRevisionQueue)
	mux.HandleFunc("POST /api/curation/group-revisions/{id}/accept", c.acceptGroupRevision)
	mux.HandleFunc("POST /api/curation/group-revisions/{id}/reject", c.rejectGroupRevision)

	// What the groups are doing. A third list on the same page: the
	// permission is the same and the frame of mind is close enough — "can
	// people come to this?" is the group question asked about one evening.
	mux.HandleFunc("GET /api/curation/groups/all", c.listAllGroups)
	mux.HandleFunc("GET /api/curation/actions", c.listActionQueue)
	mux.HandleFunc("POST /api/curation/actions/{id}/accept", c.acceptAction)
	mux.HandleFunc("POST /api/curation/actions/{id}/reject", c.rejectAction)
}

func (c *console) listAllGroups(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = groupsPerPage
	}

	listing, err := c.backend.ListAllGroups(r.Context(), page, perPage)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

// groupsPerPage is how many rows a page of the listing holds. Enough to scan
// without scrolling twice, few enough that a register with thousands of
// groups does not send them all to a browser.
const groupsPerPage = 25

func (c *console) listActionQueue(w http.ResponseWriter, r *http.Request) {
	queue, err := c.backend.ListActionQueue(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, queue)
}

func (c *console) acceptAction(w http.ResponseWriter, r *http.Request) {
	reason := decisionReason(r)
	if err := c.backend.AcceptAction(r.Context(), c.actor(r), r.PathValue("id"), reason); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// rejectAction refuses an announcement, which deletes it. The group is
// untouched, and that is worth its own line: refusing what a group announced
// is not refusing the group.
func (c *console) rejectAction(w http.ResponseWriter, r *http.Request) {
	reason := decisionReason(r)
	if err := c.backend.RejectAction(r.Context(), c.actor(r), r.PathValue("id"), reason); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	log.Info().Str("action", r.PathValue("id")).
		Msg("action refused and deleted; the group is unchanged")
	w.WriteHeader(http.StatusNoContent)
}

func (c *console) listGroupRevisionQueue(w http.ResponseWriter, r *http.Request) {
	queue, err := c.backend.ListGroupRevisionQueue(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, queue)
}

func (c *console) acceptGroupRevision(w http.ResponseWriter, r *http.Request) {
	reason := decisionReason(r)
	if err := c.backend.AcceptGroupRevision(r.Context(), c.actor(r), r.PathValue("id"), reason); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// rejectGroupRevision discards an edit. The group itself is untouched, which
// is worth a log line of its own: refusing a change is not refusing a group,
// and the two decisions must never read alike in an incident.
func (c *console) rejectGroupRevision(w http.ResponseWriter, r *http.Request) {
	reason := decisionReason(r)
	if err := c.backend.RejectGroupRevision(r.Context(), c.actor(r), r.PathValue("id"), reason); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	log.Info().Str("group", r.PathValue("id")).
		Msg("edit refused; the group is unchanged and still on the map")
	w.WriteHeader(http.StatusNoContent)
}

func (c *console) groupQueue(w http.ResponseWriter, r *http.Request) {
	data := groupsPage{page: c.newPage(r, "groups.title")}
	if c.siteURL != "" {
		data.SiteBase = c.siteURL + "/groups/"
	}
	c.renderer.Render(w, http.StatusOK, "groups", data)
}

func (c *console) listGroupQueue(w http.ResponseWriter, r *http.Request) {
	queue, err := c.backend.ListGroupQueue(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, queue)
}

func (c *console) acceptGroup(w http.ResponseWriter, r *http.Request) {
	reason := decisionReason(r)
	if err := c.backend.AcceptGroup(r.Context(), c.actor(r), r.PathValue("id"), reason); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// rejectGroup refuses a group, which deletes it along with the address that
// proposed it — so the log line is worth having even though the audit entry
// is written by the backend.
func (c *console) rejectGroup(w http.ResponseWriter, r *http.Request) {
	reason := decisionReason(r)
	if err := c.backend.RejectGroup(r.Context(), c.actor(r), r.PathValue("id"), reason); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	log.Info().Str("group", r.PathValue("id")).Msg("group refused and deleted")
	w.WriteHeader(http.StatusNoContent)
}
