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

// DeleteExpiredOAuth purges authorization codes and refresh tokens whose expiry
// has passed (issued-but-never-redeemed codes, lapsed refresh tokens). Run
// periodically so the tables don't grow unbounded.
func (s *Store) DeleteExpiredOAuth(ctx context.Context) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM oauth_codes WHERE expires_at < ?`, now); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM oauth_refresh_tokens WHERE expires_at != '' AND expires_at < ?`, now)
	return err
}

// OAuthRefreshToken is the state bound to a refresh token.
type OAuthRefreshToken struct {
	ClientID  string
	UserID    int64
	Scope     string
	Resource  string
	ExpiresAt time.Time
}

// CreateRefreshToken stores a refresh token (by hash).
func (s *Store) CreateRefreshToken(ctx context.Context, tokenHash string, t *OAuthRefreshToken) error {
	now := time.Now().UTC().Format(time.RFC3339)
	expires := ""
	if !t.ExpiresAt.IsZero() {
		expires = t.ExpiresAt.UTC().Format(time.RFC3339)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO oauth_refresh_tokens (token_hash, client_id, user_id, scope, resource, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		tokenHash, t.ClientID, t.UserID, t.Scope, t.Resource, expires, now)
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
		SELECT client_id, user_id, scope, resource, expires_at
		FROM oauth_refresh_tokens WHERE token_hash = ?`, tokenHash).
		Scan(&t.ClientID, &t.UserID, &t.Scope, &t.Resource, &expires)
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
