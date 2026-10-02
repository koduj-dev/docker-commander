package store

import (
	"context"
	"strings"
	"time"
)

// AuditEntry is a single recorded security-relevant action.
type AuditEntry struct {
	ID       int64  `json:"id"`
	UserID   int64  `json:"userId"`
	Username string `json:"username"`
	Action   string `json:"action"`
	Target   string `json:"target"`
	Detail   string `json:"detail"`
	IP       string `json:"ip"`
	// HostID is the Docker host the action targeted, 0 for the local daemon or
	// for actions with no host dimension. Recorded because a scoped action is only
	// meaningful with the "where" alongside the "what".
	HostID    int64     `json:"hostId"`
	CreatedAt time.Time `json:"createdAt"`
}

// Audit appends an entry to the audit log. Failures are returned but callers
// generally log-and-continue: an audit write must never block a user action.
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_log (user_id, username, action, target, detail, ip, host_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.UserID, e.Username, e.Action, e.Target, e.Detail, e.IP, e.HostID,
		time.Now().UTC().Format(time.RFC3339))
	return err
}

// AuditHostSeveral is the host of an audit entry about something that spans
// more than one host, or every host (a maintenance window, for instance). One
// host id can't say "these hosts", so such an entry is shown only to readers
// who may see every host: the safe side, since showing it to anyone narrower
// could disclose a host they are kept away from.
const AuditHostSeveral int64 = -1

// AuditHostOf is the audit host for something scoped to hostIDs: that host when
// there is exactly one (the local daemon by its own id, never 0), otherwise
// AuditHostSeveral. An empty list means every host.
func (s *Store) AuditHostOf(ctx context.Context, hostIDs []int64) int64 {
	if len(hostIDs) != 1 {
		return AuditHostSeveral
	}
	if s.NormalizeHostID(ctx, hostIDs[0]) != 0 {
		return hostIDs[0]
	}
	if local, err := s.LocalHostID(ctx); err == nil {
		return local
	}
	return AuditHostSeveral
}

// RecentAudit returns the most recent audit entries, newest first. When before
// is > 0, only entries older than that id are returned (cursor pagination).
//
// Unless allHosts, only entries whose host is in hostIDs are returned. The scope
// is applied in the query, before the limit: filtering a fetched page instead
// let a busy host the reader can't see push every entry they may see out of it.
func (s *Store) RecentAudit(ctx context.Context, limit int, before int64, hostIDs []int64, allHosts bool) ([]AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	query := `SELECT id, user_id, username, action, target, detail, ip, host_id, created_at FROM audit_log WHERE 1 = 1`
	args := []any{}
	if before > 0 {
		query += ` AND id < ?`
		args = append(args, before)
	}
	if !allHosts {
		if len(hostIDs) == 0 {
			return []AuditEntry{}, nil
		}
		query += ` AND host_id IN (?` + strings.Repeat(`, ?`, len(hostIDs)-1) + `)`
		for _, id := range hostIDs {
			args = append(args, id)
		}
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var created string
		if err := rows.Scan(&e.ID, &e.UserID, &e.Username, &e.Action, &e.Target, &e.Detail, &e.IP, &e.HostID, &created); err != nil {
			return nil, err
		}
		e.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, e)
	}
	return out, rows.Err()
}
