package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Session is one signed-in browser or client.
type Session struct {
	ID         string // the token's jti
	UserID     int64
	IP         string
	UserAgent  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// CreateSession records a new session at login.
func (s *Store) CreateSession(ctx context.Context, sess *Session) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, ip, user_agent, created_at, last_seen_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, sess.IP, truncateUA(sess.UserAgent), now, now,
		sess.ExpiresAt.UTC().Format(time.RFC3339))
	return err
}

// truncateUA bounds what a client can write into the database. A user agent is
// attacker-controlled text that the owner will later read in their own profile;
// there is no reason to store more of it than identifies a browser.
func truncateUA(ua string) string {
	const max = 256
	if len(ua) > max {
		return ua[:max]
	}
	return ua
}

// SessionExists reports whether a session id is still valid for that user.
func (s *Store) SessionExists(ctx context.Context, id string, userID int64) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM sessions WHERE id = ? AND user_id = ?`, id, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// TouchSession records that a session was used, at minute granularity.
//
// Written only when the stored value is already a minute old: this runs on every
// authenticated request, and a write per request would turn a read-mostly
// workload into a write-mostly one against a single-writer database.
func (s *Store) TouchSession(ctx context.Context, id string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET last_seen_at = ?
		WHERE id = ? AND last_seen_at < ?`,
		now.Format(time.RFC3339), id, now.Add(-time.Minute).Format(time.RFC3339))
	return err
}

// ListSessions returns a user's own sessions, newest first, dropping any that
// have expired (a token past its expiry is refused anyway, so listing it would
// only invite people to revoke something already gone).
func (s *Store) ListSessions(ctx context.Context, userID int64) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, ip, user_agent, created_at, last_seen_at, expires_at
		FROM sessions WHERE user_id = ? AND expires_at > ?
		ORDER BY last_seen_at DESC`, userID, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Session{}
	for rows.Next() {
		var sess Session
		var created, seen, exp string
		if err := rows.Scan(&sess.ID, &sess.UserID, &sess.IP, &sess.UserAgent, &created, &seen, &exp); err != nil {
			return nil, err
		}
		sess.CreatedAt, _ = time.Parse(time.RFC3339, created)
		sess.LastSeenAt, _ = time.Parse(time.RFC3339, seen)
		sess.ExpiresAt, _ = time.Parse(time.RFC3339, exp)
		out = append(out, sess)
	}
	return out, rows.Err()
}

// DeleteSession revokes one session. Scoped by user id as well as session id, so
// knowing (or guessing) another account's session id achieves nothing.
func (s *Store) DeleteSession(ctx context.Context, id string, userID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUserSessions revokes every session for a user — sign out everywhere, and
// what a password change does.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// PurgeExpiredSessions drops rows whose tokens can no longer be presented.
func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at <= ?`, time.Now().UTC().Format(time.RFC3339))
	return err
}
