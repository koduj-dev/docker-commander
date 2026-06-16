package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Host describes a Docker engine endpoint the app can connect to.
//
// Kind is one of:
//   - "local": the local daemon (unix socket / windows named pipe)
//   - "tcp":   a remote daemon over TCP, optionally TLS-secured
//   - "ssh":   a remote daemon reached through an SSH tunnel
type Host struct {
	ID         int64
	Name       string
	Kind       string
	Address    string
	TLSCA      string
	TLSCert    string
	TLSKey     string
	HostKey    string // pinned SSH host public key (authorized_keys line); ssh hosts only
	AlertEmail string // per-host alert recipient override (falls back to global SMTP To)
	Disabled   bool   // when true the monitor ignores this host (no events/stats)
	CreatedAt  time.Time
}

// ListHosts returns all configured hosts ordered by name.
func (s *Store) ListHosts(ctx context.Context) ([]Host, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, kind, address, tls_ca, tls_cert, tls_key, host_key, alert_email, disabled, created_at
		FROM hosts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Host
	for rows.Next() {
		var h Host
		var created string
		var disabled int
		if err := rows.Scan(&h.ID, &h.Name, &h.Kind, &h.Address, &h.TLSCA, &h.TLSCert, &h.TLSKey, &h.HostKey, &h.AlertEmail, &disabled, &created); err != nil {
			return nil, err
		}
		h.Disabled = disabled != 0
		h.CreatedAt, _ = time.Parse(time.RFC3339, created)
		s.decryptHostKey(&h)
		out = append(out, h)
	}
	return out, rows.Err()
}

// HostByID returns a single host or ErrNotFound.
func (s *Store) HostByID(ctx context.Context, id int64) (*Host, error) {
	var h Host
	var created string
	var disabled int
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, kind, address, tls_ca, tls_cert, tls_key, host_key, alert_email, disabled, created_at
		FROM hosts WHERE id = ?`, id).
		Scan(&h.ID, &h.Name, &h.Kind, &h.Address, &h.TLSCA, &h.TLSCert, &h.TLSKey, &h.HostKey, &h.AlertEmail, &disabled, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	h.Disabled = disabled != 0
	h.CreatedAt, _ = time.Parse(time.RFC3339, created)
	s.decryptHostKey(&h)
	return &h, nil
}

// CreateHost inserts a new host and returns its ID. The TLS private key is
// encrypted at rest (CA and client cert are public, so they're stored as-is).
func (s *Store) CreateHost(ctx context.Context, h *Host) (int64, error) {
	key := h.TLSKey
	if key != "" && s.cipher != nil {
		enc, err := s.cipher.Encrypt(key)
		if err != nil {
			return 0, err
		}
		key = enc
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO hosts (name, kind, address, tls_ca, tls_cert, tls_key, alert_email, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		h.Name, h.Kind, h.Address, h.TLSCA, h.TLSCert, key, h.AlertEmail,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// decryptHostKey decrypts a host's TLS private key in place. It's best-effort:
// a value that isn't ciphertext (a legacy plaintext row) is left untouched, so
// existing hosts keep working until re-encrypted.
func (s *Store) decryptHostKey(h *Host) {
	if h.TLSKey == "" || s.cipher == nil {
		return
	}
	if pk, err := s.cipher.Decrypt(h.TLSKey); err == nil {
		h.TLSKey = pk
	}
}

// EncryptPlaintextHostKeys re-encrypts any host TLS private key still stored in
// plaintext (rows created before encryption-at-rest). Called once at startup,
// after the cipher is set; a no-op when there's nothing to migrate.
func (s *Store) EncryptPlaintextHostKeys(ctx context.Context) error {
	if s.cipher == nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, tls_key FROM hosts WHERE tls_key != ''`)
	if err != nil {
		return err
	}
	type row struct {
		id  int64
		key string
	}
	var plaintext []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.key); err != nil {
			rows.Close()
			return err
		}
		if _, err := s.cipher.Decrypt(r.key); err != nil {
			plaintext = append(plaintext, r) // not ciphertext → needs encrypting
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range plaintext {
		enc, err := s.cipher.Encrypt(r.key)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE hosts SET tls_key = ? WHERE id = ?`, enc, r.id); err != nil {
			return err
		}
	}
	return nil
}

// SetHostAlertEmail sets a host's per-host alert recipient override.
func (s *Store) SetHostAlertEmail(ctx context.Context, id int64, email string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE hosts SET alert_email = ? WHERE id = ?`, email, id)
	return err
}

// SetHostDisabled toggles whether the monitor ignores a host.
func (s *Store) SetHostDisabled(ctx context.Context, id int64, disabled bool) error {
	v := 0
	if disabled {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE hosts SET disabled = ? WHERE id = ?`, v, id)
	return err
}

// DeleteHost removes a host by ID.
func (s *Store) DeleteHost(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM hosts WHERE id = ?`, id)
	return err
}

// SetHostKey pins (or clears, when key is "") the trusted SSH host public key
// for a host. Subsequent connections verify the daemon's key against it.
func (s *Store) SetHostKey(ctx context.Context, id int64, key string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE hosts SET host_key = ? WHERE id = ?`, key, id)
	return err
}

// EnsureLocalHost guarantees a "local" host row exists so the app is usable
// immediately on first run without manual host configuration.
func (s *Store) EnsureLocalHost(ctx context.Context) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM hosts WHERE kind = 'local'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := s.CreateHost(ctx, &Host{Name: "local", Kind: "local"})
	return err
}
