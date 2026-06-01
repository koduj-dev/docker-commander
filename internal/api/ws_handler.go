package api

import (
	"net/http"

	"github.com/coder/websocket"
)

// handleWebSocket upgrades the connection and hands it to the hub. The request
// has already passed RequireSession middleware (cookie or ?token=), so an
// authenticated session is guaranteed here.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	opts := &websocket.AcceptOptions{}
	if s.cfg.Dev {
		// In dev the frontend runs on a different origin (Vite); allow it.
		opts.InsecureSkipVerify = true
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	// Serve blocks until the client disconnects.
	s.hub.Serve(r.Context(), conn)
	_ = conn.CloseNow()
}
