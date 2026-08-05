package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
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
	// Email receives alerts from rules this user creates. Optional; when an LDAP
	// directory publishes a mail attribute it is synced here on login.
	Email      string
	AuthSource string // "local" (password stored here) or "ldap" (verified externally)
	ReadOnly   bool
	Sections   []string
	TOTPSecret string
	// TOTPEnabled means "has an authenticator app". MFAEnabled means "has a second
	// factor of any kind" — the two stopped being the same thing when passkeys
	// arrived, and the login path needs the second: an account with only a passkey
	// must still be challenged, and must not be asked for a code it cannot produce.
	TOTPEnabled bool
	MFAEnabled  bool
	// TOTPPending holds a secret being paired while an authenticator is already
	// active. It is promoted to TOTPSecret on confirmation, and otherwise simply
	// never takes effect: a wrong code, a cancel or a closed tab leave it sitting
	// here until the next pairing attempt overwrites it. That is deliberate —
	// nothing reads it except ConfirmTOTPEnrollment, so a stale value grants
	// nothing, and clearing it eagerly would mean a cancel path that can itself
	// fail. Do not treat its presence as "a pairing is in progress".
	TOTPPending string
	// TOTPLastCounter is the last 30-second time step whose code was accepted.
	// A code is only valid once: within its window it would otherwise work
	// repeatedly, so one shoulder-surfed or phished code could be spent several
	// times — and the challenge token it satisfies lives for five minutes.
	TOTPLastCounter int64
	// SessionEpoch is bumped when previously issued sessions must stop working.
	SessionEpoch int64
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

// CreateFirstUser inserts the first account, and only the first.
//
// Setup is otherwise a check-then-act: NeedsSetup counts, the handler validates,
// and the insert happens later — so two requests arriving together can both pass
// the count and both create an admin. The window is small but the payoff is
// permanent admin on a fresh instance, and a fresh instance is exactly what is
// reachable before anyone is watching. The condition therefore lives in the INSERT
// itself, where SQLite settles it: zero rows affected means somebody else was
// first.
func (s *Store) CreateFirstUser(ctx context.Context, u *User) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO users (username, password_hash, role, email, totp_secret, totp_enabled, read_only, sections, auth_source, created_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM users)`,
		u.Username, u.PasswordHash, orDefault(u.Role, "admin"), strings.TrimSpace(u.Email), u.TOTPSecret, boolToInt(u.TOTPEnabled),
		boolToInt(u.ReadOnly), marshalSections(u.Sections), orDefault(u.AuthSource, "local"), now)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrSetupTaken
	}
	return res.LastInsertId()
}

// CreateUser inserts a new account and returns its assigned ID.
func (s *Store) CreateUser(ctx context.Context, u *User) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO users (username, password_hash, role, email, totp_secret, totp_enabled, read_only, sections, auth_source, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.Username, u.PasswordHash, orDefault(u.Role, "admin"), strings.TrimSpace(u.Email), u.TOTPSecret, boolToInt(u.TOTPEnabled),
		boolToInt(u.ReadOnly), marshalSections(u.Sections), orDefault(u.AuthSource, "local"), now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListUsers returns all accounts (without secrets) for the admin user manager.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, username, password_hash, role, email, totp_secret,
		       EXISTS(SELECT 1 FROM auth_factors f WHERE f.user_id = users.id AND f.kind = 'totp'),
		       EXISTS(SELECT 1 FROM auth_factors f WHERE f.user_id = users.id),
		       totp_pending, totp_last_counter, session_epoch, read_only, sections, auth_source, created_at, last_login_at
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
	if err != nil {
		return err
	}
	// Their factors and sessions go with them. The middleware already refuses a token whose
	// account is gone, so this is housekeeping rather than a gate: ids are
	// AUTOINCREMENT and never reused, so what is left behind is dead rows, not a
	// way in. Nothing else would ever delete them.
	if err := s.DeleteUserFactors(ctx, id); err != nil {
		return err
	}
	return s.DeleteUserSessions(ctx, id)
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
		SELECT id, username, password_hash, role, email, totp_secret,
		       EXISTS(SELECT 1 FROM auth_factors f WHERE f.user_id = users.id AND f.kind = 'totp'),
		       EXISTS(SELECT 1 FROM auth_factors f WHERE f.user_id = users.id),
		       totp_pending, totp_last_counter, session_epoch, read_only, sections, auth_source, created_at, last_login_at
		FROM users WHERE username = ?`, username))
}

// UserByID looks up a user by primary key.
func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUserRow(s.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, role, email, totp_secret,
		       EXISTS(SELECT 1 FROM auth_factors f WHERE f.user_id = users.id AND f.kind = 'totp'),
		       EXISTS(SELECT 1 FROM auth_factors f WHERE f.user_id = users.id),
		       totp_pending, totp_last_counter, session_epoch, read_only, sections, auth_source, created_at, last_login_at
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

// SetTOTP is gone: an account's second factors are rows in auth_factors, and
// "does this account have 2FA" is now derived from whether any exist rather than
// stored beside them. Two places recording the same fact is how an account ends
// up flagged as protected while holding no working factor, or the reverse.

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
	var mfa int
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Email, &u.TOTPSecret, &enabled, &mfa,
		&u.TOTPPending, &u.TOTPLastCounter, &u.SessionEpoch, &readOnly, &sections, &u.AuthSource, &createdAt, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.TOTPEnabled = enabled != 0
	u.MFAEnabled = mfa != 0
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

// SetUserEmail records where a user's own alert e-mails go. It is self-service —
// an account edits its own address — and is also written by the LDAP sync when the
// directory publishes one.
func (s *Store) SetUserEmail(ctx context.Context, id int64, email string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET email = ? WHERE id = ?`, strings.TrimSpace(email), id)
	return err
}

// SetTOTPPending stores a secret for an authenticator being paired while another
// one is still active, without touching the working secret.
func (s *Store) SetTOTPPending(ctx context.Context, userID int64, secret string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET totp_pending = ? WHERE id = ?`, secret, userID)
	return err
}

// PromoteTOTPPending makes the pending secret the active one and clears it. Used
// once the user has proved they can generate codes from the new authenticator.
func (s *Store) PromoteTOTPPending(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_secret = totp_pending, totp_enabled = 1, totp_pending = '' WHERE id = ?`, userID)
	return err
}

// marshalIDs / unmarshalIDs store an id list as JSON, with "" for empty so an
// unset list and an explicitly empty one look the same in the column — both mean
// "no restriction".
func marshalIDs(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	b, _ := json.Marshal(ids)
	return string(b)
}

func unmarshalIDs(raw string) []int64 {
	if raw == "" {
		return nil
	}
	var out []int64
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// SetTOTPLastCounter records the time step whose code was just accepted, so the
// same code cannot be presented again while it is still inside its window.
//
// Written with a `>` guard rather than a plain assignment: two requests carrying
// the same code can race here, and the loser must not be able to move the
// watermark backwards.
func (s *Store) SetTOTPLastCounter(ctx context.Context, userID, counter int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_last_counter = ? WHERE id = ? AND totp_last_counter < ?`, counter, userID, counter)
	return err
}

// SessionEpoch returns the account's current session generation. A token minted
// before the last bump is stale and must be refused.
func (s *Store) SessionEpoch(ctx context.Context, userID int64) (int64, error) {
	var epoch int64
	err := s.db.QueryRowContext(ctx, `SELECT session_epoch FROM users WHERE id = ?`, userID).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return epoch, err
}

// BumpSessionEpoch invalidates every session token already issued for a user.
//
// A JWT is self-contained: nothing about changing a password reaches the copy a
// browser (or a script) already holds, so without this an attacker whose access
// prompted the reset keeps it until the token expires — up to twelve hours of
// full Docker control, granted by the very act meant to revoke it.
func (s *Store) BumpSessionEpoch(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET session_epoch = session_epoch + 1 WHERE id = ?`, userID)
	return err
}
