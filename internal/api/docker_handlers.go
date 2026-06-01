package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.store.ListHosts(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list hosts")
		return
	}
	// Shape a safe view; never leak TLS key material to the client.
	out := make([]map[string]any, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, map[string]any{
			"id": h.ID, "name": h.Name, "kind": h.Kind, "address": h.Address,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListContainers(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	list, err := s.docker.ListContainers(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleInspectContainer(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	detail, err := s.docker.InspectContainer(r.Context(), hostID, chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleContainerAction(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	id := chi.URLParam(r, "id")
	action := chi.URLParam(r, "action")
	if err := s.docker.ContainerAction(r.Context(), hostID, id, action); err != nil {
		if err == docker.ErrUnknownAction {
			writeErr(w, http.StatusBadRequest, "unknown action")
			return
		}
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	s.audit(r, "container."+action, id, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleListNetworks(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	nets, err := s.docker.ListNetworks(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, nets)
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	info, err := s.docker.SystemInfo(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.RecentAudit(r.Context(), 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not read audit log")
		return
	}
	writeJSON(w, http.StatusOK, entries)
}
