package store

import (
	"context"
	"time"
)

// AuditEntry is a single recorded security-relevant action.
type AuditEntry struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"userId"`
	Username  string    `json:"username"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Detail    string    `json:"detail"`
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"createdAt"`
}

// Audit appends an entry to the audit log. Failures are returned but callers
// generally log-and-continue: an audit write must never block a user action.
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_log (user_id, username, action, target, detail, ip, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.UserID, e.Username, e.Action, e.Target, e.Detail, e.IP,
		time.Now().UTC().Format(time.RFC3339))
	return err
}

// RecentAudit returns the most recent audit entries, newest first.
func (s *Store) RecentAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, username, action, target, detail, ip, created_at
		FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var created string
		if err := rows.Scan(&e.ID, &e.UserID, &e.Username, &e.Action, &e.Target, &e.Detail, &e.IP, &created); err != nil {
			return nil, err
		}
		e.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, e)
	}
	return out, rows.Err()
}
