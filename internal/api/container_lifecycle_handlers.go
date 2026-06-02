package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

func (s *Server) handleCreateContainer(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	var spec docker.CreateSpec
	if err := decodeJSON(r, &spec); err != nil || spec.Image == "" {
		writeErr(w, http.StatusBadRequest, "image is required")
		return
	}
	id, err := s.docker.CreateContainer(r.Context(), hostID, spec)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "container.create", spec.Name, spec.Image)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

func (s *Server) handleRenameContainer(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	id := chi.URLParam(r, "id")
	var b struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &b); err != nil || b.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := s.docker.RenameContainer(r.Context(), hostID, id, b.Name); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "container.rename", id, b.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleUpdateContainer(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	id := chi.URLParam(r, "id")
	var b struct {
		Memory        int64  `json:"memory"`
		NanoCPUs      int64  `json:"nanoCpus"`
		RestartPolicy string `json:"restartPolicy"`
	}
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.docker.UpdateContainer(r.Context(), hostID, id, b.Memory, b.NanoCPUs, b.RestartPolicy); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "container.update", id, b.RestartPolicy)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleCommitContainer(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	id := chi.URLParam(r, "id")
	var b struct {
		Ref     string `json:"ref"`
		Comment string `json:"comment"`
	}
	if err := decodeJSON(r, &b); err != nil || b.Ref == "" {
		writeErr(w, http.StatusBadRequest, "ref is required")
		return
	}
	imageID, err := s.docker.CommitContainer(r.Context(), hostID, id, b.Ref, b.Comment)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "container.commit", id, b.Ref)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "imageId": imageID})
}
