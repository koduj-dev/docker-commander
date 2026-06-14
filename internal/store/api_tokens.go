package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// APIToken is a long-lived bearer credential for programmatic (MCP) access. The
// plaintext secret is never stored — only TokenHash (a SHA-256 hex digest). A
// token can only narrow its owner's rights, never widen them:
//
//   - Sections, when non-empty, restricts the token to a subset of the user's
//     granted sections (the dispatcher still intersects with the live user
//     grants, so revoking a section in the admin UI also shrinks the token).
//   - ReadOnly, when true, forces read-only even if the user is read-write.
type APIToken struct {
	ID         int64
	UserID     int64
	TokenHash  string
	Name       string
	Sections   []string // empty = inherit all of the user's sections
	ReadOnly   bool
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time // zero = never expires
	Revoked    bool
}

// Expired reports whether the token has a set expiry that is in the past.
func (t *APIToken) Expired() bool {
	return !t.ExpiresAt.IsZero() && time.Now().After(t.ExpiresAt)
}

// CreateAPIToken inserts a new token row and returns its assigned ID. The caller
// is responsible for generating the secret and passing its SHA-256 hash.
func (s *Store) CreateAPIToken(ctx context.Context, t *APIToken) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	expires := ""
	if !t.ExpiresAt.IsZero() {
		expires = t.ExpiresAt.UTC().Format(time.RFC3339)
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO api_tokens (user_id, token_hash, name, sections, read_only, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.UserID, t.TokenHash, t.Name, marshalSections(t.Sections), boolToInt(t.ReadOnly), now, expires)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// APITokenByHash looks up an active (non-revoked) token by its SHA-256 hash.
// Expiry is NOT enforced here — callers check Expired() so they can treat an
// expired token identically to a missing one. Returns ErrNotFound if absent or
// revoked.
func (s *Store) APITokenByHash(ctx context.Context, hash string) (*APIToken, error) {
	return scanAPITokenRow(s.db.QueryRowContext(ctx, `
		SELECT id, user_id, token_hash, name, sections, read_only, created_at, last_used_at, expires_at, revoked
		FROM api_tokens WHERE token_hash = ? AND revoked = 0`, hash))
}

// ListAPITokens returns a user's tokens (newest first) for the management UI.
// The hash is included but is not the secret — the secret is unrecoverable.
func (s *Store) ListAPITokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, token_hash, name, sections, read_only, created_at, last_used_at, expires_at, revoked
		FROM api_tokens WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		t, err := scanAPITokenRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// APITokenWithUser is an APIToken plus its owner's username, for the admin
// overview where tokens from every account are listed together.
type APITokenWithUser struct {
	APIToken
	Username string
}

// ListAllAPITokens returns every user's tokens (newest first), each annotated
// with the owner's username, for the admin overview. Revoked tokens are
// included so an admin can see recently-revoked credentials; the handler/UI
// distinguishes them via the Revoked flag. The token hash is deliberately NOT
// selected — the overview is metadata-only, so the digest never even reaches
// process memory here (no chance of leaking via a log line or panic).
func (s *Store) ListAllAPITokens(ctx context.Context) ([]APITokenWithUser, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.user_id, t.name, t.sections, t.read_only,
		       t.created_at, t.last_used_at, t.expires_at, t.revoked, u.username
		FROM api_tokens t JOIN users u ON u.id = t.user_id
		ORDER BY t.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APITokenWithUser
	for rows.Next() {
		var t APIToken
		var readOnly, revoked int
		var sections, createdAt, lastUsed, expiresAt, username string
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &sections, &readOnly,
			&createdAt, &lastUsed, &expiresAt, &revoked, &username); err != nil {
			return nil, err
		}
		t.ReadOnly = readOnly != 0
		t.Revoked = revoked != 0
		t.Sections = unmarshalSections(sections)
		t.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		t.LastUsedAt, _ = time.Parse(time.RFC3339, lastUsed)
		if expiresAt != "" {
			t.ExpiresAt, _ = time.Parse(time.RFC3339, expiresAt)
		}
		out = append(out, APITokenWithUser{APIToken: t, Username: username})
	}
	return out, rows.Err()
}

// AdminRevokeAPIToken marks any token revoked regardless of owner — for admins
// managing the fleet. Unlike RevokeAPIToken it is not scoped to a user. The bool
// reports whether a matching, still-active token was revoked (false → unknown id
// or already revoked), so the handler can return 404 instead of a false success.
func (s *Store) AdminRevokeAPIToken(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_tokens SET revoked = 1 WHERE id = ? AND revoked = 0`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// RevokeAPIToken marks a token revoked. It is scoped to userID so a caller can
// only revoke their own tokens. The bool reports whether a matching, owned token
// was actually revoked (false → unknown id or not the caller's), so the handler
// can return 404 instead of a misleading success.
func (s *Store) RevokeAPIToken(ctx context.Context, id, userID int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_tokens SET revoked = 1 WHERE id = ? AND user_id = ? AND revoked = 0`, id, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// TouchAPIToken records the last time a token was used. Best-effort: callers
// ignore the error so a logging write never blocks an authenticated request.
func (s *Store) TouchAPIToken(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE api_tokens SET last_used_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id)
	return err
}

func scanAPITokenRow(row scanner) (*APIToken, error) {
	var t APIToken
	var readOnly, revoked int
	var sections, createdAt, lastUsed, expiresAt string
	err := row.Scan(&t.ID, &t.UserID, &t.TokenHash, &t.Name, &sections, &readOnly,
		&createdAt, &lastUsed, &expiresAt, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.ReadOnly = readOnly != 0
	t.Revoked = revoked != 0
	t.Sections = unmarshalSections(sections)
	t.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	t.LastUsedAt, _ = time.Parse(time.RFC3339, lastUsed)
	if expiresAt != "" {
		t.ExpiresAt, _ = time.Parse(time.RFC3339, expiresAt)
	}
	return &t, nil
}
