package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// OAuthClient is a dynamically-registered (RFC 7591) MCP OAuth client. Clients
// are public (no secret); security rests on PKCE + exact redirect-URI matching.
type OAuthClient struct {
	ID           string // client_id
	Name         string
	RedirectURIs []string
	CreatedAt    time.Time
}

// CreateOAuthClient stores a newly registered client.
func (s *Store) CreateOAuthClient(ctx context.Context, c *OAuthClient) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO oauth_clients (client_id, client_name, redirect_uris, created_at)
		VALUES (?, ?, ?, ?)`,
		c.ID, c.Name, marshalSections(c.RedirectURIs), time.Now().UTC().Format(time.RFC3339))
	return err
}

// OAuthClientByID looks up a registered client.
func (s *Store) OAuthClientByID(ctx context.Context, id string) (*OAuthClient, error) {
	var c OAuthClient
	var uris, created string
	err := s.db.QueryRowContext(ctx,
		`SELECT client_id, client_name, redirect_uris, created_at FROM oauth_clients WHERE client_id = ?`, id).
		Scan(&c.ID, &c.Name, &uris, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.RedirectURIs = unmarshalSections(uris)
	c.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return &c, nil
}

// ListOAuthClients returns every registered MCP OAuth client (newest first) for
// the admin overview. Clients are public (no secret stored), so the full row is
// safe to surface.
func (s *Store) ListOAuthClients(ctx context.Context) ([]OAuthClient, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT client_id, client_name, redirect_uris, created_at
		FROM oauth_clients ORDER BY created_at DESC, client_id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OAuthClient
	for rows.Next() {
		var c OAuthClient
		var uris, created string
		if err := rows.Scan(&c.ID, &c.Name, &uris, &created); err != nil {
			return nil, err
		}
		c.RedirectURIs = unmarshalSections(uris)
		c.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteOAuthClient removes a registered client and, in the same transaction,
// any authorization codes and refresh tokens issued to it — so de-registering a
// client immediately severs every credential derived from it. The bool reports
// whether a client row actually existed (false → unknown id → 404).
func (s *Store) DeleteOAuthClient(ctx context.Context, id string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // rolled back unless committed

	res, err := tx.ExecContext(ctx, `DELETE FROM oauth_clients WHERE client_id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil // unknown client; nothing else to purge
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_codes WHERE client_id = ?`, id); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_refresh_tokens WHERE client_id = ?`, id); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mcp_oauth_sessions WHERE client_id = ?`, id); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// OAuthCode is the state bound to a single-use authorization code.
type OAuthCode struct {
	ClientID      string
	UserID        int64
	RedirectURI   string
	CodeChallenge string
	Resource      string
	Scope         string
	ExpiresAt     time.Time
}

// CreateOAuthCode stores an authorization code (by hash).
func (s *Store) CreateOAuthCode(ctx context.Context, codeHash string, c *OAuthCode) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO oauth_codes (code_hash, client_id, user_id, redirect_uri, code_challenge, resource, scope, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		codeHash, c.ClientID, c.UserID, c.RedirectURI, c.CodeChallenge, c.Resource, c.Scope,
		c.ExpiresAt.UTC().Format(time.RFC3339), now)
	return err
}

// ConsumeOAuthCode atomically fetches and deletes an authorization code, so a
// code can never be redeemed twice. Returns ErrNotFound if absent. Callers must
// still check ExpiresAt.
func (s *Store) ConsumeOAuthCode(ctx context.Context, codeHash string) (*OAuthCode, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // rolled back unless committed

	var c OAuthCode
	var expires string
	err = tx.QueryRowContext(ctx, `
		SELECT client_id, user_id, redirect_uri, code_challenge, resource, scope, expires_at
		FROM oauth_codes WHERE code_hash = ?`, codeHash).
		Scan(&c.ClientID, &c.UserID, &c.RedirectURI, &c.CodeChallenge, &c.Resource, &c.Scope, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_codes WHERE code_hash = ?`, codeHash); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	c.ExpiresAt, _ = time.Parse(time.RFC3339, expires)
	return &c, nil
}

// DeleteExpiredOAuth purges authorization codes, refresh tokens and MCP
// sessions whose expiry has passed (issued-but-never-redeemed codes, lapsed
// refresh tokens, abandoned connector pairings). Run periodically so the
// tables don't grow unbounded.
func (s *Store) DeleteExpiredOAuth(ctx context.Context) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM oauth_codes WHERE expires_at < ?`, now); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM oauth_refresh_tokens WHERE expires_at != '' AND expires_at < ?`, now); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM mcp_oauth_sessions WHERE expires_at < ?`, now)
	return err
}

// OAuthRefreshToken is the state bound to a refresh token.
type OAuthRefreshToken struct {
	ClientID  string
	UserID    int64
	Scope     string
	Resource  string
	ExpiresAt time.Time
	// SessionID names the mcp_oauth_sessions row this refresh token belongs
	// to. It is minted once (at the authorization-code grant) and carried
	// forward unchanged across every rotation, unlike TokenHash — it is what
	// lets RevokeMCPOAuthSession find and delete the CURRENT refresh token
	// for a session without ever seeing its hash. Empty for a refresh token
	// issued before per-session revocation existed.
	SessionID string
}

// CreateRefreshToken stores a refresh token (by hash).
func (s *Store) CreateRefreshToken(ctx context.Context, tokenHash string, t *OAuthRefreshToken) error {
	now := time.Now().UTC().Format(time.RFC3339)
	expires := ""
	if !t.ExpiresAt.IsZero() {
		expires = t.ExpiresAt.UTC().Format(time.RFC3339)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO oauth_refresh_tokens (token_hash, client_id, user_id, scope, resource, expires_at, created_at, session_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		tokenHash, t.ClientID, t.UserID, t.Scope, t.Resource, expires, now, t.SessionID)
	return err
}

// ConsumeRefreshToken atomically fetches and deletes a refresh token (rotation:
// every use invalidates the old token and a fresh one is issued). Returns
// ErrNotFound if absent. Callers must still check ExpiresAt.
func (s *Store) ConsumeRefreshToken(ctx context.Context, tokenHash string) (*OAuthRefreshToken, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // rolled back unless committed

	var t OAuthRefreshToken
	var expires string
	err = tx.QueryRowContext(ctx, `
		SELECT client_id, user_id, scope, resource, expires_at, session_id
		FROM oauth_refresh_tokens WHERE token_hash = ?`, tokenHash).
		Scan(&t.ClientID, &t.UserID, &t.Scope, &t.Resource, &expires, &t.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_refresh_tokens WHERE token_hash = ?`, tokenHash); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if expires != "" {
		t.ExpiresAt, _ = time.Parse(time.RFC3339, expires)
	}
	return &t, nil
}

// Sentinel errors RotateRefreshToken's validate callback can return to abort
// a rotation with a specific, wire-visible reason (mapped to distinct
// invalid_grant messages by the caller) rather than the generic ErrNotFound.
var (
	ErrRefreshTokenExpired     = errors.New("refresh token expired")
	ErrRefreshClientMismatch   = errors.New("refresh token client mismatch")
	ErrRefreshResourceMismatch = errors.New("refresh token resource mismatch")
)

// RotateRefreshToken is an entire OAuth refresh-grant rotation as ONE
// transaction: consume oldTokenHash, hand the result to validate for the
// expiry/client_id/resource checks a caller needs (pure checks against the
// already-fetched record — no further DB access required), and — only if
// validate accepts it — mint the replacement token bound to the consumed
// token's session (touching it), or a freshly created one identified by
// candidateSessionID if the consumed token predates per-session revocation
// (its SessionID is empty). sessionIP/sessionUserAgent/sessionExpiresAt feed
// that touch-or-create; newTokenHash/newTokenExpiresAt are the replacement
// refresh token's identity. Returns the consumed record and the session id
// the new token now carries.
//
// This is deliberately ONE transaction end to end, not "consume, then
// separately mint" as it used to be: a concurrent RevokeMCPOAuthSession
// deletes a session row and its current refresh token together, so it can
// now only ever land wholly BEFORE this call starts (in which case its
// delete of the presented token is exactly what makes the initial lookup
// below return ErrNotFound) or wholly AFTER this call commits (correctly
// killing the session this call just renewed) — never in the gap between
// "the old token is gone" and "the new one, and the session it belongs to,
// are both live". Before this, that gap was real: a revoke landing inside
// it deleted the session but had nothing left to delete from
// oauth_refresh_tokens (the old token already consumed, the new one not yet
// inserted), and the caller's own self-heal for a legitimately-desynced
// session (see TouchMCPOAuthSession) then silently resurrected it with a
// fresh, fully valid token pair.
//
// If the consumed token already carried a session id and that session row
// doesn't exist, it is recreated (self-healed) rather than refused — by
// construction (see above) that state can now only be a legitimate desync
// (sessions and their refresh tokens are swept independently by their own
// expiry in DeleteExpiredOAuth), never a revoke racing this call, so
// resurrecting it here is safe.
func (s *Store) RotateRefreshToken(ctx context.Context, oldTokenHash string, validate func(*OAuthRefreshToken) error, candidateSessionID, sessionIP, sessionUserAgent string, sessionExpiresAt time.Time, newTokenHash string, newTokenExpiresAt time.Time) (t *OAuthRefreshToken, sessionID string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback() //nolint:errcheck // rolled back unless committed

	var rec OAuthRefreshToken
	var expires string
	err = tx.QueryRowContext(ctx, `
		SELECT client_id, user_id, scope, resource, expires_at, session_id
		FROM oauth_refresh_tokens WHERE token_hash = ?`, oldTokenHash).
		Scan(&rec.ClientID, &rec.UserID, &rec.Scope, &rec.Resource, &expires, &rec.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_refresh_tokens WHERE token_hash = ?`, oldTokenHash); err != nil {
		return nil, "", err
	}
	if expires != "" {
		rec.ExpiresAt, _ = time.Parse(time.RFC3339, expires)
	}

	if validate != nil {
		if verr := validate(&rec); verr != nil {
			// The old token is single-use regardless of what happens next, so
			// this still commits its deletion — matching the pre-existing
			// behaviour where consuming alone already burned it.
			if cerr := tx.Commit(); cerr != nil {
				return nil, "", cerr
			}
			return &rec, "", verr
		}
	}

	sessionID = rec.SessionID
	create := false
	if sessionID == "" {
		// A refresh token issued before per-session revocation existed. Start
		// tracking it as a session from here on rather than leaving it
		// unrevocable forever.
		sessionID = candidateSessionID
		create = true
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if !create {
		res, err := tx.ExecContext(ctx,
			`UPDATE mcp_oauth_sessions SET last_used_at = ?, expires_at = ? WHERE id = ?`,
			now, sessionExpiresAt.UTC().Format(time.RFC3339), sessionID)
		if err != nil {
			return nil, "", err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, "", err
		}
		// The session row is gone (swept independently — see the doc comment
		// above for why this can only be that, never a revoke racing this
		// call) but the refresh chain is still alive: recreate it, same as a
		// fresh grant, so the token this call is about to mint stays
		// verifiable instead of being rejected on its very next use by
		// MCPOAuthSessionExists.
		if n == 0 {
			create = true
		}
	}
	if create {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO mcp_oauth_sessions (id, client_id, user_id, ip, user_agent, created_at, last_used_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			sessionID, rec.ClientID, rec.UserID, sessionIP, truncateUA(sessionUserAgent), now, now,
			sessionExpiresAt.UTC().Format(time.RFC3339)); err != nil {
			return nil, "", err
		}
	}

	newExpires := ""
	if !newTokenExpiresAt.IsZero() {
		newExpires = newTokenExpiresAt.UTC().Format(time.RFC3339)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO oauth_refresh_tokens (token_hash, client_id, user_id, scope, resource, expires_at, created_at, session_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		newTokenHash, rec.ClientID, rec.UserID, rec.Scope, rec.Resource, newExpires, now, sessionID); err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	return &rec, sessionID, nil
}
