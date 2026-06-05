package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// handleListStacks returns the Compose stacks on the selected host (containers
// grouped by their compose project label — including stacks started by the
// `docker compose` CLI).
func (s *Server) handleListStacks(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	stacks, err := s.docker.ListStacks(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stacks)
}

// handleStackCompose returns the stack's compose file (read from the host —
// directly for local, over SSH for ssh hosts).
func (s *Server) handleStackCompose(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	project := chi.URLParam(r, "project")
	path, content, err := s.docker.StackComposeFile(r.Context(), hostID, project)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": path, "content": content})
}

// handleStackAction applies a lifecycle action (start / stop / restart /
// remove) to a whole stack.
func (s *Server) handleStackAction(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	project := chi.URLParam(r, "project")
	action := chi.URLParam(r, "action")
	if err := s.docker.StackAction(r.Context(), hostID, project, action); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "stack."+action, project, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
