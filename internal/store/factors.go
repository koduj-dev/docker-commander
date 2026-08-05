package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// AuthFactor is one paired second factor: an authenticator app today, a passkey
// once that lands. The kind is stored rather than implied so both can live in one
// list — an account's factors are a single set, and splitting them into parallel
// tables is how "how many factors does this account have?" ends up with two
// answers.
type AuthFactor struct {
	ID          int64
	UserID      int64
	Kind        string
	Name        string
	Secret      string
	LastCounter int64
	CreatedAt   time.Time
	LastUsedAt  time.Time
}

// FactorKindTOTP is an authenticator app.
const FactorKindTOTP = "totp"

// factorNameMax bounds what the owner can write. The name is theirs and shown
// back to them; it does not need to be long.
const factorNameMax = 64

// CreateFactor pairs a new factor and returns its id.
func (s *Store) CreateFactor(ctx context.Context, f *AuthFactor) (int64, error) {
	name := strings.TrimSpace(f.Name)
	if name == "" {
		name = "Authenticator"
	}
	if len(name) > factorNameMax {
		name = name[:factorNameMax]
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO auth_factors (user_id, kind, name, secret, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		f.UserID, orDefault(f.Kind, FactorKindTOTP), name, f.Secret,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListFactors returns a user's paired factors, oldest first — the order they were
// added is the order that makes sense of "which one is my old phone".
func (s *Store) ListFactors(ctx context.Context, userID int64) ([]AuthFactor, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, kind, name, secret, last_counter, created_at, last_used_at
		FROM auth_factors WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AuthFactor{}
	for rows.Next() {
		var f AuthFactor
		var created, used string
		if err := rows.Scan(&f.ID, &f.UserID, &f.Kind, &f.Name, &f.Secret, &f.LastCounter, &created, &used); err != nil {
			return nil, err
		}
		f.CreatedAt, _ = time.Parse(time.RFC3339, created)
		f.LastUsedAt, _ = time.Parse(time.RFC3339, used)
		out = append(out, f)
	}
	return out, rows.Err()
}

// CountFactors reports how many factors an account has paired.
func (s *Store) CountFactors(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM auth_factors WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

// DeleteFactor removes one factor. Scoped by user id as well as factor id, so
// knowing another account's factor id achieves nothing.
func (s *Store) DeleteFactor(ctx context.Context, id, userID int64) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM auth_factors WHERE id = ? AND user_id = ?`, id, userID)
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

// BurnFactorCounter records the time step a code came from, and that the factor
// was used. The counter must move forward: replaying the same code inside its
// 30-second window has to fail, which is the whole point of storing it.
func (s *Store) BurnFactorCounter(ctx context.Context, id, counter int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE auth_factors SET last_counter = ?, last_used_at = ?
		WHERE id = ? AND last_counter < ?`,
		counter, time.Now().UTC().Format(time.RFC3339), id, counter)
	return err
}

// FactorByID returns one factor, scoped to its owner.
func (s *Store) FactorByID(ctx context.Context, id, userID int64) (*AuthFactor, error) {
	var f AuthFactor
	var created, used string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, kind, name, secret, last_counter, created_at, last_used_at
		FROM auth_factors WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&f.ID, &f.UserID, &f.Kind, &f.Name, &f.Secret, &f.LastCounter, &created, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	f.CreatedAt, _ = time.Parse(time.RFC3339, created)
	f.LastUsedAt, _ = time.Parse(time.RFC3339, used)
	return &f, nil
}

// DeleteUserFactors removes every factor for a user, for account deletion.
func (s *Store) DeleteUserFactors(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_factors WHERE user_id = ?`, userID)
	return err
}
