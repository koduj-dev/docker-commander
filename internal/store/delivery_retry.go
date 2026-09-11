package store

import (
	"context"
	"database/sql"
	"time"
)

// AlertDeliveryRetry is one queued retry of a failed delivery. Only
// transient failures are queued here — see the table's own doc comment in
// store.go's schema for exactly which ones and why.
type AlertDeliveryRetry struct {
	ID      int64
	EventID int64
	Channel string // "webhook" | "email"
	// WebhookID is nil for an email retry.
	WebhookID *int64
	// RuleEmails duplicates the rule's own recipients as they were at the
	// time of the original (failed) attempt — a retry re-resolves the SAME
	// recipients that attempt used, not whatever the rule says now.
	RuleEmails    []string
	Attempt       int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
}

// EnqueueAlertDeliveryRetry queues a first retry attempt.
func (s *Store) EnqueueAlertDeliveryRetry(ctx context.Context, r *AlertDeliveryRetry) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO alert_delivery_retries (event_id, channel, webhook_id, rule_emails, attempt, next_attempt_at, last_error, created_at)
		VALUES (?, ?, ?, ?, 0, ?, ?, ?)`,
		r.EventID, r.Channel, r.WebhookID, marshalEmails(r.RuleEmails),
		r.NextAttemptAt.UTC().Format(time.RFC3339), r.LastError, now)
	return err
}

// DueAlertDeliveryRetries returns queued retries whose next attempt is due,
// oldest-due first, capped at limit so one sweep can't run unbounded.
func (s *Store) DueAlertDeliveryRetries(ctx context.Context, now time.Time, limit int) ([]AlertDeliveryRetry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, event_id, channel, webhook_id, rule_emails, attempt, next_attempt_at, last_error, created_at
		FROM alert_delivery_retries WHERE next_attempt_at <= ?
		ORDER BY next_attempt_at ASC LIMIT ?`, now.UTC().Format(time.RFC3339), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlertDeliveryRetry{}
	for rows.Next() {
		var r AlertDeliveryRetry
		var webhookID sql.NullInt64
		var ruleEmails, nextAt, created string
		if err := rows.Scan(&r.ID, &r.EventID, &r.Channel, &webhookID, &ruleEmails, &r.Attempt, &nextAt, &r.LastError, &created); err != nil {
			return nil, err
		}
		if webhookID.Valid {
			id := webhookID.Int64
			r.WebhookID = &id
		}
		r.RuleEmails = unmarshalEmails(ruleEmails)
		r.NextAttemptAt, _ = time.Parse(time.RFC3339, nextAt)
		r.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, r)
	}
	return out, rows.Err()
}

// RescheduleAlertDeliveryRetry records another failed attempt and pushes the
// next one out. incrementAttempt is false when this "attempt" was actually
// skipped because an active maintenance window now covers the event — that
// doesn't count against the retry budget, it just waits the window out.
func (s *Store) RescheduleAlertDeliveryRetry(ctx context.Context, id int64, incrementAttempt bool, nextAttemptAt time.Time, lastError string) error {
	q := `UPDATE alert_delivery_retries SET next_attempt_at = ?, last_error = ?`
	args := []any{nextAttemptAt.UTC().Format(time.RFC3339), lastError}
	if incrementAttempt {
		q += `, attempt = attempt + 1`
	}
	q += ` WHERE id = ?`
	args = append(args, id)
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

// DeleteAlertDeliveryRetry removes a queued retry — on success, or once it
// has given up (not retriable, or attempts exhausted).
func (s *Store) DeleteAlertDeliveryRetry(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM alert_delivery_retries WHERE id = ?`, id)
	return err
}
