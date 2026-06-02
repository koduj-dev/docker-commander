package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleListVolumes(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	vols, err := s.docker.ListVolumes(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, vols)
}

type volumeBody struct {
	Name   string            `json:"name"`
	Driver string            `json:"driver"`
	Labels map[string]string `json:"labels"`
}

func (s *Server) handleCreateVolume(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	var b volumeBody
	if err := decodeJSON(r, &b); err != nil || b.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	driver := b.Driver
	if driver == "" {
		driver = "local"
	}
	v, err := s.docker.CreateVolume(r.Context(), hostID, b.Name, driver, b.Labels)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "volume.create", b.Name, driver)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "volume": v})
}

func (s *Server) handleRemoveVolume(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	name := chi.URLParam(r, "name")
	force := r.URL.Query().Get("force") == "1"
	if err := s.docker.RemoveVolume(r.Context(), hostID, name, force); err != nil {
		// The daemon rejects volumes still mounted by a container; surface it.
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "volume.remove", name, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handlePruneVolumes(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	res, err := s.docker.PruneVolumes(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	s.audit(r, "volume.prune", "", "")
	writeJSON(w, http.StatusOK, res)
}
