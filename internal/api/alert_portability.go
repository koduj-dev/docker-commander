package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Alert-rule import/export. Rules are exported as a portable JSON bundle so they
// can be version-controlled or moved between instances. Webhooks are referenced
// by NAME, never by their internal ID, and their URLs/headers/secrets are never
// included — the bundle carries rule definitions only, so it is safe to share
// and an import can never smuggle in a new webhook destination (which would be an
// SSRF / exfiltration vector). On import, a webhook name is re-linked only if a
// webhook with that exact name already exists locally; otherwise the rule is
// imported with no destination and a warning is returned.

const (
	alertExportVersion = 1
	maxImportRules     = 1000
	maxRuleConfigBytes = 16 * 1024
	maxRuleNameLen     = 200
	maxRuleTargetLen   = 200
	maxCooldownSec     = 24 * 60 * 60
)

var (
	validAlertTypes = map[string]bool{"state": true, "resource": true, "log": true, "restart": true}
	validSeverities = map[string]bool{"info": true, "warning": true, "critical": true}
)

// portableRule is one alert rule in an export bundle. It deliberately omits the
// instance-specific id and createdAt, and references the webhook by name.
type portableRule struct {
	Name        string          `json:"name"`
	Enabled     bool            `json:"enabled"`
	Type        string          `json:"type"`
	Target      string          `json:"target"`
	Config      json.RawMessage `json:"config"`
	Severity    string          `json:"severity"`
	Webhook     string          `json:"webhook"` // webhook name; "" = no destination
	Email       bool            `json:"email"`
	CooldownSec int             `json:"cooldownSec"`
}

// alertRuleBundle is the export envelope.
type alertRuleBundle struct {
	Version    int            `json:"version"`
	ExportedAt string         `json:"exportedAt"`
	Rules      []portableRule `json:"rules"`
}

// handleExportAlertRules streams all alert rules as a portable JSON bundle.
func (s *Server) handleExportAlertRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.store.ListAlertRules(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list rules")
		return
	}
	hooks, err := s.store.ListWebhooks(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list webhooks")
		return
	}
	nameByID := make(map[int64]string, len(hooks))
	for _, h := range hooks {
		nameByID[h.ID] = h.Name
	}

	bundle := alertRuleBundle{
		Version:    alertExportVersion,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Rules:      make([]portableRule, 0, len(rules)),
	}
	for _, rule := range rules {
		cfg := rule.Config
		if cfg == "" || !json.Valid([]byte(cfg)) {
			cfg = "{}"
		}
		pr := portableRule{
			Name:        rule.Name,
			Enabled:     rule.Enabled,
			Type:        rule.Type,
			Target:      rule.Target,
			Config:      json.RawMessage(cfg),
			Severity:    rule.Severity,
			Email:       rule.Email,
			CooldownSec: rule.CooldownSec,
		}
		if rule.WebhookID != nil {
			pr.Webhook = nameByID[*rule.WebhookID]
		}
		bundle.Rules = append(bundle.Rules, pr)
	}

	s.audit(r, "alert_rules.export", strconv.Itoa(len(bundle.Rules)), "")
	w.Header().Set("Content-Disposition", `attachment; filename="alert-rules.json"`)
	writeJSON(w, http.StatusOK, bundle)
}

// handleImportAlertRules creates alert rules from an uploaded bundle. Each rule
// is validated and normalised; invalid rules are skipped (reported as warnings)
// rather than failing the whole import. Imported rules are always created anew —
// the import never overwrites or deletes existing rules.
func (s *Server) handleImportAlertRules(w http.ResponseWriter, r *http.Request) {
	var bundle alertRuleBundle
	// decodeJSON rejects unknown fields, so a bundle written by a NEWER version
	// that adds fields will fail here with "invalid bundle" rather than the
	// version message below. That is acceptable today (one version); when the
	// schema grows, bump alertExportVersion and decode leniently per version.
	if err := decodeJSON(r, &bundle); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid bundle")
		return
	}
	if bundle.Version != alertExportVersion {
		writeErr(w, http.StatusBadRequest, "unsupported export version")
		return
	}
	if len(bundle.Rules) == 0 {
		writeErr(w, http.StatusBadRequest, "the bundle contains no rules")
		return
	}
	if len(bundle.Rules) > maxImportRules {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("too many rules (max %d)", maxImportRules))
		return
	}

	hooks, err := s.store.ListWebhooks(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list webhooks")
		return
	}
	idByName := make(map[string]int64, len(hooks))
	for _, h := range hooks {
		idByName[h.Name] = h.ID
	}

	var warnings []string
	imported := 0
	for i, pr := range bundle.Rules {
		rule, warn, err := normalizeImportedRule(pr, idByName)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("rule %d (%q) skipped: %s", i+1, pr.Name, err))
			continue
		}
		if warn != "" {
			warnings = append(warnings, warn)
		}
		if _, err := s.store.CreateAlertRule(r.Context(), rule); err != nil {
			warnings = append(warnings, fmt.Sprintf("rule %d (%q) skipped: could not save", i+1, rule.Name))
			continue
		}
		imported++
	}

	s.audit(r, "alert_rules.import", strconv.Itoa(imported), strconv.Itoa(len(bundle.Rules)))
	writeJSON(w, http.StatusOK, map[string]any{"imported": imported, "warnings": warnings})
}

// normalizeImportedRule validates and normalises one imported rule. It returns
// the rule to create, an optional non-fatal warning, and a fatal error (rule
// skipped). Untrusted input from the bundle is fully validated here.
func normalizeImportedRule(pr portableRule, webhookIDByName map[string]int64) (*store.AlertRule, string, error) {
	name := strings.TrimSpace(pr.Name)
	if name == "" {
		return nil, "", errors.New("name is required")
	}
	if len(name) > maxRuleNameLen {
		return nil, "", errors.New("name is too long")
	}
	if !validAlertTypes[pr.Type] {
		return nil, "", fmt.Errorf("unknown type %q", pr.Type)
	}
	sev := pr.Severity
	if sev == "" {
		sev = "warning"
	}
	if !validSeverities[sev] {
		return nil, "", fmt.Errorf("unknown severity %q", pr.Severity)
	}
	if len(pr.Target) > maxRuleTargetLen {
		return nil, "", errors.New("target is too long")
	}

	cfg := strings.TrimSpace(string(pr.Config))
	if cfg == "" || cfg == "null" {
		cfg = "{}"
	}
	if len(cfg) > maxRuleConfigBytes {
		return nil, "", errors.New("config is too large")
	}
	// The config must be a JSON object — this also rejects arrays, scalars and
	// malformed JSON that could otherwise reach the alert engine.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(cfg), &probe); err != nil {
		return nil, "", errors.New("config must be a JSON object")
	}

	cooldown := pr.CooldownSec
	if cooldown < 0 {
		cooldown = 0
	}
	if cooldown > maxCooldownSec {
		cooldown = maxCooldownSec
	}

	var warning string
	var webhookID *int64
	if wn := strings.TrimSpace(pr.Webhook); wn != "" {
		if id, ok := webhookIDByName[wn]; ok {
			webhookID = &id
		} else {
			warning = fmt.Sprintf("rule %q: webhook %q not found, left without a destination", name, wn)
		}
	}

	return &store.AlertRule{
		Name:        name,
		Enabled:     pr.Enabled,
		Type:        pr.Type,
		Target:      strings.TrimSpace(pr.Target),
		Config:      cfg,
		Severity:    sev,
		WebhookID:   webhookID,
		Email:       pr.Email,
		CooldownSec: cooldown,
	}, warning, nil
}
