package store

import (
	"context"
	"strings"
	"time"
)

// IgnoredCVE is a vulnerability finding a human has reviewed and accepted, so
// a Trivy scan stops re-flagging it. See the schema comment in store.go for
// why this is keyed by CVE id alone rather than per-image.
type IgnoredCVE struct {
	ID        string    `json:"id"`
	Reason    string    `json:"reason"`
	AddedBy   string    `json:"addedBy"`
	CreatedAt time.Time `json:"createdAt"`
}

// ListIgnoredCVEs returns every reviewed/accepted CVE, newest first.
func (s *Store) ListIgnoredCVEs(ctx context.Context) ([]IgnoredCVE, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, reason, added_by, created_at FROM ignored_cves ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IgnoredCVE{}
	for rows.Next() {
		var c IgnoredCVE
		var created string
		if err := rows.Scan(&c.ID, &c.Reason, &c.AddedBy, &created); err != nil {
			return nil, err
		}
		c.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, c)
	}
	return out, rows.Err()
}

// IgnoreCVEs bulk-inserts ids as reviewed/accepted, attributed to addedBy.
// Blank/whitespace-only ids are dropped rather than stored as junk rows. An id
// already ignored is left as-is (its original reason/author/date survive a
// re-submission of the same bulk selection) rather than overwritten.
func (s *Store) IgnoreCVEs(ctx context.Context, ids []string, reason, addedBy string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed
	now := time.Now().UTC().Format(time.RFC3339)
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO ignored_cves (id, reason, added_by, created_at) VALUES (?, ?, ?, ?)
			 ON CONFLICT(id) DO NOTHING`,
			id, reason, addedBy, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UnignoreCVE removes one CVE from the ignored list, so future scans flag it
// again.
func (s *Store) UnignoreCVE(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM ignored_cves WHERE id = ?`, id)
	return err
}
