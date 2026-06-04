package api

import (
	"context"
	"net/http"
	"time"
)

// handleHealthz is an unauthenticated liveness/readiness probe for load
// balancers, uptime checks and k8s. It reports the build version and verifies
// the database is reachable (503 if not).
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "error", "error": "database unreachable", "version": s.cfg.Version,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": s.cfg.Version})
}

// handleVersion returns the running build version (for the UI footer).
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": s.cfg.Version})
}
