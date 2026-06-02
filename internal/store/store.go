// Package store provides a pure-Go SQLite-backed persistence layer.
//
// It uses modernc.org/sqlite which is a CGO-free SQLite implementation,
// so the whole application can be cross-compiled to a single static binary
// for Windows/Linux/macOS without a C toolchain.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/koduj-dev/docker-commander/internal/crypto"
)

// ErrNotFound is returned when a lookup yields no row.
var ErrNotFound = errors.New("store: not found")

// Store wraps the database handle and exposes typed queries.
type Store struct {
	db     *sql.DB
	cipher *crypto.Cipher // used to seal/open registry secrets; set after Open
}

// SetCipher installs the cipher used to encrypt secrets at rest (registry
// credentials). It is wired up once at startup, after the key is loaded.
func (s *Store) SetCipher(c *crypto.Cipher) { s.cipher = c }

// Open opens (creating if necessary) the SQLite database at path and runs
// all pending migrations. A path of ":memory:" yields an ephemeral DB.
func Open(path string) (*Store, error) {
	// _pragma options enable WAL for better concurrency and enforce FKs.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// SQLite handles one writer at a time; keep the pool small and predictable.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

// migrate applies the schema. Each statement is idempotent (IF NOT EXISTS),
// which keeps the first iteration simple; a versioned migration table can be
// introduced later without breaking existing databases.
func (s *Store) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS users (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	username      TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	role          TEXT NOT NULL DEFAULT 'admin',
	totp_secret   TEXT NOT NULL DEFAULT '',
	totp_enabled  INTEGER NOT NULL DEFAULT 0,
	created_at    TEXT NOT NULL,
	last_login_at TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS hosts (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL UNIQUE,
	kind       TEXT NOT NULL,            -- 'local' | 'tcp' | 'ssh'
	address    TEXT NOT NULL DEFAULT '', -- socket path, tcp host:port, or ssh target
	tls_ca     TEXT NOT NULL DEFAULT '',
	tls_cert   TEXT NOT NULL DEFAULT '',
	tls_key    TEXT NOT NULL DEFAULT '',
	host_key   TEXT NOT NULL DEFAULT '', -- pinned SSH host public key (authorized_keys line)
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_log (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id    INTEGER,
	username   TEXT NOT NULL DEFAULT '',
	action     TEXT NOT NULL,
	target     TEXT NOT NULL DEFAULT '',
	detail     TEXT NOT NULL DEFAULT '',
	ip         TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_log(created_at);

CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS webhooks (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	name          TEXT NOT NULL,
	url           TEXT NOT NULL,
	method        TEXT NOT NULL DEFAULT 'POST',
	headers       TEXT NOT NULL DEFAULT '{}',   -- JSON object
	body_template TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS alert_rules (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL,
	enabled     INTEGER NOT NULL DEFAULT 1,
	type        TEXT NOT NULL,                  -- state | resource | log | restart
	target      TEXT NOT NULL DEFAULT '',       -- container name substring; '' or '*' = all
	config      TEXT NOT NULL DEFAULT '{}',     -- type-specific JSON
	severity    TEXT NOT NULL DEFAULT 'warning',
	webhook_id  INTEGER,
	cooldown_sec INTEGER NOT NULL DEFAULT 60,
	email       INTEGER NOT NULL DEFAULT 0,
	created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS alert_events (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	rule_id        INTEGER,
	rule_name      TEXT NOT NULL DEFAULT '',
	type           TEXT NOT NULL DEFAULT '',
	severity       TEXT NOT NULL DEFAULT 'warning',
	container_id   TEXT NOT NULL DEFAULT '',
	container_name TEXT NOT NULL DEFAULT '',
	message        TEXT NOT NULL DEFAULT '',
	value          REAL,
	acknowledged   INTEGER NOT NULL DEFAULT 0,
	created_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_alert_events_created ON alert_events(id DESC);

CREATE TABLE IF NOT EXISTS parse_rules (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL,
	pattern    TEXT NOT NULL,            -- regex with (?<name>…) capture groups
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS registries (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL,
	address    TEXT NOT NULL,            -- registry host, e.g. ghcr.io, registry-1.docker.io, localhost:5000
	username   TEXT NOT NULL DEFAULT '',
	secret_enc TEXT NOT NULL DEFAULT '', -- AES-GCM encrypted password/token
	created_at TEXT NOT NULL
);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return err
	}
	// Additive column migrations for databases created before the column
	// existed. SQLite has no "ADD COLUMN IF NOT EXISTS", so we ignore the
	// duplicate-column error that older-or-newer DBs harmlessly raise.
	for _, alter := range []string{
		`ALTER TABLE hosts ADD COLUMN host_key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE alert_rules ADD COLUMN email INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE users ADD COLUMN read_only INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE users ADD COLUMN sections TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.ExecContext(ctx, alter); err != nil && !isDuplicateColumn(err) {
			return err
		}
	}
	return nil
}

// isDuplicateColumn reports whether err is SQLite's "duplicate column name"
// error, which an idempotent ADD COLUMN migration expects on existing DBs.
func isDuplicateColumn(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate column name")
}
