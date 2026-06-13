package api

import (
	"net/http"

	"github.com/coder/websocket"

	"github.com/koduj-dev/docker-commander/internal/auth"
)

// handleWebSocket upgrades the connection and hands it to the hub. The request
// has already passed RequireSession middleware (cookie or ?token=), so an
// authenticated session is guaranteed here. The hub stream is itself RBAC-gated:
// each subscription's channel maps to a section, checked against the user's live
// permissions, so a user can only stream stats/logs for sections they may see.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	u, err := s.store.UserByID(r.Context(), claims.UserID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	opts := &websocket.AcceptOptions{}
	if s.cfg.Dev {
		// In dev the frontend runs on a different origin (Vite); allow it.
		opts.InsecureSkipVerify = true
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	// Stream frames are gated per channel against the user's live RBAC. An
	// unrecognised channel is denied outright (fail closed).
	allow := func(channel string) bool {
		section, ok := wsChannelSection(channel)
		if !ok {
			return false
		}
		return s.checkAccess(r.Context(), u, section, false) == nil
	}
	// Serve blocks until the client disconnects.
	s.hub.Serve(r.Context(), conn, allow)
	_ = conn.CloseNow()
}
