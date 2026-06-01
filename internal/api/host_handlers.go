package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// hostBody is the create payload for a Docker host.
type hostBody struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`    // local | tcp | ssh
	Address string `json:"address"` // tcp://host:2376  |  user@host[:port]
	TLSCA   string `json:"tlsCa"`
	TLSCert string `json:"tlsCert"`
	TLSKey  string `json:"tlsKey"`
}

func (s *Server) handleCreateHost(w http.ResponseWriter, r *http.Request) {
	var b hostBody
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if b.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	switch b.Kind {
	case "tcp", "ssh":
		if b.Address == "" {
			writeErr(w, http.StatusBadRequest, "address is required for tcp/ssh hosts")
			return
		}
	case "local":
	default:
		writeErr(w, http.StatusBadRequest, "kind must be local, tcp or ssh")
		return
	}

	id, err := s.store.CreateHost(r.Context(), &store.Host{
		Name: b.Name, Kind: b.Kind, Address: b.Address,
		TLSCA: b.TLSCA, TLSCert: b.TLSCert, TLSKey: b.TLSKey,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create host")
		return
	}
	s.audit(r, "host.create", b.Name, b.Kind+" "+b.Address)
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := s.store.DeleteHost(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not delete host")
		return
	}
	s.docker.Disconnect(id)
	s.audit(r, "host.delete", chi.URLParam(r, "id"), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTestHost checks whether a host is reachable by fetching system info.
func (s *Server) handleTestHost(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	// Bound the probe so an unreachable host fails fast instead of hanging.
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	info, err := s.docker.SystemInfo(ctx, id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "serverVersion": info.ServerVersion, "containersRunning": info.ContainersRunning})
}
