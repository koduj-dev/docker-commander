package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// User is an application account. PasswordHash is an Argon2id encoded hash.
// TOTPSecret is the base32 shared secret; it is only meaningful once
// TOTPEnabled is true (i.e. the user confirmed enrollment with a valid code).
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string
	TOTPSecret   string
	TOTPEnabled  bool
	CreatedAt    time.Time
	LastLoginAt  time.Time
}

// CountUsers returns the number of accounts; used to detect first-run setup.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser inserts a new account and returns its assigned ID.
func (s *Store) CreateUser(ctx context.Context, u *User) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO users (username, password_hash, role, totp_secret, totp_enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		u.Username, u.PasswordHash, orDefault(u.Role, "admin"), u.TOTPSecret, boolToInt(u.TOTPEnabled), now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UserByUsername looks up a user by their unique username.
func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, role, totp_secret, totp_enabled, created_at, last_login_at
		FROM users WHERE username = ?`, username))
}

// UserByID looks up a user by primary key.
func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, role, totp_secret, totp_enabled, created_at, last_login_at
		FROM users WHERE id = ?`, id))
}

// SetTOTP stores the secret and enabled flag for a user (enrollment / disable).
func (s *Store) SetTOTP(ctx context.Context, userID int64, secret string, enabled bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_secret = ?, totp_enabled = ? WHERE id = ?`,
		secret, boolToInt(enabled), userID)
	return err
}

// TouchLogin records the timestamp of a successful login.
func (s *Store) TouchLogin(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET last_login_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), userID)
	return err
}

// UpdatePassword replaces the stored Argon2id hash for a user.
func (s *Store) UpdatePassword(ctx context.Context, userID int64, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, userID)
	return err
}

func (s *Store) scanUser(row *sql.Row) (*User, error) {
	var u User
	var enabled int
	var createdAt, lastLogin string
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.TOTPSecret, &enabled, &createdAt, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.TOTPEnabled = enabled != 0
	u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	u.LastLoginAt, _ = time.Parse(time.RFC3339, lastLogin)
	return &u, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
