package store

import (
	"context"
	"time"
)

// ParseRule is a saved log-parsing rule: a regex with named capture groups that
// the Logs view applies to extract structured fields (columns) from log lines.
type ParseRule struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Pattern   string    `json:"pattern"`
	CreatedAt time.Time `json:"createdAt"`
}

// ListParseRules returns all saved log-parsing rules.
func (s *Store) ListParseRules(ctx context.Context) ([]ParseRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, pattern, created_at FROM parse_rules ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ParseRule
	for rows.Next() {
		var r ParseRule
		var created string
		if err := rows.Scan(&r.ID, &r.Name, &r.Pattern, &created); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreateParseRule inserts a parse rule and returns its ID.
func (s *Store) CreateParseRule(ctx context.Context, name, pattern string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO parse_rules (name, pattern, created_at) VALUES (?, ?, ?)`,
		name, pattern, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteParseRule removes a parse rule by ID.
func (s *Store) DeleteParseRule(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM parse_rules WHERE id = ?`, id)
	return err
}
