package api

import (
	"net/http"
	"net/http/pprof"

	"github.com/go-chi/chi/v5"
)

// mountPProf wires Go's net/http/pprof endpoints under /debug/pprof, gated to
// loopback clients. It is only called when DC_PPROF is set. The endpoints leak
// goroutine stacks, heap layout and allocation detail, so they are never exposed
// off-box: operators capture a profile through an SSH tunnel, e.g.
//
//	go tool pprof http://127.0.0.1:8470/debug/pprof/profile?seconds=30
func (s *Server) mountPProf(r chi.Router) {
	r.Route("/debug/pprof", func(r chi.Router) {
		r.Use(loopbackOnly)
		// The index and the named profiles (heap, goroutine, allocs, …) are all
		// served by pprof.Index, which routes on the trailing path segment.
		r.HandleFunc("/", pprof.Index)
		r.HandleFunc("/{profile}", pprof.Index)
		// These four have dedicated handlers (streaming / query-param driven).
		r.HandleFunc("/cmdline", pprof.Cmdline)
		r.HandleFunc("/profile", pprof.Profile)
		r.HandleFunc("/symbol", pprof.Symbol)
		r.HandleFunc("/trace", pprof.Trace)
	})
}

// loopbackOnly rejects any request that doesn't originate from the loopback
// interface with a 404 (not 403, so the path's existence isn't advertised).
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
