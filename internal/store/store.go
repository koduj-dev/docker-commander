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

	_ "modernc.org/sqlite" // CGO-free SQLite driver, registered with database/sql

	"github.com/koduj-dev/docker-commander/internal/crypto"
)

// ErrNotFound is returned when a lookup yields no row.
var ErrNotFound = errors.New("store: not found")

// ErrDuplicate is returned when an insert violates a UNIQUE constraint
// (e.g. a project slug that already exists).
var ErrDuplicate = errors.New("store: duplicate")

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

// Ping checks that the database is reachable (used by the health endpoint).
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

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

CREATE TABLE IF NOT EXISTS projects (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	name         TEXT NOT NULL,            -- user-facing display name
	slug         TEXT NOT NULL UNIQUE,     -- compose project name (-p), [a-z0-9][a-z0-9_-]*
	compose_file TEXT NOT NULL DEFAULT 'compose.yml',
	created_by   TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL
);

-- User-saved project presets. Metadata lives here; the scaffold files live on
-- disk under DataDir/project-templates/{id}/ (mirrors how projects are stored).
CREATE TABLE IF NOT EXISTS project_templates (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL,
	slug        TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	created_by  TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL
);

-- User-defined builder service blocks (the "skladacka"). Each is a single
-- compose service fragment stored inline.
CREATE TABLE IF NOT EXISTS service_blocks (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	name         TEXT NOT NULL,
	slug         TEXT NOT NULL UNIQUE,
	description  TEXT NOT NULL DEFAULT '',
	service      TEXT NOT NULL,
	service_yaml TEXT NOT NULL,
	volumes      TEXT NOT NULL DEFAULT '',  -- JSON array of top-level volume names
	created_by   TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL
);

-- User-saved builder "shared definitions": a top-level compose fragment (a YAML
-- anchor, e.g. "x-common: &common ...") emitted above services: so any service
-- can merge it with "<<: *common".
CREATE TABLE IF NOT EXISTS compose_fragments (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL,
	slug        TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	content     TEXT NOT NULL,
	created_by  TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL
);

-- Long-lived bearer tokens for programmatic (MCP) access. The token itself is a
-- high-entropy random secret shown once; only its SHA-256 is stored. A token is
-- scoped to its owning user and can only ever NARROW that user's rights:
-- sections (JSON array, empty = inherit all of the user's sections) and
-- read_only (ORs with the user's own read-only flag). It never widens access.
CREATE TABLE IF NOT EXISTS api_tokens (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id      INTEGER NOT NULL,
	token_hash   TEXT NOT NULL UNIQUE,
	name         TEXT NOT NULL DEFAULT '',
	sections     TEXT NOT NULL DEFAULT '',  -- JSON array; empty = inherit user's sections
	read_only    INTEGER NOT NULL DEFAULT 0,
	created_at   TEXT NOT NULL,
	last_used_at TEXT NOT NULL DEFAULT '',
	expires_at   TEXT NOT NULL DEFAULT '',  -- empty = never expires
	revoked      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens(user_id);
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
		`ALTER TABLE hosts ADD COLUMN alert_email TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE alert_events ADD COLUMN host_id INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE alert_events ADD COLUMN host_name TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN auth_source TEXT NOT NULL DEFAULT 'local'`,
		`ALTER TABLE users ADD COLUMN ui_prefs TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE hosts ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0`,
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
