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

// scopedHostID resolves the {id} path parameter and authorizes the caller
// against that host.
//
// These routes name the host in the PATH, not in ?host=, so the permissions
// middleware authorized the `hosts` section against host 0 — which every grant
// satisfies. Without this, a role scoped to one host could rename, disable or
// delete another, and worse: /trust pins a new SSH host key, so an out-of-scope
// caller could vouch for a key the operator never saw. Host ids are sequential.
//
// Out of scope answers like missing, so the id space cannot be walked to learn
// which hosts exist.
func (s *Server) scopedHostID(w http.ResponseWriter, r *http.Request, write bool) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid host id")
		return 0, false
	}
	// Out of reach answers like missing (see loadProject for why); visible but
	// read-only answers 403, so an operator who can see the host isn't told it
	// disappeared.
	if err := s.requireSectionOnHost(r, "hosts", false, id); err != nil {
		writeErr(w, http.StatusNotFound, "host not found")
		return 0, false
	}
	if write {
		if err := s.requireSectionOnHost(r, "hosts", true, id); err != nil {
			writeErr(w, http.StatusForbidden, "read-only")
			return 0, false
		}
	}
	return id, true
}

// handleUpdateHost updates a host's per-host alert email override and/or its
// disabled flag (fields are optional — only those present are changed).
func (s *Server) handleUpdateHost(w http.ResponseWriter, r *http.Request) {
	id, ok := s.scopedHostID(w, r, true)
	if !ok {
		return
	}
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
	id, ok := s.scopedHostID(w, r, true)
	if !ok {
		return
	}
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
	id, ok := s.scopedHostID(w, r, false)
	if !ok {
		return
	}
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
	// A write: pinning a host key is an authority decision about that host.
	id, ok := s.scopedHostID(w, r, true)
	if !ok {
		return
	}

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
