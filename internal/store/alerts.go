package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Webhook is a generic HTTP destination an alert rule can fire to. body_template
// is a Go text/template rendered against the alert event.
type Webhook struct {
	ID           int64             `json:"id"`
	Name         string            `json:"name"`
	URL          string            `json:"url"`
	Method       string            `json:"method"`
	Headers      map[string]string `json:"headers"`
	BodyTemplate string            `json:"bodyTemplate"`
	CreatedAt    time.Time         `json:"createdAt"`
}

// AlertRule defines when an alert fires and where it goes.
type AlertRule struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Type      string `json:"type"`     // state | resource | log | restart
	Target    string `json:"target"`   // container name substring; '' or '*' = all
	Config    string `json:"config"`   // raw JSON, interpreted by the engine
	Severity  string `json:"severity"` // info | warning | critical
	WebhookID *int64 `json:"webhookId"`
	Email     bool   `json:"email"` // also send this rule by e-mail
	// Emails are this rule's own recipients. Empty falls back to the instance-wide
	// SMTP "To" (and the per-host override), so rules written before per-rule
	// recipients existed keep delivering exactly as they did.
	Emails      []string  `json:"emails"`
	CooldownSec int       `json:"cooldownSec"`
	CreatedAt   time.Time `json:"createdAt"`
}

// AlertEvent is a fired alert recorded for the in-app feed.
type AlertEvent struct {
	ID            int64     `json:"id"`
	RuleID        int64     `json:"ruleId"`
	RuleName      string    `json:"ruleName"`
	Type          string    `json:"type"`
	Severity      string    `json:"severity"`
	HostID        int64     `json:"hostId"`
	HostName      string    `json:"hostName"`
	ContainerID   string    `json:"containerId"`
	ContainerName string    `json:"containerName"`
	Message       string    `json:"message"`
	Value         *float64  `json:"value"`
	Acknowledged  bool      `json:"acknowledged"`
	CreatedAt     time.Time `json:"createdAt"`
	// Kind is the point in a condition's life this event marks:
	//
	//	firing    the condition started
	//	escalated it is still on, at a higher severity than before
	//	eased     it is still on, at a lower severity
	//	repeat    it is still on and the re-notify interval elapsed
	//	resolved  it stopped; DurationSec says how long it lasted
	//
	// Edge-triggered rules (state, log, restart) only ever emit "firing" —
	// a container that died or a log line that matched has no later moment at
	// which it stops being true.
	Kind string `json:"kind"`
	// DurationSec is how long the condition held, set on resolved events.
	DurationSec int `json:"durationSec"`
}

// AlertKind values. Anything level-triggered moves between these; anything
// edge-triggered stays at KindFiring.
const (
	KindFiring    = "firing"
	KindEscalated = "escalated"
	KindEased     = "eased"
	KindRepeat    = "repeat"
	KindResolved  = "resolved"
)

// AlertState is a condition currently held to be true — one row per
// (host, container, metric), regardless of how many rules noticed it.
type AlertState struct {
	HostID        int64
	HostName      string
	ContainerID   string
	ContainerName string
	Metric        string
	RuleID        int64
	RuleName      string
	Severity      string
	LastValue     *float64
	StartedAt     time.Time
	NotifiedAt    time.Time
}

// ---- Webhooks ---------------------------------------------------------------

// ListWebhooks returns all configured webhooks.
func (s *Store) ListWebhooks(ctx context.Context) ([]Webhook, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, url, method, headers, body_template, created_at FROM webhooks ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// WebhookByID returns one webhook by ID (ErrNotFound if missing).
func (s *Store) WebhookByID(ctx context.Context, id int64) (*Webhook, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, url, method, headers, body_template, created_at FROM webhooks WHERE id = ?`, id)
	return scanWebhook(row)
}

// CreateWebhook inserts a webhook and returns its ID.
func (s *Store) CreateWebhook(ctx context.Context, w *Webhook) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO webhooks (name, url, method, headers, body_template, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		w.Name, w.URL, orDefault(w.Method, "POST"), encodeJSON(w.Headers), w.BodyTemplate,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteWebhook removes a webhook by ID.
func (s *Store) DeleteWebhook(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM webhooks WHERE id = ?`, id)
	return err
}

// ---- Alert rules ------------------------------------------------------------

// ListAlertRules returns all alert rules.
func (s *Store) ListAlertRules(ctx context.Context) ([]AlertRule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, enabled, type, target, config, severity, webhook_id, cooldown_sec, email, emails, created_at
		FROM alert_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertRule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// CreateAlertRule inserts an alert rule and returns its ID.
func (s *Store) CreateAlertRule(ctx context.Context, r *AlertRule) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO alert_rules (name, enabled, type, target, config, severity, webhook_id, cooldown_sec, email, emails, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Name, boolToInt(r.Enabled), r.Type, r.Target, orDefault(r.Config, "{}"),
		orDefault(r.Severity, "warning"), r.WebhookID, defaultInt(r.CooldownSec, 60), boolToInt(r.Email),
		marshalEmails(r.Emails), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetAlertRuleEnabled toggles an alert rule on or off.
func (s *Store) SetAlertRuleEnabled(ctx context.Context, id int64, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alert_rules SET enabled = ? WHERE id = ?`, boolToInt(enabled), id)
	return err
}

// UpdateAlertRule replaces a rule's mutable fields (enabled is managed
// separately via SetAlertRuleEnabled).
func (s *Store) UpdateAlertRule(ctx context.Context, id int64, r *AlertRule) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE alert_rules
		SET name = ?, type = ?, target = ?, config = ?, severity = ?, webhook_id = ?, cooldown_sec = ?, email = ?, emails = ?
		WHERE id = ?`,
		r.Name, r.Type, r.Target, orDefault(r.Config, "{}"), orDefault(r.Severity, "warning"),
		r.WebhookID, defaultInt(r.CooldownSec, 60), boolToInt(r.Email), marshalEmails(r.Emails), id)
	return err
}

// DeleteAlertRule removes an alert rule by ID.
func (s *Store) DeleteAlertRule(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM alert_rules WHERE id = ?`, id)
	return err
}

// ---- Alert events -----------------------------------------------------------

// InsertAlertEvent records a fired alert event and returns its ID.
func (s *Store) InsertAlertEvent(ctx context.Context, e *AlertEvent) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO alert_events (rule_id, rule_name, type, severity, host_id, host_name, container_id, container_name, message, value, kind, duration_sec, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.RuleID, e.RuleName, e.Type, e.Severity, e.HostID, e.HostName, e.ContainerID, e.ContainerName, e.Message, e.Value,
		orDefault(e.Kind, KindFiring), e.DurationSec,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListAlertEvents returns recent alert events (newest first), up to limit.
func (s *Store) ListAlertEvents(ctx context.Context, limit int) ([]AlertEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, rule_id, rule_name, type, severity, host_id, host_name, container_id, container_name, message, value, acknowledged, kind, duration_sec, created_at
		FROM alert_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertEvent
	for rows.Next() {
		var e AlertEvent
		var created string
		var value sql.NullFloat64
		var ack int
		if err := rows.Scan(&e.ID, &e.RuleID, &e.RuleName, &e.Type, &e.Severity, &e.HostID, &e.HostName, &e.ContainerID,
			&e.ContainerName, &e.Message, &value, &ack, &e.Kind, &e.DurationSec, &created); err != nil {
			return nil, err
		}
		if value.Valid {
			e.Value = &value.Float64
		}
		e.Acknowledged = ack != 0
		e.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, e)
	}
	return out, rows.Err()
}

// AckAlertEvent marks an alert event acknowledged.
func (s *Store) AckAlertEvent(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alert_events SET acknowledged = 1 WHERE id = ?`, id)
	return err
}

// CountUnacknowledged returns the number of unacknowledged alert events.
func (s *Store) CountUnacknowledged(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM alert_events WHERE acknowledged = 0`).Scan(&n)
	return n, err
}

// ---- scanning helpers -------------------------------------------------------

type scanner interface {
	Scan(dest ...any) error
}

func scanWebhook(r scanner) (*Webhook, error) {
	var w Webhook
	var headers, created string
	err := r.Scan(&w.ID, &w.Name, &w.URL, &w.Method, &headers, &w.BodyTemplate, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	w.Headers = decodeJSON(headers)
	w.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return &w, nil
}

func scanRule(r scanner) (*AlertRule, error) {
	var rule AlertRule
	var enabled, email int
	var emails string
	var created string
	var webhookID sql.NullInt64
	err := r.Scan(&rule.ID, &rule.Name, &enabled, &rule.Type, &rule.Target, &rule.Config,
		&rule.Severity, &webhookID, &rule.CooldownSec, &email, &emails, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rule.Enabled = enabled != 0
	rule.Email = email != 0
	rule.Emails = unmarshalEmails(emails)
	if webhookID.Valid {
		rule.WebhookID = &webhookID.Int64
	}
	rule.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return &rule, nil
}

func defaultInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// marshalEmails stores a rule's recipients, dropping blanks and duplicates so a
// sloppy paste can't produce empty or repeated addresses.
func marshalEmails(in []string) string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, e := range in {
		e = strings.TrimSpace(e)
		if e == "" || seen[strings.ToLower(e)] {
			continue
		}
		seen[strings.ToLower(e)] = true
		out = append(out, e)
	}
	if len(out) == 0 {
		return ""
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func unmarshalEmails(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// ---- alert states (level-triggered conditions) --------------------------------

// ListAlertStates returns every condition currently held to be firing.
func (s *Store) ListAlertStates(ctx context.Context) ([]AlertState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT host_id, host_name, container_id, container_name, metric, rule_id, rule_name, severity,
		       last_value, started_at, notified_at
		FROM alert_states`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertState
	for rows.Next() {
		var a AlertState
		var value sql.NullFloat64
		var started, notified string
		if err := rows.Scan(&a.HostID, &a.HostName, &a.ContainerID, &a.ContainerName, &a.Metric,
			&a.RuleID, &a.RuleName, &a.Severity, &value, &started, &notified); err != nil {
			return nil, err
		}
		if value.Valid {
			a.LastValue = &value.Float64
		}
		a.StartedAt, _ = time.Parse(time.RFC3339, started)
		a.NotifiedAt, _ = time.Parse(time.RFC3339, notified)
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpsertAlertState records or updates a firing condition. StartedAt is written
// only on insert, so the age of an incident survives escalation and re-notify.
func (s *Store) UpsertAlertState(ctx context.Context, a *AlertState) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO alert_states (host_id, host_name, container_id, container_name, metric,
		                          rule_id, rule_name, severity, last_value, started_at, notified_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(host_id, container_id, metric) DO UPDATE SET
			host_name = excluded.host_name,
			container_name = excluded.container_name,
			rule_id = excluded.rule_id,
			rule_name = excluded.rule_name,
			severity = excluded.severity,
			last_value = excluded.last_value,
			notified_at = excluded.notified_at`,
		a.HostID, a.HostName, a.ContainerID, a.ContainerName, a.Metric,
		a.RuleID, a.RuleName, a.Severity, a.LastValue,
		a.StartedAt.UTC().Format(time.RFC3339), a.NotifiedAt.UTC().Format(time.RFC3339))
	return err
}

// DeleteAlertState clears a condition that no longer holds.
func (s *Store) DeleteAlertState(ctx context.Context, hostID int64, containerID, metric string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM alert_states WHERE host_id = ? AND container_id = ? AND metric = ?`,
		hostID, containerID, metric)
	return err
}
