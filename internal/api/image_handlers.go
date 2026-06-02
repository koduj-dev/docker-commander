package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

func (s *Server) handleListImages(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	imgs, err := s.docker.ListImages(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, imgs)
}

func (s *Server) handleRemoveImage(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		writeErr(w, http.StatusBadRequest, "ref is required")
		return
	}
	force := r.URL.Query().Get("force") == "1"
	changed, err := s.docker.RemoveImage(r.Context(), hostID, ref, force)
	if err != nil {
		// Surface the daemon's "image is in use" conflict so the UI can offer
		// a force retry instead of treating it as a hard failure.
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "image.remove", ref, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changed": changed})
}

func (s *Server) handlePruneImages(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	res, err := s.docker.PruneImages(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "docker error: "+err.Error())
		return
	}
	s.audit(r, "image.prune", "", "")
	writeJSON(w, http.StatusOK, res)
}

// handlePullImage upgrades to a WebSocket and streams pull progress as JSON
// text frames (server → browser only), mirroring the exec bridge. The image
// reference comes from the "ref" query parameter.
func (s *Server) handlePullImage(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		writeErr(w, http.StatusBadRequest, "ref is required")
		return
	}

	opts := &websocket.AcceptOptions{}
	if s.cfg.Dev {
		opts.InsecureSkipVerify = true
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer conn.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	send := func(p any) {
		if b, err := json.Marshal(p); err == nil {
			_ = conn.Write(ctx, websocket.MessageText, b)
		}
	}

	s.audit(r, "image.pull", ref, "")
	err = s.docker.PullImage(ctx, hostID, ref, func(p docker.PullProgress) {
		send(p)
	})
	if err != nil {
		send(map[string]any{"error": err.Error()})
		return
	}
	send(map[string]any{"done": true, "status": "Pull complete"})
}
