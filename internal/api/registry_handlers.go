package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// registryBody is the create payload for a registry credential.
type registryBody struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Username string `json:"username"`
	Secret   string `json:"secret"`
}

func (s *Server) handleListRegistries(w http.ResponseWriter, r *http.Request) {
	regs, err := s.store.ListRegistries(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list registries")
		return
	}
	out := make([]map[string]any, 0, len(regs))
	for _, rg := range regs {
		// Never leak the secret; only metadata.
		out = append(out, map[string]any{
			"id": rg.ID, "name": rg.Name, "address": rg.Address, "username": rg.Username,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateRegistry(w http.ResponseWriter, r *http.Request) {
	var b registryBody
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if b.Name == "" || b.Address == "" {
		writeErr(w, http.StatusBadRequest, "name and address are required")
		return
	}
	id, err := s.store.CreateRegistry(r.Context(), b.Name, b.Address, b.Username, b.Secret)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create registry")
		return
	}
	// Audit without the secret or address detail beyond the host.
	s.audit(r, "registry.create", b.Name, b.Address)
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (s *Server) handleDeleteRegistry(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := s.store.DeleteRegistry(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not delete registry")
		return
	}
	s.audit(r, "registry.delete", chi.URLParam(r, "id"), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTestRegistry verifies the stored credentials against the registry by
// asking the daemon to log in.
func (s *Server) handleTestRegistry(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	auth, err := s.store.AuthByID(r.Context(), id)
	if err != nil {
		if err == store.ErrNotFound {
			writeErr(w, http.StatusNotFound, "registry not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not load registry")
		return
	}
	if err := s.docker.RegistryLogin(r.Context(), hostID, *auth); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
