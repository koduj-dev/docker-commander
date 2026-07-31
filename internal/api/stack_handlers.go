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
// directly for local, over SSH for ssh hosts), plus whether the app can write it
// back. The UI needs the reason it can't, not just the fact, so it can say why
// the editor is unavailable instead of silently hiding it.
func (s *Server) handleStackCompose(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	project := chi.URLParam(r, "project")
	path, content, editable, reason, err := s.docker.StackCompose(r.Context(), hostID, project)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "path": path, "content": content,
		"editable": editable, "readOnlyReason": reason,
	})
}

// handleWriteStackCompose replaces a CLI-discovered stack's compose file on its
// host. It does NOT redeploy — writing and applying are separate so an operator
// can save a half-finished edit without restarting anything.
func (s *Server) handleWriteStackCompose(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	project := chi.URLParam(r, "project")
	path, err := s.docker.StackWriteComposeFile(r.Context(), hostID, project, body.Content)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "stack.compose.write", project, path)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": path})
}

// handleRedeployStack runs `docker compose up -d` for the stack in its original
// working directory and returns the CLI output, which carries the warnings
// (orphans, unset variables) an operator needs to see.
func (s *Server) handleRedeployStack(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	project := chi.URLParam(r, "project")
	out, err := s.docker.StackRedeploy(r.Context(), hostID, project)
	if err != nil {
		s.audit(r, "stack.redeploy.failed", project, err.Error())
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "output": out})
		return
	}
	s.audit(r, "stack.redeploy", project, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out})
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
