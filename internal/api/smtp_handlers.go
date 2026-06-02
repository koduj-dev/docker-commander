package api

import (
	"net/http"

	"github.com/koduj-dev/docker-commander/internal/monitor"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// handleGetSMTP returns the SMTP config with the password masked (presence only).
func (s *Server) handleGetSMTP(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetSMTP(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not load smtp config")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"host": cfg.Host, "port": cfg.Port, "username": cfg.Username,
		"from": cfg.From, "to": cfg.To, "tls": cfg.TLS,
		"hasPassword": cfg.Password != "",
	})
}

// handleSetSMTP saves the SMTP config. An empty password keeps the stored one.
func (s *Server) handleSetSMTP(w http.ResponseWriter, r *http.Request) {
	var c store.SMTPConfig
	if err := decodeJSON(r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.store.SetSMTP(r.Context(), c); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save smtp config")
		return
	}
	s.audit(r, "smtp.configure", c.Host, c.To)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTestSMTP sends a test email using the stored config (merged with any
// fields supplied in the body, so the user can test before saving).
func (s *Server) handleTestSMTP(w http.ResponseWriter, r *http.Request) {
	stored, _ := s.store.GetSMTP(r.Context())
	var c store.SMTPConfig
	_ = decodeJSON(r, &c)
	// An empty/blank body means "test the saved config"; a populated body lets the
	// user test edits before saving. A blank password always falls back to stored.
	if c.Host == "" && c.From == "" && c.To == "" {
		c = stored
	} else if c.Password == "" {
		c.Password = stored.Password
	}
	if !c.Configured() {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "host, from and to are required"})
		return
	}
	if err := monitor.SendMail(c, "Docker Commander test email", "This is a test message from Docker Commander.\n"); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
