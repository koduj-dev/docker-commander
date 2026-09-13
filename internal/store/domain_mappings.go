package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// DomainMapping records a human's intent to route a public domain to one
// service+port inside a project, for the (not-yet-built) embedded reverse
// proxy — see NEXT.md's "Per-container domain + TLS". This stores intent
// only: nothing listens on Domain until the proxy engine itself ships.
type DomainMapping struct {
	ID         int64     `json:"id"`
	ProjectID  int64     `json:"projectId"`
	Domain     string    `json:"domain"`     // lowercase FQDN, globally unique across all projects
	Service    string    `json:"service"`    // compose service name within the project
	TargetPort int       `json:"targetPort"` // container port the service listens on
	TLSMode    string    `json:"tlsMode"`    // "acme" (only mode Phase 1 accepts) | "none" (reserved)
	CreatedBy  string    `json:"createdBy"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// ListDomainMappings returns a project's domain mappings, ordered by domain.
func (s *Store) ListDomainMappings(ctx context.Context, projectID int64) ([]DomainMapping, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, domain, service, target_port, tls_mode, created_by, created_at, updated_at
		FROM domain_mappings WHERE project_id = ? ORDER BY domain`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Never nil: the API always returns "[]", not "null", for an empty
	// list — the frontend uses a nil vs. non-nil array specifically to
	// distinguish "still loading" from "loaded, no mappings yet".
	out := make([]DomainMapping, 0)
	for rows.Next() {
		m, err := scanDomainMapping(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DomainMappingByDomain looks up a mapping by its domain alone (not
// project-scoped) — the lookup the Phase-2 proxy's routing/HostPolicy needs.
func (s *Store) DomainMappingByDomain(ctx context.Context, domain string) (*DomainMapping, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, domain, service, target_port, tls_mode, created_by, created_at, updated_at
		FROM domain_mappings WHERE domain = ?`, domain)
	m, err := scanDomainMapping(row)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

type domainMappingScanner interface {
	Scan(dest ...any) error
}

func scanDomainMapping(row domainMappingScanner) (DomainMapping, error) {
	var m DomainMapping
	var created, updated string
	err := row.Scan(&m.ID, &m.ProjectID, &m.Domain, &m.Service, &m.TargetPort, &m.TLSMode, &m.CreatedBy, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return DomainMapping{}, ErrNotFound
	}
	if err != nil {
		return DomainMapping{}, err
	}
	m.CreatedAt, _ = time.Parse(time.RFC3339, created)
	m.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return m, nil
}

// CreateDomainMapping stores a new mapping. Returns ErrDuplicate if the
// domain is already mapped (by this project or any other — a public
// hostname can only ever point at one place).
func (s *Store) CreateDomainMapping(ctx context.Context, projectID int64, domain, service string, targetPort int, tlsMode, createdBy string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO domain_mappings (project_id, domain, service, target_port, tls_mode, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, domain, service, targetPort, tlsMode, createdBy, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrDuplicate
		}
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateDomainMapping changes a mapping's target and/or TLS mode. The domain
// itself is immutable — delete and recreate to repoint a hostname, the same
// convention project secrets uses for its own immutable key (the name).
func (s *Store) UpdateDomainMapping(ctx context.Context, projectID, id int64, service string, targetPort int, tlsMode string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `
		UPDATE domain_mappings SET service = ?, target_port = ?, tls_mode = ?, updated_at = ?
		WHERE id = ? AND project_id = ?`,
		service, targetPort, tlsMode, now, id, projectID)
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

// DeleteDomainMapping removes one mapping.
func (s *Store) DeleteDomainMapping(ctx context.Context, projectID, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM domain_mappings WHERE id = ? AND project_id = ?`, id, projectID)
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

// deleteDomainMappings removes every mapping for a project, called from
// DeleteProject — a mapping describes this specific project's routing and
// outlives nothing useful once it's gone.
func (s *Store) deleteDomainMappings(ctx context.Context, projectID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM domain_mappings WHERE project_id = ?`, projectID)
	return err
}
