package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// MCPOAuthSession is one authorized MCP connector pairing: an OAuth
// authorization grant plus the refresh-token chain it started. ID is a stable
// identifier minted once at the initial authorization-code exchange and
// carried forward, unchanged, across every refresh — unlike the short-lived
// access token's own jti, it does not rotate, which is what lets an admin or
// the owning user kill one specific connector session without waiting for
// token expiry or removing the whole OAuth client.
type MCPOAuthSession struct {
	ID         string
	ClientID   string
	UserID     int64
	IP         string
	UserAgent  string
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time
}

// CreateMCPOAuthSession records a new session at the authorization-code grant.
func (s *Store) CreateMCPOAuthSession(ctx context.Context, sess *MCPOAuthSession) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO mcp_oauth_sessions (id, client_id, user_id, ip, user_agent, created_at, last_used_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.ClientID, sess.UserID, sess.IP, truncateUA(sess.UserAgent), now, now,
		sess.ExpiresAt.UTC().Format(time.RFC3339))
	return err
}

// MCPOAuthSessionExists reports whether a session id is still valid for that
// user. Called on every MCP request bearing an OAuth access token, so a
// revoked session stops working on the very next request rather than waiting
// out the token's own TTL.
func (s *Store) MCPOAuthSessionExists(ctx context.Context, id string, userID int64) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM mcp_oauth_sessions WHERE id = ? AND user_id = ?`, id, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// TouchMCPOAuthSession records that a session's refresh token was used, and
// rolls its expiry forward by the same amount as the freshly-rotated refresh
// token it now travels with — otherwise an actively-used session would
// eventually expire out from under a refresh chain that is still alive.
//
// Called only from the refresh grant (at most every AccessTokenTTL per active
// connector), not from the per-request verification path — unlike sessions.go's
// TouchSession this needs no extra throttling of its own.
func (s *Store) TouchMCPOAuthSession(ctx context.Context, id string, expiresAt time.Time) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`UPDATE mcp_oauth_sessions SET last_used_at = ?, expires_at = ? WHERE id = ?`,
		now, expiresAt.UTC().Format(time.RFC3339), id)
	return err
}

// ListMCPOAuthSessions returns a user's own sessions, most recently used
// first, dropping any that have expired.
func (s *Store) ListMCPOAuthSessions(ctx context.Context, userID int64) ([]MCPOAuthSession, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, client_id, user_id, ip, user_agent, created_at, last_used_at, expires_at
		FROM mcp_oauth_sessions WHERE user_id = ? AND expires_at > ?
		ORDER BY last_used_at DESC`, userID, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMCPOAuthSessions(rows)
}

// ListAllMCPOAuthSessions returns every user's active sessions, for the admin
// fleet-wide overview.
func (s *Store) ListAllMCPOAuthSessions(ctx context.Context) ([]MCPOAuthSession, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, client_id, user_id, ip, user_agent, created_at, last_used_at, expires_at
		FROM mcp_oauth_sessions WHERE expires_at > ?
		ORDER BY last_used_at DESC`, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMCPOAuthSessions(rows)
}

func scanMCPOAuthSessions(rows *sql.Rows) ([]MCPOAuthSession, error) {
	out := []MCPOAuthSession{}
	for rows.Next() {
		var sess MCPOAuthSession
		var created, used, exp string
		if err := rows.Scan(&sess.ID, &sess.ClientID, &sess.UserID, &sess.IP, &sess.UserAgent, &created, &used, &exp); err != nil {
			return nil, err
		}
		sess.CreatedAt, _ = time.Parse(time.RFC3339, created)
		sess.LastUsedAt, _ = time.Parse(time.RFC3339, used)
		sess.ExpiresAt, _ = time.Parse(time.RFC3339, exp)
		out = append(out, sess)
	}
	return out, rows.Err()
}

// RevokeMCPOAuthSession revokes one of the caller's own sessions: the session
// row AND its current refresh token are deleted in the same transaction. This
// is not optional — deleting only the session row would leave the client's
// still-valid refresh token able to mint a fresh access token on its next use,
// silently resurrecting the "revoked" session. Scoped by user id, so knowing
// (or guessing) another account's session id achieves nothing. Returns
// ErrNotFound if the session doesn't exist or isn't the caller's.
func (s *Store) RevokeMCPOAuthSession(ctx context.Context, id string, userID int64) error {
	return deleteMCPOAuthSessionTx(ctx, s.db, `DELETE FROM mcp_oauth_sessions WHERE id = ? AND user_id = ?`, id, userID)
}

// AdminRevokeMCPOAuthSession revokes any user's session by id, for fleet-wide
// administration. Same atomicity guarantee as RevokeMCPOAuthSession.
func (s *Store) AdminRevokeMCPOAuthSession(ctx context.Context, id string) error {
	return deleteMCPOAuthSessionTx(ctx, s.db, `DELETE FROM mcp_oauth_sessions WHERE id = ?`, id)
}

func deleteMCPOAuthSessionTx(ctx context.Context, db *sql.DB, deleteSessionQuery string, args ...any) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rolled back unless committed

	res, err := tx.ExecContext(ctx, deleteSessionQuery, args...)
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
	// The session id is what ties this refresh token to the session — the
	// token's own hash is single-use/opaque and unknown here, so this is the
	// only way to find it.
	sessionID, _ := args[0].(string)
	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_refresh_tokens WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}
