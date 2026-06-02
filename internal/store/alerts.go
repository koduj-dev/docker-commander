package store

import (
	"context"
	"database/sql"
	"errors"
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
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Enabled     bool      `json:"enabled"`
	Type        string    `json:"type"`     // state | resource | log | restart
	Target      string    `json:"target"`   // container name substring; '' or '*' = all
	Config      string    `json:"config"`   // raw JSON, interpreted by the engine
	Severity    string    `json:"severity"` // info | warning | critical
	WebhookID   *int64    `json:"webhookId"`
	Email       bool      `json:"email"` // also send to the configured SMTP recipient
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
}

// ---- Webhooks ---------------------------------------------------------------

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

func (s *Store) WebhookByID(ctx context.Context, id int64) (*Webhook, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, url, method, headers, body_template, created_at FROM webhooks WHERE id = ?`, id)
	return scanWebhook(row)
}

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

func (s *Store) DeleteWebhook(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM webhooks WHERE id = ?`, id)
	return err
}

// ---- Alert rules ------------------------------------------------------------

func (s *Store) ListAlertRules(ctx context.Context) ([]AlertRule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, enabled, type, target, config, severity, webhook_id, cooldown_sec, email, created_at
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

func (s *Store) CreateAlertRule(ctx context.Context, r *AlertRule) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO alert_rules (name, enabled, type, target, config, severity, webhook_id, cooldown_sec, email, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Name, boolToInt(r.Enabled), r.Type, r.Target, orDefault(r.Config, "{}"),
		orDefault(r.Severity, "warning"), r.WebhookID, defaultInt(r.CooldownSec, 60), boolToInt(r.Email),
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) SetAlertRuleEnabled(ctx context.Context, id int64, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alert_rules SET enabled = ? WHERE id = ?`, boolToInt(enabled), id)
	return err
}

// UpdateAlertRule replaces a rule's mutable fields (enabled is managed
// separately via SetAlertRuleEnabled).
func (s *Store) UpdateAlertRule(ctx context.Context, id int64, r *AlertRule) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE alert_rules
		SET name = ?, type = ?, target = ?, config = ?, severity = ?, webhook_id = ?, cooldown_sec = ?, email = ?
		WHERE id = ?`,
		r.Name, r.Type, r.Target, orDefault(r.Config, "{}"), orDefault(r.Severity, "warning"),
		r.WebhookID, defaultInt(r.CooldownSec, 60), boolToInt(r.Email), id)
	return err
}

func (s *Store) DeleteAlertRule(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM alert_rules WHERE id = ?`, id)
	return err
}

// ---- Alert events -----------------------------------------------------------

func (s *Store) InsertAlertEvent(ctx context.Context, e *AlertEvent) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO alert_events (rule_id, rule_name, type, severity, host_id, host_name, container_id, container_name, message, value, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.RuleID, e.RuleName, e.Type, e.Severity, e.HostID, e.HostName, e.ContainerID, e.ContainerName, e.Message, e.Value,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListAlertEvents(ctx context.Context, limit int) ([]AlertEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, rule_id, rule_name, type, severity, host_id, host_name, container_id, container_name, message, value, acknowledged, created_at
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
			&e.ContainerName, &e.Message, &value, &ack, &created); err != nil {
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

func (s *Store) AckAlertEvent(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alert_events SET acknowledged = 1 WHERE id = ?`, id)
	return err
}

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
	var created string
	var webhookID sql.NullInt64
	err := r.Scan(&rule.ID, &rule.Name, &enabled, &rule.Type, &rule.Target, &rule.Config,
		&rule.Severity, &webhookID, &rule.CooldownSec, &email, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rule.Enabled = enabled != 0
	rule.Email = email != 0
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
