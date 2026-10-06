package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/store"
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
	if msg := s.managedByProject(r.Context(), hostID, project); msg != "" {
		editable, reason = false, msg
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
	if msg := s.managedByProject(r.Context(), hostID, project); msg != "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": msg})
		return
	}
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
	if msg := s.managedByProject(r.Context(), hostID, project); msg != "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": msg})
		return
	}
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
	release, err := s.beginStackOp(r.Context(), hostID, project, "a stack "+action)
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errBusy) {
			code = http.StatusConflict
		}
		writeErr(w, code, err.Error())
		return
	}
	defer release()
	if err := s.docker.StackAction(r.Context(), hostID, project, action); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "stack."+action, project, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// managedByProject returns why a stack can't be edited here because a Project
// owns it, or "" when none does. A project deploys as the Compose project named
// by its slug, on its target host (0 = the local daemon). Editing that stack's
// file here would bypass the project: no revision, no policy check, and the next
// project deploy would overwrite the change anyway.
func (s *Server) managedByProject(ctx context.Context, hostID int64, stack string) string {
	p, err := s.stackProject(ctx, hostID, stack)
	if err != nil {
		return "could not check whether a project owns this stack"
	}
	if p != nil {
		return fmt.Sprintf("this stack belongs to the project %q; edit and deploy it in Projects", p.Name)
	}
	return ""
}

// stackProject returns the managed project that deploys a stack: the Compose
// project named by its slug, on the same host (0 = the local daemon). nil when
// no project does.
func (s *Server) stackProject(ctx context.Context, hostID int64, stack string) (*store.Project, error) {
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	host, err := s.docker.ResolveHostID(ctx, hostID)
	if err != nil {
		return nil, err
	}
	for i := range projects {
		p := projects[i]
		if p.Slug != stack {
			continue
		}
		ph, err := s.docker.ResolveHostID(ctx, p.HostID)
		if err == nil && ph == host {
			return &p, nil
		}
	}
	return nil, nil
}
