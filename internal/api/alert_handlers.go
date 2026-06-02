package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// ---- Webhooks ---------------------------------------------------------------

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	hooks, err := s.store.ListWebhooks(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list webhooks")
		return
	}
	if hooks == nil {
		hooks = []store.Webhook{}
	}
	writeJSON(w, http.StatusOK, hooks)
}

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	var wh store.Webhook
	if err := decodeJSON(r, &wh); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if wh.Name == "" || wh.URL == "" {
		writeErr(w, http.StatusBadRequest, "name and url are required")
		return
	}
	id, err := s.store.CreateWebhook(r.Context(), &wh)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create webhook")
		return
	}
	s.audit(r, "webhook.create", wh.Name, wh.URL)
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := s.store.DeleteWebhook(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not delete webhook")
		return
	}
	s.audit(r, "webhook.delete", chi.URLParam(r, "id"), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- Alert rules ------------------------------------------------------------

// alertRuleBody mirrors store.AlertRule but takes config as raw JSON so the
// type-specific shape passes through untouched.
type alertRuleBody struct {
	Name        string          `json:"name"`
	Enabled     bool            `json:"enabled"`
	Type        string          `json:"type"`
	Target      string          `json:"target"`
	Config      json.RawMessage `json:"config"`
	Severity    string          `json:"severity"`
	WebhookID   *int64          `json:"webhookId"`
	Email       bool            `json:"email"`
	CooldownSec int             `json:"cooldownSec"`
}

func (s *Server) handleListAlertRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.store.ListAlertRules(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list rules")
		return
	}
	if rules == nil {
		rules = []store.AlertRule{}
	}
	writeJSON(w, http.StatusOK, rules)
}

func (s *Server) handleCreateAlertRule(w http.ResponseWriter, r *http.Request) {
	var b alertRuleBody
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if b.Name == "" || b.Type == "" {
		writeErr(w, http.StatusBadRequest, "name and type are required")
		return
	}
	cfg := string(b.Config)
	if cfg == "" {
		cfg = "{}"
	}
	rule := &store.AlertRule{
		Name: b.Name, Enabled: b.Enabled, Type: b.Type, Target: b.Target,
		Config: cfg, Severity: b.Severity, WebhookID: b.WebhookID, Email: b.Email, CooldownSec: b.CooldownSec,
	}
	id, err := s.store.CreateAlertRule(r.Context(), rule)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create rule")
		return
	}
	s.audit(r, "alert_rule.create", b.Name, b.Type)
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (s *Server) handleToggleAlertRule(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.store.SetAlertRuleEnabled(r.Context(), id, body.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not update rule")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteAlertRule(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := s.store.DeleteAlertRule(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not delete rule")
		return
	}
	s.audit(r, "alert_rule.delete", chi.URLParam(r, "id"), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- Alert events (in-app feed) ---------------------------------------------

func (s *Server) handleListAlertEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.ListAlertEvents(r.Context(), 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list alerts")
		return
	}
	if events == nil {
		events = []store.AlertEvent{}
	}
	unread, _ := s.store.CountUnacknowledged(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "unread": unread})
}

func (s *Server) handleAckAlertEvent(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := s.store.AckAlertEvent(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not acknowledge")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
