package web

import (
	"io/fs"
	"net/http"
	"strings"
)

// StaticHandler serves embedded assets.
//
// The files are compiled into the binary, so the running container needs no
// writable path and no assets directory to mount — which is what lets the
// exposed services run on a read-only root filesystem.
func StaticHandler(fsys fs.FS, dir string) (http.Handler, error) {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		return nil, err
	}

	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Embedded assets change only when the binary does, so they are safe
		// to cache for a long time — but a request for a directory listing is
		// not something to serve at all.
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		files.ServeHTTP(w, r)
	}), nil
}
