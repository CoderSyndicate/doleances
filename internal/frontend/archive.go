package frontend

import (
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/snapshot"
)

// archivePage is the public download page.
//
// It is a first-class feature rather than an admin export the public happens
// to be allowed to use: the whole point of the register is that it does not
// depend on this installation, and a mirror nobody knows exists is not a
// mirror.
type archivePage struct {
	page

	// Available is false when no contributions snapshot has been generated
	// yet. The page then explains that rather than offering a dead link.
	Available bool

	Tag         string
	GeneratedAt time.Time
	SizeLabel   string
	Counts      map[string]int

	// ProjectURL is where the source of this software lives, so that somebody
	// holding a copy of the register can also get the thing that reads it.
	// Empty when the operator did not publish a source, and the section is
	// then omitted rather than shown as a dead promise.
	ProjectURL string
}

func (s *site) registerArchiveRoutes(mux *http.ServeMux) {
	mux.Handle("GET /archive", s.localization.Middleware(http.HandlerFunc(s.archive)))
	mux.HandleFunc("GET /archive/download", s.downloadArchive)
}

func (s *site) archive(w http.ResponseWriter, r *http.Request) {
	data := archivePage{page: s.newPage(r, "archive.title")}
	data.ProjectURL = s.project.URL

	if latest, ok := s.latestPublicSnapshot(r); ok {
		data.Available = true
		data.Tag = latest.Tag
		data.GeneratedAt = latest.GeneratedAt
		data.SizeLabel = humanSize(latest.SizeBytes)
		data.Counts = latest.Counts
	}

	s.renderer.Render(w, http.StatusOK, "archive", data)
}

// downloadArchive streams the newest contributions package.
//
// The frontend only ever asks for the contributions kind, and the backend
// refuses to build any other one for it; a visitor cannot name a snapshot, so
// there is no path from here to a package containing personal data.
func (s *site) downloadArchive(w http.ResponseWriter, r *http.Request) {
	latest, ok := s.latestPublicSnapshot(r)
	if !ok {
		http.Error(w, "no archive is available yet", http.StatusNotFound)
		return
	}

	data, err := s.backend.DownloadSnapshot(r.Context(), latest.Tag, "")
	if err != nil {
		log.Error().Err(err).Str("tag", latest.Tag).Msg("cannot fetch the public snapshot")
		http.Error(w, "the archive is temporarily unavailable", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+snapshot.Filename(snapshot.KindContributions, latest.GeneratedAt)+`"`)
	w.Write(data) //nolint:errcheck
}

// latestPublicSnapshot finds the newest contributions package.
//
// It asks the backend for that kind explicitly rather than filtering a full
// listing here: the kind a visitor may have is decided once, in the request,
// not in a condition somebody could later edit out.
func (s *site) latestPublicSnapshot(r *http.Request) (publicSnapshot, bool) {
	catalogue, err := s.backend.ListSnapshots(r.Context(), string(snapshot.KindContributions))
	if err != nil {
		log.Warn().Err(err).Msg("cannot list public snapshots")
		return publicSnapshot{}, false
	}

	for _, item := range catalogue.Snapshots {
		// Belt and braces: the query already narrowed this, and a package
		// carrying personal data must never reach this page even if a future
		// backend change made the filter permissive.
		if !item.Public || item.Kind != string(snapshot.KindContributions) {
			continue
		}
		return publicSnapshot{
			Tag:         item.Tag,
			GeneratedAt: item.GeneratedAt,
			SizeBytes:   item.SizeBytes,
			Counts:      item.Counts,
		}, true
	}
	return publicSnapshot{}, false
}

type publicSnapshot struct {
	Tag         string
	GeneratedAt time.Time
	SizeBytes   int64
	Counts      map[string]int
}

// humanSize renders a byte count for somebody deciding whether to start a
// download on a phone.
func humanSize(bytes int64) string {
	switch {
	case bytes < 1024:
		return strconv.FormatInt(bytes, 10) + " B"
	case bytes < 1024*1024:
		return strconv.FormatInt(bytes/1024, 10) + " KB"
	default:
		return strconv.FormatFloat(float64(bytes)/(1024*1024), 'f', 1, 64) + " MB"
	}
}
