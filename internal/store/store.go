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
	"os"
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

// ErrBuiltinRole is returned when a built-in role is edited or deleted. They are
// the known-good baseline; the UI offers Duplicate to customise instead.
var ErrBuiltinRole = errors.New("store: built-in roles cannot be modified")

// ErrSetupTaken means the first account already existed by the time this insert
// ran — two setup requests raced and this one lost.
var ErrSetupTaken = errors.New("store: setup already completed")

// ErrRoleInUseAsFallback is returned when deleting the role configured as the
// LDAP fallback. Allowing it would leave the fallback itself dangling — the one
// thing the fallback exists to prevent.
var ErrRoleInUseAsFallback = errors.New("store: role is the configured LDAP fallback")

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
	email         TEXT NOT NULL DEFAULT '',  -- where this account's own alerts go
	totp_secret   TEXT NOT NULL DEFAULT '',
	totp_enabled  INTEGER NOT NULL DEFAULT 0,
	-- A re-pair in progress. Kept separate so abandoning it can never disable the
	-- authenticator that already works.
	totp_pending  TEXT NOT NULL DEFAULT '',
	-- Whether the password was proved when this enrolment was started. Redeeming it
	-- checks this against the account's protection at that moment; see
	-- totp_pending_stepup in the migration list.
	totp_pending_stepup INTEGER NOT NULL DEFAULT 0,
	-- Opt-in: may a passkey alone sign this account in? See the migration list.
	passwordless  INTEGER NOT NULL DEFAULT 0,
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
	emails      TEXT NOT NULL DEFAULT '',      -- JSON list; empty = the instance SMTP "To"
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

-- Live state of level-triggered (resource) alerts, so an alert is a condition
-- with a lifetime rather than a line reprinted every evaluation. Keyed by what
-- the condition is ABOUT — a container's metric — not by the rule that noticed
-- it, so two rules with different severities over the same metric are one
-- incident that escalates, not two competing alerts.
--
-- Persisted rather than kept in memory: without it a restart forgets every
-- firing condition, so nothing would ever resolve across one, and "currently
-- firing" could not be answered at all.
-- One row per delivery attempt of an alert, so "we notified you" is a checkable
-- claim rather than an assumption. A webhook that 500s or an SMTP server that
-- refuses the connection is otherwise invisible: the alert shows in the feed and
-- nobody learns it never left the building.
--
-- NOTE: target deliberately holds the webhook's NAME and host, never its full
-- URL. Webhook URLs routinely carry a token in the path or query, and this table
-- is readable by anyone with the alerts section.
CREATE TABLE IF NOT EXISTS alert_deliveries (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	event_id     INTEGER NOT NULL,
	channel      TEXT    NOT NULL DEFAULT '',
	target       TEXT    NOT NULL DEFAULT '',
	ok           INTEGER NOT NULL DEFAULT 0,
	status       INTEGER NOT NULL DEFAULT 0,
	detail       TEXT    NOT NULL DEFAULT '',
	attempted_at TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_alert_deliveries_event ON alert_deliveries(event_id);

CREATE TABLE IF NOT EXISTS alert_states (
	host_id      INTEGER NOT NULL DEFAULT 0,
	container_id TEXT    NOT NULL DEFAULT '',
	metric       TEXT    NOT NULL DEFAULT '',
	rule_id      INTEGER NOT NULL DEFAULT 0,
	rule_name    TEXT    NOT NULL DEFAULT '',
	severity     TEXT    NOT NULL DEFAULT '',
	container_name TEXT  NOT NULL DEFAULT '',
	host_name    TEXT    NOT NULL DEFAULT '',
	last_value   REAL,
	started_at   TEXT    NOT NULL,
	notified_at  TEXT    NOT NULL,
	PRIMARY KEY (host_id, container_id, metric)
);

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

-- One row per signed-in session, keyed by the token's jti.
--
-- A JWT is self-contained, so without a row per session there is nothing to
-- point at when someone asks "what is signed in as me, and can I stop that one?".
-- The row is also the revocation: the middleware refuses a token whose id is not
-- here, so deleting it takes effect on the very next request.
--
-- ip/user_agent are recorded for recognition — "that is my laptop, this one I do
-- not know" — and are visible only to the account itself.
CREATE TABLE IF NOT EXISTS sessions (
	id           TEXT PRIMARY KEY,        -- the token's jti
	user_id      INTEGER NOT NULL,
	ip           TEXT NOT NULL DEFAULT '',
	user_agent   TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL,
	last_seen_at TEXT NOT NULL,
	expires_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);

-- One paired second factor. Several per account, because people own more than one
-- device and losing the only paired one used to mean losing the account.
--
-- kind is 'totp' today; the column exists so a passkey can be a row here rather
-- than a parallel table with its own list, its own "remove" and its own chance of
-- disagreeing about how many factors an account actually has.
--
-- last_counter is the replay guard, per factor: a code is valid for its 30-second
-- step, so the step it came from is burned. Per factor and not per account, or
-- pairing a second authenticator would let one device's code invalidate the
-- other's.
CREATE TABLE IF NOT EXISTS auth_factors (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id      INTEGER NOT NULL,
	kind         TEXT NOT NULL DEFAULT 'totp',
	name         TEXT NOT NULL DEFAULT '',
	secret       TEXT NOT NULL DEFAULT '',
	last_counter INTEGER NOT NULL DEFAULT 0,
	created_at   TEXT NOT NULL,
	last_used_at TEXT NOT NULL DEFAULT '',
	-- A passkey stores no shared secret: what identifies it is its credential id,
	-- and what verifies it is a public key. Both live in the credential column as
	-- the JSON the WebAuthn library round-trips; credential_id is lifted out of it
	-- because every assertion arrives naming one, so it has to be found by it.
	credential_id TEXT NOT NULL DEFAULT '',
	credential    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_auth_factors_user ON auth_factors(user_id);

-- Named bundles of section grants, assignable to users. See internal/store/roles.go.
CREATE TABLE IF NOT EXISTS roles (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	builtin     INTEGER NOT NULL DEFAULT 0  -- built-ins are read-only (Duplicate to customise)
);

-- One section grant inside a role; write=0 means read-only for that section.
CREATE TABLE IF NOT EXISTS role_sections (
	role_id INTEGER NOT NULL,
	section TEXT NOT NULL,
	write   INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (role_id, section)
);

CREATE TABLE IF NOT EXISTS user_roles (
	user_id INTEGER NOT NULL,
	role_id INTEGER NOT NULL,
	PRIMARY KEY (user_id, role_id)
);

-- Which Docker hosts a role's grants apply to. NO ROWS FOR A ROLE MEANS EVERY
-- HOST — chosen so that upgrading changes nobody's reach (design note D4). The
-- local daemon (host 0) is always in scope and is never stored here: making it
-- scopeable would let a single-host install lock itself out.
CREATE TABLE IF NOT EXISTS role_hosts (
	role_id INTEGER NOT NULL,
	host_id INTEGER NOT NULL,
	PRIMARY KEY (role_id, host_id)
);

CREATE TABLE IF NOT EXISTS projects (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	name         TEXT NOT NULL,            -- user-facing display name
	slug         TEXT NOT NULL UNIQUE,     -- compose project name (-p), [a-z0-9][a-z0-9_-]*
	compose_file TEXT NOT NULL DEFAULT 'compose.yml',
	host_id      INTEGER NOT NULL DEFAULT 0, -- target Docker host (0 = local)
	-- opt-in: let a remote deploy mount bind sources from outside the project
	-- folder (i.e. paths on the remote host). Needs the "hosts" permission.
	allow_remote_host_paths INTEGER NOT NULL DEFAULT 0,
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

-- OAuth 2.1 authorization-server state for the remote MCP server. Clients are
-- registered dynamically (RFC 7591); codes are single-use and short-lived;
-- refresh tokens are rotated on use. Only hashes of codes/refresh-tokens are
-- stored, never the secret itself.
CREATE TABLE IF NOT EXISTS oauth_clients (
	client_id     TEXT PRIMARY KEY,
	client_name   TEXT NOT NULL DEFAULT '',
	redirect_uris TEXT NOT NULL,            -- JSON array, exact-match validated
	created_at    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_codes (
	code_hash      TEXT PRIMARY KEY,
	client_id      TEXT NOT NULL,
	user_id        INTEGER NOT NULL,
	redirect_uri   TEXT NOT NULL,
	code_challenge TEXT NOT NULL,           -- PKCE S256 challenge
	resource       TEXT NOT NULL DEFAULT '',
	scope          TEXT NOT NULL DEFAULT '',
	expires_at     TEXT NOT NULL,
	created_at     TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_refresh_tokens (
	token_hash  TEXT PRIMARY KEY,
	client_id   TEXT NOT NULL,
	user_id     INTEGER NOT NULL,
	scope       TEXT NOT NULL DEFAULT '',
	resource    TEXT NOT NULL DEFAULT '',
	expires_at  TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL
);

-- A vulnerability a human has reviewed and accepted, so a Trivy scan stops
-- re-flagging it. Keyed by CVE id alone, not per-image: the identifier is
-- global, and the same CVE turning up in a different image is still the same
-- reviewed finding, not a new one to triage again.
CREATE TABLE IF NOT EXISTS ignored_cves (
	id         TEXT PRIMARY KEY,        -- e.g. "CVE-2023-12345"
	reason     TEXT NOT NULL DEFAULT '',
	added_by   TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);

-- A specific (service, kind) drift on a project a human has reviewed and
-- deliberately accepted, so the deploy preview stops counting it as active
-- drift. Scoped per project, unlike ignored_cves: accepting a resource-limit
-- drift on one project's "web" service says nothing about the same shape
-- elsewhere. No declared FK (this schema doesn't use them anywhere) —
-- DeleteProject removes matching rows itself.
CREATE TABLE IF NOT EXISTS project_drift_ignores (
	project_id  INTEGER NOT NULL,
	service     TEXT NOT NULL,
	kind        TEXT NOT NULL,
	fingerprint TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL,
	PRIMARY KEY (project_id, service, kind)
);

-- One successful deploy of a project. The row is metadata only — the actual
-- compose file + every sidecar file, as they were at that moment, live on
-- disk as a zip under DataDir/project-revisions/<project_id>/<revision>.zip
-- (see Server.revisionZipPath). Resolved config and structural diffs are
-- always derived fresh from that zip rather than duplicated here, so there is
-- exactly one stored copy of "what did this revision actually contain" to go
-- stale. The revision column numbers 1.. per project (not a global id),
-- because that is the number an operator says out loud ("roll back to
-- revision 4").
CREATE TABLE IF NOT EXISTS project_revisions (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id       INTEGER NOT NULL,
	revision         INTEGER NOT NULL,
	host_id          INTEGER NOT NULL DEFAULT 0,
	profiles         TEXT NOT NULL DEFAULT '',  -- JSON array
	images           TEXT NOT NULL DEFAULT '',  -- JSON [{"service","image","digest"}]
	valid            INTEGER NOT NULL DEFAULT 1,
	validation_error TEXT NOT NULL DEFAULT '',
	output           TEXT NOT NULL DEFAULT '',
	author           TEXT NOT NULL DEFAULT '',
	reason           TEXT NOT NULL DEFAULT '',
	created_at       TEXT NOT NULL,
	UNIQUE (project_id, revision)
);
CREATE INDEX IF NOT EXISTS idx_project_revisions_project ON project_revisions(project_id);

-- A trigger-and-status wrapper around a user-supplied backup command (their
-- own restic/borg/etc, already pointed at its own repository) run against a
-- volume's or project's data. Not a backup engine: no repository, retention
-- or storage logic of our own. last_run_* is denormalized here (mirroring how
-- alert_rules stays separate from alert_events/alert_deliveries) so a
-- volume/project badge is an O(1) lookup rather than a join against
-- backup_runs. env_enc is the job's command environment (e.g.
-- RESTIC_PASSWORD), JSON-encoded then encrypted with the store's cipher —
-- write-only, same pattern as registries.secret_enc.
CREATE TABLE IF NOT EXISTS backup_jobs (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	name             TEXT NOT NULL,
	enabled          INTEGER NOT NULL DEFAULT 1,
	scope            TEXT NOT NULL,             -- 'volume' | 'project'
	volume_name      TEXT NOT NULL DEFAULT '',
	project_id       INTEGER NOT NULL DEFAULT 0,
	host_id          INTEGER NOT NULL DEFAULT 0,
	image            TEXT NOT NULL,
	command          TEXT NOT NULL,             -- run as: sh -c <command>
	env_enc          TEXT NOT NULL DEFAULT '',
	interval_minutes INTEGER NOT NULL DEFAULT 0, -- 0 = manual only
	created_by       TEXT NOT NULL DEFAULT '',
	created_at       TEXT NOT NULL,
	updated_at       TEXT NOT NULL,
	last_run_at      TEXT NOT NULL DEFAULT '',
	last_run_ok      INTEGER NOT NULL DEFAULT 0,
	last_run_detail  TEXT NOT NULL DEFAULT ''
);

-- Append-only run history for a backup job (status feed / audit trail of
-- what actually happened, separate from the job's own config).
CREATE TABLE IF NOT EXISTS backup_runs (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id       INTEGER NOT NULL,
	started_at   TEXT NOT NULL,
	finished_at  TEXT NOT NULL DEFAULT '',
	ok           INTEGER NOT NULL DEFAULT 0,
	exit_code    INTEGER NOT NULL DEFAULT 0,
	output       TEXT NOT NULL DEFAULT '',
	error        TEXT NOT NULL DEFAULT '',
	triggered_by TEXT NOT NULL DEFAULT '' -- 'schedule' or a username
);
CREATE INDEX IF NOT EXISTS idx_backup_runs_job ON backup_runs(job_id);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return err
	}
	// Additive column migrations for databases created before the column
	// existed. SQLite has no "ADD COLUMN IF NOT EXISTS", so we ignore the
	// duplicate-column error that older-or-newer DBs harmlessly raise.
	for _, alter := range []string{
		// Passkey columns, added to the same table so an account's factors stay one
		// list with one answer to "how many do I have".
		`ALTER TABLE auth_factors ADD COLUMN credential_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE auth_factors ADD COLUMN credential TEXT NOT NULL DEFAULT ''`,
		// The user handle a passkey is registered against: opaque, per account, and
		// stable, because it is what the authenticator stores alongside the key.
		`ALTER TABLE users ADD COLUMN webauthn_handle TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE hosts ADD COLUMN host_key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE alert_rules ADD COLUMN email INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE users ADD COLUMN read_only INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE users ADD COLUMN sections TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE hosts ADD COLUMN alert_email TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE alert_events ADD COLUMN host_id INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE alert_events ADD COLUMN host_name TEXT NOT NULL DEFAULT ''`,
		// 'firing' is the default so every event recorded before alerts had a
		// lifecycle reads as what it was: the moment a condition was noticed.
		`ALTER TABLE alert_events ADD COLUMN kind TEXT NOT NULL DEFAULT 'firing'`,
		`ALTER TABLE alert_events ADD COLUMN duration_sec INTEGER NOT NULL DEFAULT 0`,
		// Who acknowledged, and when. Events acknowledged before this existed keep
		// an empty name rather than being attributed to whoever looks next.
		`ALTER TABLE alert_events ADD COLUMN acknowledged_by TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE alert_events ADD COLUMN acknowledged_at TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN auth_source TEXT NOT NULL DEFAULT 'local'`,
		`ALTER TABLE users ADD COLUMN ui_prefs TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE hosts ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE projects ADD COLUMN host_id INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE projects ADD COLUMN allow_remote_host_paths INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN totp_pending TEXT NOT NULL DEFAULT ''`,
		// Whether the password was proved when the pending enrolment was started.
		//
		// A pending secret is a capability created under one authorization state and
		// redeemed later, possibly under another: an enrolment begun while the account
		// had no second factor needs no password, and if it could still be confirmed
		// after the account gained one, a stolen session could stash a secret, wait for
		// the owner to pair a passkey, and then quietly add its own authenticator to a
		// now-protected account. So the authorization travels with the enrolment.
		//
		// Existing rows default to 0, which is correct: whatever is pending on upgrade
		// was started before this existed, and if the account is protected it must not
		// be redeemable without the password.
		`ALTER TABLE users ADD COLUMN totp_pending_stepup INTEGER NOT NULL DEFAULT 0`,
		// Whether this account may be signed into by a passkey alone.
		//
		// Off by default, including on upgrade, and that is the point: turning a
		// passkey from a second factor into a whole login changes what the account
		// rests on, and for a SYNCED passkey it moves that to the platform account
		// the key syncs through. Nobody's security model should change because they
		// updated the app.
		`ALTER TABLE users ADD COLUMN passwordless INTEGER NOT NULL DEFAULT 0`,
		// The last TOTP counter accepted for this account, so a code cannot be
		// replayed inside its own validity window.
		`ALTER TABLE users ADD COLUMN totp_last_counter INTEGER NOT NULL DEFAULT 0`,
		// Bumped when a credential change must invalidate sessions already issued.
		`ALTER TABLE users ADD COLUMN session_epoch INTEGER NOT NULL DEFAULT 0`,
		// Per-rule recipients. Empty keeps the previous behaviour: fall back to the
		// instance-wide SMTP "To" (and the per-host override), so existing rules
		// deliver exactly as before.
		`ALTER TABLE alert_rules ADD COLUMN emails TEXT NOT NULL DEFAULT ''`,
		// Host narrowing for MCP tokens, and the host an audited action happened
		// on. Both default to the pre-scoping meaning: an empty token host list is
		// no restriction, and host 0 is the local daemon.
		`ALTER TABLE api_tokens ADD COLUMN host_ids TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE audit_log ADD COLUMN host_id INTEGER NOT NULL DEFAULT 0`,
		// The profiles used on the project's last successful `compose up`, as a
		// JSON array (same convention as users.sections / api_tokens.host_ids).
		// This is what was actually deployed, not what's merely selected for the
		// next deploy — the two can differ, and that's the whole point of tracking
		// it: a service excluded by these profiles is "not part of the active
		// profile set", not "stopped". Empty means never successfully deployed, or
		// deployed with no profiles.
		`ALTER TABLE projects ADD COLUMN last_deployed_profiles TEXT NOT NULL DEFAULT ''`,
		// Scopes a drift ignore to the specific from/to/detail values reviewed,
		// not just the (service, kind) pair — see ProjectDriftIgnore's doc
		// comment. A pre-existing row from before this column defaults to '',
		// which never matches a real fingerprint, so it simply stops applying
		// rather than mismatching something it was never meant to cover.
		`ALTER TABLE project_drift_ignores ADD COLUMN fingerprint TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.ExecContext(ctx, alter); err != nil && !isDuplicateColumn(err) {
			return err
		}
	}
	if err := s.migrateTOTPToFactors(ctx); err != nil {
		return err
	}
	if err := s.enforceOneFactorPerSecret(ctx); err != nil {
		return err
	}
	// Built-in roles come last: the tables above must exist first, and seeding is
	// idempotent (an existing row is left untouched).
	return s.seedBuiltinRoles(ctx)
}

// enforceOneFactorPerSecret makes "one authenticator, one row" a property of the
// database rather than of the code that happens to write it.
//
// Two rows sharing a secret give that authenticator two independent replay
// watermarks, so every code it produces becomes spendable twice. The pairing path
// is written not to allow it; this is the backstop for the paths nobody thought
// about — a bad migration, a future import, a hand-edited database.
//
// Duplicates are collapsed first (keeping the oldest row and the highest watermark
// among them), because an index that cannot be created would take the whole
// installation down on start.
func (s *Store) enforceOneFactorPerSecret(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE auth_factors SET last_counter = (
			SELECT MAX(d.last_counter) FROM auth_factors d
			WHERE d.user_id = auth_factors.user_id AND d.secret = auth_factors.secret
		) WHERE secret != ''`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM auth_factors WHERE secret != '' AND id NOT IN (
			SELECT MIN(id) FROM auth_factors WHERE secret != '' GROUP BY user_id, secret
		)`); err != nil {
		return err
	}
	// Partial, and every statement above is too. A shared secret is what makes two
	// rows the same authenticator; a factor with NO secret — which is what a passkey
	// will be, since its credential does not live in this column — shares nothing
	// with anything. A plain unique index would let an account hold exactly one of
	// them and silently delete the rest on the next start, which is a trap laid for
	// the very feature the `kind` column exists to allow.
	// The earlier name carried a NON-partial index. `CREATE ... IF NOT EXISTS` would
	// leave it in place — same name, wrong definition — so it goes explicitly.
	if _, err := s.db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_auth_factors_user_secret`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE UNIQUE INDEX IF NOT EXISTS idx_auth_factors_secret_unique
		ON auth_factors(user_id, secret) WHERE secret != ''`); err != nil {
		return err
	}
	// A credential id is globally unique by construction, and must be unique here
	// too: an assertion names one, and two rows answering to it would make "whose
	// passkey is this?" ambiguous — across accounts, not just within one.
	_, err := s.db.ExecContext(ctx, `
		CREATE UNIQUE INDEX IF NOT EXISTS idx_auth_factors_credential_unique
		ON auth_factors(credential_id) WHERE credential_id != ''`)
	return err
}

// migrateTOTPToFactors moves the single per-user authenticator into auth_factors.
//
// The secret is CLEARED from users afterwards, on purpose. Leaving it would mean a
// live second factor sitting in a column nothing reads: remove the authenticator
// from your profile and the old secret would still be there, no longer visible and
// no longer removable. Dead credentials are worse than none.
//
// Idempotent by the same test that drives it: a user is migrated only while their
// secret is still in the old column, and the move clears it.
func (s *Store) migrateTOTPToFactors(ctx context.Context) error {
	// One transaction, because the two halves are one fact. SQLite autocommits each
	// statement, so an INSERT that commits and an UPDATE that then fails (BUSY, a
	// full disk, a kill between them) leaves the secret in BOTH places: startup
	// aborts, and the next start re-runs the INSERT and pairs the same authenticator
	// twice. Two factors, one secret, one watermark each — every code from that
	// device replayable, for every 2FA user in the installation at once.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO auth_factors (user_id, kind, name, secret, last_counter, created_at)
		SELECT id, 'totp', 'Authenticator', totp_secret, totp_last_counter, ?
		FROM users WHERE totp_secret != '' AND totp_enabled = 1`, now); err != nil {
		return err
	}
	// Every legacy secret goes, not just the confirmed ones. An unconfirmed
	// enrolment left behind is a scanned secret sitting in a column nothing reads
	// and nobody can remove — the same "dead credential" this migration exists to
	// avoid, minus the excuse.
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET totp_secret = '', totp_last_counter = 0 WHERE totp_secret != ''`); err != nil {
		return err
	}
	return tx.Commit()
}

// isDuplicateColumn reports whether err is SQLite's "duplicate column name"
// error, which an idempotent ADD COLUMN migration expects on existing DBs.
func isDuplicateColumn(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate column name")
}

// BackupTo writes a consistent snapshot of the database to path using
// `VACUUM INTO`. The database runs in WAL mode, so copying the file directly is
// unsafe: committed data can still live in the -wal file, and a copy taken during
// a write yields a torn database. VACUUM INTO takes the snapshot through the
// live connection instead, so it is safe while the server is running.
func (s *Store) BackupTo(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err == nil {
		// VACUUM INTO refuses to overwrite, and a stale file would fail the backup
		// for a confusing reason.
		return fmt.Errorf("store: %s already exists", path)
	}
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}
