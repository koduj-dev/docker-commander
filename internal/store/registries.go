package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Registry holds credentials for a container image registry. The secret
// (password/token) is encrypted at rest and never returned in listings.
type Registry struct {
	ID        int64
	Name      string
	Address   string
	Username  string
	CreatedAt time.Time
}

// RegistryAuth is the decrypted credential pair used to authenticate to a
// registry for pull/push. It is only assembled server-side, never serialised.
type RegistryAuth struct {
	Address  string
	Username string
	Password string
}

// ListRegistries returns the configured registries without their secrets.
func (s *Store) ListRegistries(ctx context.Context) ([]Registry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, address, username, created_at FROM registries ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Registry
	for rows.Next() {
		var r Registry
		var created string
		if err := rows.Scan(&r.ID, &r.Name, &r.Address, &r.Username, &created); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreateRegistry stores a registry, encrypting the secret. The address is
// normalised so it matches image references later (see NormalizeRegistryHost).
func (s *Store) CreateRegistry(ctx context.Context, name, address, username, secret string) (int64, error) {
	if s.cipher == nil {
		return 0, errors.New("store: cipher not configured")
	}
	enc, err := s.cipher.Encrypt(secret)
	if err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO registries (name, address, username, secret_enc, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		name, NormalizeRegistryHost(address), username, enc,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteRegistry removes a registry by ID.
func (s *Store) DeleteRegistry(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM registries WHERE id = ?`, id)
	return err
}

// AuthByID returns the decrypted credentials for a single registry.
func (s *Store) AuthByID(ctx context.Context, id int64) (*RegistryAuth, error) {
	return s.scanAuth(s.db.QueryRowContext(ctx, `
		SELECT address, username, secret_enc FROM registries WHERE id = ?`, id))
}

// AuthForHost returns the decrypted credentials whose address matches the
// registry host of an image reference, or ErrNotFound if none is configured.
func (s *Store) AuthForHost(ctx context.Context, host string) (*RegistryAuth, error) {
	host = NormalizeRegistryHost(host)
	return s.scanAuth(s.db.QueryRowContext(ctx, `
		SELECT address, username, secret_enc FROM registries WHERE address = ? LIMIT 1`, host))
}

// scanAuth decrypts a credential row.
func (s *Store) scanAuth(row *sql.Row) (*RegistryAuth, error) {
	if s.cipher == nil {
		return nil, errors.New("store: cipher not configured")
	}
	var a RegistryAuth
	var enc string
	err := row.Scan(&a.Address, &a.Username, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if enc != "" {
		pw, err := s.cipher.Decrypt(enc)
		if err != nil {
			return nil, err
		}
		a.Password = pw
	}
	return &a, nil
}

// NormalizeRegistryHost maps the various Docker Hub aliases to a single key so
// a stored "docker.io" credential matches refs like "nginx" or "user/app".
func NormalizeRegistryHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimSuffix(host, "/")
	switch host {
	case "", "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com", "hub.docker.com":
		return "docker.io"
	}
	return host
}
