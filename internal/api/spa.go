package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// spaHandler serves static assets from the embedded build and falls back to
// index.html for client-side routes (so deep links like /containers/abc work).
func (s *Server) spaHandler() http.Handler {
	fileServer := http.FileServer(http.FS(s.webFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(s.webFS, p); err != nil {
			// Unknown path → hand control to the SPA router via index.html.
			r2 := new(http.Request)
			*r2 = *r
			r2.URL.Path = "/"
			http.ServeFileFS(w, r2, s.webFS, "index.html")
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
