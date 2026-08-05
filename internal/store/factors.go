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

// factorName normalises what the owner typed: bounded, and never blank.
func factorName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Authenticator"
	}
	if len(name) > factorNameMax {
		return name[:factorNameMax]
	}
	return name
}

// CreateFactor pairs a new factor and returns its id.
func (s *Store) CreateFactor(ctx context.Context, f *AuthFactor) (int64, error) {
	name := factorName(f.Name)
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

// ErrLastFactor is returned when a delete would leave the account with no second
// factor at all.
var ErrLastFactor = errors.New("store: that is the only second factor on this account")

// DeleteFactor removes one factor, unless it is the last one.
//
// Scoped by user id as well as factor id, so knowing another account's factor id
// achieves nothing — and the "is this the last one?" test is part of the DELETE
// rather than a read before it. Counting first and deleting second is a race two
// concurrent requests win together: both see two factors, both delete, and the
// account is left with none. That is not the self-lockout it looks like — 2FA is
// derived from whether any factor exists, so zero factors means the password alone
// signs in. The guard has to be atomic or it is decoration.
func (s *Store) DeleteFactor(ctx context.Context, id, userID int64) error {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM auth_factors
		WHERE id = ? AND user_id = ?
		  AND (SELECT COUNT(*) FROM auth_factors WHERE user_id = ?) > 1`, id, userID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	// Nothing was deleted: either it is not theirs (or does not exist), or it is
	// the only one they have. Telling those apart needs a second look, and only
	// the owner's own factors are visible to it.
	if _, err := s.FactorByID(ctx, id, userID); err != nil {
		return err // ErrNotFound, or a real failure
	}
	return ErrLastFactor
}

// BurnFactorCounter records the time step a code came from, and that the factor
// was used. The counter must move forward: replaying the same code inside its
// 30-second window has to fail, which is the whole point of storing it.
func (s *Store) BurnFactorCounter(ctx context.Context, id, counter int64) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE auth_factors SET last_counter = ?, last_used_at = ?
		WHERE id = ? AND last_counter < ?`,
		counter, time.Now().UTC().Format(time.RFC3339), id, counter)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	// No row moved means someone else burned this step first — two requests
	// presenting the SAME code at the same moment. Reporting success there hands
	// both of them a session, which is exactly the replay this watermark exists to
	// stop, so the loser has to be told it lost.
	if n == 0 {
		return ErrCounterNotAdvanced
	}
	return nil
}

// ErrCounterNotAdvanced means the time step was already spent.
var ErrCounterNotAdvanced = errors.New("store: that code's time step was already used")

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

// PairPendingFactor turns the account's pending enrolment into a factor, atomically.
//
// The caller has already checked a code against `pending`. Between that check and
// this call, anything could have happened — including the same request arriving
// sixteen times in parallel, which is a POST away. So the claim on the pending
// secret is a compare-and-swap: exactly one caller clears it, and only that caller
// inserts. Without it, one enrolment becomes N factors holding ONE secret, and
// since the replay watermark is per factor, every future code from that
// authenticator becomes spendable N times.
func (s *Store) PairPendingFactor(ctx context.Context, userID int64, pending, name string) (int64, error) {
	if pending == "" {
		return 0, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`UPDATE users SET totp_pending = '' WHERE id = ? AND totp_pending = ?`, userID, pending)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		// Someone else already claimed this enrolment (or it was abandoned).
		return 0, ErrNotFound
	}

	res, err = tx.ExecContext(ctx, `
		INSERT INTO auth_factors (user_id, kind, name, secret, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		userID, FactorKindTOTP, factorName(name), pending, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// DeleteUserFactors removes every factor for a user, for account deletion.
func (s *Store) DeleteUserFactors(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_factors WHERE user_id = ?`, userID)
	return err
}
