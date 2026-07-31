package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/auth"
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
	Emails      []string        `json:"emails"` // this rule's own recipients; empty = the instance default
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
		Config: cfg, Severity: b.Severity, WebhookID: b.WebhookID, Email: b.Email, Emails: cleanEmails(b.Emails), CooldownSec: b.CooldownSec,
	}
	id, err := s.store.CreateAlertRule(r.Context(), rule)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create rule")
		return
	}
	s.audit(r, "alert_rule.create", b.Name, b.Type)
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

// handleUpdateAlertRule replaces a rule's fields (PUT). Toggling enabled keeps
// using PATCH.
func (s *Server) handleUpdateAlertRule(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
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
		Name: b.Name, Type: b.Type, Target: b.Target, Config: cfg,
		Severity: b.Severity, WebhookID: b.WebhookID, Email: b.Email, Emails: cleanEmails(b.Emails), CooldownSec: b.CooldownSec,
	}
	if err := s.store.UpdateAlertRule(r.Context(), id, rule); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not update rule")
		return
	}
	s.audit(r, "alert_rule.update", b.Name, b.Type)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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

// alertQueryFrom builds the store filter from the request, applying the caller's
// host scope. It writes the error response and returns ok=false when an explicit
// ?host= falls outside that scope.
//
// Shared by listing and bulk acknowledge on purpose: "acknowledge everything I
// can see" must mean exactly what the list showed.
func (s *Server) alertQueryFrom(w http.ResponseWriter, r *http.Request) (store.AlertQuery, bool) {
	q := r.URL.Query()
	// Host scoping goes INTO the query, not over its result: filtering a page
	// after fetching it yields short pages and a total that counts rows the
	// caller isn't allowed to see.
	ids, all := s.visibleHostIDs(r)
	aq := store.AlertQuery{
		Severity:  q.Get("severity"),
		Kind:      q.Get("kind"),
		Container: q.Get("container"),
		Rule:      q.Get("rule"),
		Text:      q.Get("q"),
		Unacked:   q.Get("unacked") == "1",
		Sort:      q.Get("sort"),
		Desc:      q.Get("desc") == "1",
		Limit:     atoiDefault(q.Get("limit"), 50),
		Offset:    atoiDefault(q.Get("offset"), 0),
	}
	if !all {
		aq.HostIDs = ids
	}
	if hv := q.Get("host"); hv != "" {
		if id, err := strconv.ParseInt(hv, 10, 64); err == nil {
			// A requested host still has to survive the scope above.
			if all || containsInt64(ids, id) {
				aq.HostID = &id
			} else {
				writeErr(w, http.StatusForbidden, "your access does not include that host")
				return store.AlertQuery{}, false
			}
		}
	}
	return aq, true
}

func (s *Server) handleListAlertEvents(w http.ResponseWriter, r *http.Request) {
	aq, ok := s.alertQueryFrom(w, r)
	if !ok {
		return
	}

	events, total, err := s.store.ListAlertEvents(r.Context(), aq)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list alerts")
		return
	}

	// Delivery outcomes for the page on screen only — the feed is a list, not a
	// report, and joining every attempt for every event would grow without bound.
	ids64 := make([]int64, 0, len(events))
	for _, e := range events {
		ids64 = append(ids64, e.ID)
	}
	if deliveries, derr := s.store.AlertDeliveriesFor(r.Context(), ids64); derr == nil {
		for i := range events {
			events[i].Deliveries = deliveries[events[i].ID]
		}
	}

	// The unread badge counts what the caller can see, so it never betrays the
	// events their scope hid.
	unackQ := aq
	unackQ.Unacked, unackQ.Limit, unackQ.Offset = true, 1, 0
	unackQ.Severity, unackQ.Kind, unackQ.Container, unackQ.Rule, unackQ.Text = "", "", "", "", ""
	unackQ.HostID = nil
	// The badge means "something is wrong", so it counts only warnings and
	// criticals. That is also why it needs no separate rule for resolved events:
	// a condition ending is emitted as info, so good news can never make the
	// number climb — which would train people to stop reading it.
	unackQ.Severities = []string{"warning", "critical"}
	// A resolved condition is good news, not an outstanding item. Counting it
	// would make the badge climb as problems FIX themselves.
	_, unread, uerr := s.store.ListAlertEvents(r.Context(), unackQ)
	if uerr != nil {
		unread = 0
	}
	// How many of the FILTERED set are still outstanding. The confirm dialog for
	// "acknowledge all" quotes this: quoting `total` instead would promise to act
	// on rows that are already acknowledged, and overstate what the button does.
	outQ := aq
	outQ.Unacked, outQ.Limit, outQ.Offset = true, 1, 0
	_, outstanding, oerr := s.store.ListAlertEvents(r.Context(), outQ)
	if oerr != nil {
		outstanding = 0
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events": events, "unread": unread, "total": total, "outstanding": outstanding,
		"limit": aq.Limit, "offset": aq.Offset,
	})
}

// containsInt64 reports whether ids holds id.
func containsInt64(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// atoiDefault parses n, falling back to def for anything unparseable.
func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return def
	}
	return v
}

// handleAckAllAlertEvents acknowledges everything matching the caller's current
// filter. Scoped to the filter rather than the whole table so the button does
// what the screen in front of the user implies, and the count is returned so the
// UI can say what happened.
func (s *Server) handleAckAllAlertEvents(w http.ResponseWriter, r *http.Request) {
	aq, ok := s.alertQueryFrom(w, r)
	if !ok {
		return
	}
	by := ""
	if claims, ok := auth.ClaimsFrom(r.Context()); ok {
		by = claims.Username
	}
	n, err := s.store.AckMatchingAlertEvents(r.Context(), aq, by)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not acknowledge")
		return
	}
	s.audit(r, "alert.ack-all", strconv.FormatInt(n, 10), "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "acknowledged": n})
}

func (s *Server) handleAckAlertEvent(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	// Record WHO acknowledged: "someone dealt with this" is only actionable if
	// you can go and ask them.
	by := ""
	if claims, ok := auth.ClaimsFrom(r.Context()); ok {
		by = claims.Username
	}
	if err := s.store.AckAlertEvent(r.Context(), id, by); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not acknowledge")
		return
	}
	s.audit(r, "alert.ack", strconv.FormatInt(id, 10), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// cleanEmails drops blanks and anything that isn't an address, so a bad paste
// can't quietly become a recipient that never receives.
func cleanEmails(in []string) []string {
	out := make([]string, 0, len(in))
	for _, e := range in {
		e = strings.TrimSpace(e)
		if e != "" && validEmail(e) {
			out = append(out, e)
		}
	}
	return out
}
