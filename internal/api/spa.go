package api

import (
	"bytes"
	"html"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// spaHandler serves static assets from the embedded build and falls back to
// index.html for client-side routes (so deep links like /containers/abc work).
func (s *Server) spaHandler() http.Handler {
	fileServer := http.FileServer(http.FS(s.webFS))
	index, indexErr := fs.ReadFile(s.webFS, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if _, err := fs.Stat(s.webFS, p); p == "" || p == "index.html" || err != nil {
			// The root, or an unknown path → hand control to the SPA router.
			if indexErr != nil {
				http.NotFound(w, r)
				return
			}
			serveIndex(w, index, requestPrefix(r))
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

// serveIndex writes index.html with a <base href> naming the path prefix the
// UI is served under. The build references its assets relatively, so they
// resolve against it at any depth (a deep link like /dockercmd/containers/abc
// still loads /dockercmd/assets/…), and the app reads it to prefix API calls
// and routes.
//
// The page differs per prefix, which a request header can choose, so it must
// never be cached for anyone else: no-store, and Vary for caches that store
// regardless.
func serveIndex(w http.ResponseWriter, index []byte, prefix string) {
	base := `<base href="` + html.EscapeString(prefix+"/") + `">`
	page := bytes.Replace(index, []byte("<head>"), []byte("<head>\n    "+base), 1)
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Add("Vary", forwardedPrefixHeader)
	_, _ = w.Write(page)
}
