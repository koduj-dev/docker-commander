package api

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// secretNameRE is a valid compose/env identifier: letters, digits and
// underscore, not starting with a digit — anything both docker-compose's
// ${NAME} interpolation and a real process environment variable can accept.
var secretNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// handleListProjectSecrets lists a project's secrets — name and metadata
// only, never a value. RBAC and project resolution are handled by
// loadProject, exactly like every other /projects/{id}/... route: a read
// grant on the "projects" section can list, a write grant is required to
// create/update/delete (see the other handlers below).
func (s *Server) handleListProjectSecrets(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	secrets, err := s.store.ListProjectSecrets(r.Context(), p.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list secrets")
		return
	}
	out := make([]map[string]any, 0, len(secrets))
	for _, sec := range secrets {
		out = append(out, map[string]any{
			"id": sec.ID, "name": sec.Name, "createdBy": sec.CreatedBy,
			"createdAt": sec.CreatedAt, "updatedAt": sec.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateProjectSecret adds a new named secret to a project. The value
// is written once, encrypted at rest, and never returned again — see
// handleUpdateProjectSecret to replace it.
func (s *Server) handleCreateProjectSecret(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r) // POST is a write request; loadProject requires the write grant
	if !ok {
		return
	}
	var b struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if !secretNameRE.MatchString(b.Name) {
		writeErr(w, http.StatusBadRequest, "name must be a valid identifier (letters, digits, underscore; not starting with a digit)")
		return
	}
	if b.Value == "" {
		writeErr(w, http.StatusBadRequest, "value is required")
		return
	}
	id, err := s.store.CreateProjectSecret(r.Context(), p.ID, b.Name, b.Value, currentUsername(r))
	if errors.Is(err, store.ErrDuplicate) {
		writeErr(w, http.StatusConflict, "a secret with this name already exists")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create secret")
		return
	}
	s.audit(r, "project.secret.create", p.Slug, b.Name)
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

// handleUpdateProjectSecret replaces a secret's value. The name is
// immutable — delete and recreate to rename.
func (s *Server) handleUpdateProjectSecret(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	var b struct {
		Value string `json:"value"`
	}
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if b.Value == "" {
		writeErr(w, http.StatusBadRequest, "value is required")
		return
	}
	err := s.store.UpdateProjectSecretValue(r.Context(), p.ID, name, b.Value)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "secret not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not update secret")
		return
	}
	s.audit(r, "project.secret.update", p.Slug, name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDeleteProjectSecret removes one secret from a project.
func (s *Server) handleDeleteProjectSecret(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	err := s.store.DeleteProjectSecret(r.Context(), p.ID, name)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "secret not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not delete secret")
		return
	}
	s.audit(r, "project.secret.delete", p.Slug, name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
