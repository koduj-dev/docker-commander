package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

// handleEvents upgrades to a WebSocket and streams live Docker daemon events as
// JSON text frames (server → browser only), mirroring the pull bridge.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
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

	// Stop streaming if the browser closes the socket.
	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				cancel()
				return
			}
		}
	}()

	err = s.docker.StreamEvents(ctx, hostID, func(e docker.EventMsg) {
		if b, err := json.Marshal(e); err == nil {
			_ = conn.Write(ctx, websocket.MessageText, b)
		}
	})
	if err != nil && ctx.Err() == nil {
		if b, err := json.Marshal(map[string]string{"error": err.Error()}); err == nil {
			_ = conn.Write(ctx, websocket.MessageText, b)
		}
	}
}
