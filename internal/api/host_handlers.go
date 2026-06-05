package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// hostBody is the create payload for a Docker host.
type hostBody struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`    // local | tcp | ssh
	Address    string `json:"address"` // tcp://host:2376  |  user@host[:port]
	TLSCA      string `json:"tlsCa"`
	TLSCert    string `json:"tlsCert"`
	TLSKey     string `json:"tlsKey"`
	AlertEmail string `json:"alertEmail"`
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
		TLSCA: b.TLSCA, TLSCert: b.TLSCert, TLSKey: b.TLSKey, AlertEmail: b.AlertEmail,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create host")
		return
	}
	s.audit(r, "host.create", b.Name, b.Kind+" "+b.Address)
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

// handleUpdateHost updates a host's per-host alert email override and/or its
// disabled flag (fields are optional — only those present are changed).
func (s *Server) handleUpdateHost(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var b struct {
		AlertEmail *string `json:"alertEmail"`
		Disabled   *bool   `json:"disabled"`
	}
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if b.AlertEmail != nil {
		if err := s.store.SetHostAlertEmail(r.Context(), id, *b.AlertEmail); err != nil {
			writeErr(w, http.StatusInternalServerError, "could not update host")
			return
		}
		s.audit(r, "host.update", chi.URLParam(r, "id"), *b.AlertEmail)
	}
	if b.Disabled != nil {
		if err := s.store.SetHostDisabled(r.Context(), id, *b.Disabled); err != nil {
			writeErr(w, http.StatusInternalServerError, "could not update host")
			return
		}
		// Drop the cached client/SSH connection so nothing keeps hitting a
		// disabled host; the monitor stops watching it on its next reconcile.
		s.docker.Disconnect(id)
		action := "host.enable"
		if *b.Disabled {
			action = "host.disable"
		}
		s.audit(r, action, chi.URLParam(r, "id"), "")
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
		// An untrusted/changed SSH host key is reported structurally so the UI
		// can show the fingerprint and offer an explicit "trust" affordance.
		var unknown *docker.HostKeyUnknownError
		if errors.As(err, &unknown) {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": false, "untrusted": true,
				"fingerprint": unknown.Fingerprint, "keyType": unknown.KeyType,
				"error": unknown.Error(),
			})
			return
		}
		var mismatch *docker.HostKeyMismatchError
		if errors.As(err, &mismatch) {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": false, "mismatch": true,
				"fingerprint": mismatch.Fingerprint,
				"error":       mismatch.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "serverVersion": info.ServerVersion, "containersRunning": info.ContainersRunning})
}

// trustBody optionally carries the fingerprint the operator reviewed, so the
// server can confirm the host still presents that exact key before pinning it.
type trustBody struct {
	Fingerprint string `json:"fingerprint"`
}

// handleTrustHost pins the SSH host's current public key after explicit operator
// approval (trust-on-first-use). The key is captured server-side; if the caller
// passed the fingerprint they reviewed, it must still match — otherwise the host
// swapped keys between review and trust and we refuse.
func (s *Server) handleTrustHost(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)

	var b trustBody
	_ = decodeJSON(r, &b) // body is optional

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	keyLine, fingerprint, err := s.docker.ProbeHostKey(ctx, id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if b.Fingerprint != "" && b.Fingerprint != fingerprint {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "mismatch": true, "fingerprint": fingerprint,
			"error": "host key changed since you reviewed it — not trusting",
		})
		return
	}
	if err := s.store.SetHostKey(ctx, id, keyLine); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not store host key")
		return
	}
	s.docker.Disconnect(id) // force reconnect with the freshly pinned key
	s.audit(r, "host.trust", chi.URLParam(r, "id"), fingerprint)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "fingerprint": fingerprint})
}
