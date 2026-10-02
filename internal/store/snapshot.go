package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ErrNotADatabase is returned by OpenSnapshotSource for a file that exists but is
// not a Docker Commander database.
var ErrNotADatabase = errors.New("not a Docker Commander database")

// identityTables are tables every Docker Commander database has had since the
// first release. A file missing any of them is something else that happens to
// carry the name.
var identityTables = []string{"users", "hosts", "settings", "audit_log"}

// OpenSnapshotSource opens an existing database read-only, as the source of a
// backup. Unlike Open it never creates the file and never migrates it.
//
// Open is the wrong tool for this. It creates a missing file and runs the schema
// migration on whatever it is pointed at, so a backup taken through it wrote to
// its own source: a wrong --data-dir produced an empty database and backed THAT
// up, a stray SQLite file called docker-commander.db had our tables added to it,
// and a database from another version was migrated by a command meant to copy
// it. A backup must leave its source exactly as it found it.
func OpenSnapshotSource(path string) (*Store, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("no database at %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, table := range identityTables {
		var n int
		err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n)
		if err != nil {
			_ = db.Close()
			// Only SQLite's own verdict means "not a database". Anything else (an
			// unreadable file, an I/O error, a timeout) is reported as itself, so a
			// real database that couldn't be read isn't called foreign.
			if strings.Contains(err.Error(), "not a database") {
				return nil, fmt.Errorf("%s: %w (%v)", path, ErrNotADatabase, err)
			}
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		if n == 0 {
			_ = db.Close()
			return nil, fmt.Errorf("%s: %w (no %q table)", path, ErrNotADatabase, table)
		}
	}
	return &Store{db: db}, nil
}
