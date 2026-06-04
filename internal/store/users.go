package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// User is an application account. PasswordHash is an Argon2id encoded hash.
// TOTPSecret is the base32 shared secret; it is only meaningful once
// TOTPEnabled is true (i.e. the user confirmed enrollment with a valid code).
//
// Role is "admin" (full access incl. user/feature management) or "user".
// For "user" accounts, Sections lists the menu sections they may access and
// ReadOnly blocks mutating actions. Admins ignore both.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string
	AuthSource   string // "local" (password stored here) or "ldap" (verified externally)
	ReadOnly     bool
	Sections     []string
	TOTPSecret   string
	TOTPEnabled  bool
	CreatedAt    time.Time
	LastLoginAt  time.Time
}

// IsAdmin reports whether the user has the admin role.
func (u *User) IsAdmin() bool { return u.Role == "admin" }

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
		INSERT INTO users (username, password_hash, role, totp_secret, totp_enabled, read_only, sections, auth_source, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.Username, u.PasswordHash, orDefault(u.Role, "admin"), u.TOTPSecret, boolToInt(u.TOTPEnabled),
		boolToInt(u.ReadOnly), marshalSections(u.Sections), orDefault(u.AuthSource, "local"), now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListUsers returns all accounts (without secrets) for the admin user manager.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, username, password_hash, role, totp_secret, totp_enabled, read_only, sections, auth_source, created_at, last_login_at
		FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// DeleteUser removes an account.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	return err
}

// CountAdmins returns how many admin accounts exist (to guard the last admin).
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin'`).Scan(&n)
	return n, err
}

// UpdateUserAccess changes a user's role, read-only flag and allowed sections.
func (s *Store) UpdateUserAccess(ctx context.Context, id int64, role string, readOnly bool, sections []string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET role = ?, read_only = ?, sections = ? WHERE id = ?`,
		orDefault(role, "user"), boolToInt(readOnly), marshalSections(sections), id)
	return err
}

// UserByUsername looks up a user by their unique username.
func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	return scanUserRow(s.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, role, totp_secret, totp_enabled, read_only, sections, auth_source, created_at, last_login_at
		FROM users WHERE username = ?`, username))
}

// UserByID looks up a user by primary key.
func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUserRow(s.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, role, totp_secret, totp_enabled, read_only, sections, auth_source, created_at, last_login_at
		FROM users WHERE id = ?`, id))
}

// UserPrefs returns a user's UI preferences as a JSON object string ("{}" if
// none). These are opaque to the server — the frontend owns the shape.
func (s *Store) UserPrefs(ctx context.Context, userID int64) (string, error) {
	var prefs string
	err := s.db.QueryRowContext(ctx, `SELECT ui_prefs FROM users WHERE id = ?`, userID).Scan(&prefs)
	if err != nil {
		return "{}", err
	}
	if prefs == "" {
		prefs = "{}"
	}
	return prefs, nil
}

// SetUserPrefs replaces a user's UI preferences JSON blob.
func (s *Store) SetUserPrefs(ctx context.Context, userID int64, prefs string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET ui_prefs = ? WHERE id = ?`, prefs, userID)
	return err
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

func scanUserRow(row scanner) (*User, error) {
	var u User
	var enabled, readOnly int
	var sections, createdAt, lastLogin string
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.TOTPSecret, &enabled,
		&readOnly, &sections, &u.AuthSource, &createdAt, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.TOTPEnabled = enabled != 0
	u.ReadOnly = readOnly != 0
	u.Sections = unmarshalSections(sections)
	u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	u.LastLoginAt, _ = time.Parse(time.RFC3339, lastLogin)
	return &u, nil
}

func marshalSections(s []string) string {
	if len(s) == 0 {
		return ""
	}
	b, _ := json.Marshal(s)
	return string(b)
}

func unmarshalSections(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	_ = json.Unmarshal([]byte(raw), &out)
	return out
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
