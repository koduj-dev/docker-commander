package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
)

// execControl is a text control frame from the browser (terminal resize).
type execControl struct {
	Type string `json:"type"` // "resize"
	Cols uint   `json:"cols"`
	Rows uint   `json:"rows"`
}

// handleExec upgrades to a WebSocket and bridges it to a container exec TTY:
//   - browser binary frames  -> exec stdin
//   - exec output            -> browser binary frames
//   - browser text frames    -> JSON control (resize)
//
// It is host-agnostic: the exec runs through the Docker API for whichever host
// is selected, so it works the same for local and remote daemons.
func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	hostID, err := s.resolveHostID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no host configured")
		return
	}
	id := chi.URLParam(r, "id")

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

	sess, err := s.docker.ExecAttach(ctx, hostID, id, nil, 80, 24)
	if err != nil {
		_ = conn.Write(ctx, websocket.MessageText, []byte("\r\n\x1b[31mfailed to start shell: "+err.Error()+"\x1b[0m\r\n"))
		return
	}
	defer sess.Close()
	s.audit(r, "container.exec", id, "interactive shell")

	// Pump container output -> browser.
	go func() {
		defer cancel()
		buf := make([]byte, 32*1024)
		for {
			n, err := sess.Read(buf)
			if n > 0 {
				if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Pump browser -> container stdin / handle resize control frames.
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		switch typ {
		case websocket.MessageBinary:
			if _, err := sess.Write(data); err != nil {
				return
			}
		case websocket.MessageText:
			var ctrl execControl
			if json.Unmarshal(data, &ctrl) == nil && ctrl.Type == "resize" {
				_ = sess.Resize(ctx, ctrl.Cols, ctrl.Rows)
			}
		}
	}
}
