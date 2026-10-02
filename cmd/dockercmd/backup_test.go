package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/backup"
	"github.com/koduj-dev/docker-commander/internal/store"
)

func fileSum(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

// A data dir without a database is refused. Before, the backup opened it with
// store.Open, which created an empty database and then backed THAT up: on a
// packaged install, `sudo dockercmd --backup x.tar.gz` without --data-dir looked
// in root's config dir and produced a valid-looking backup of nothing.
func TestBackupRefusesAMissingDatabase(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "b.tar.gz")

	_, err := backupDataDir(dir, out, "")
	if err == nil || !strings.Contains(err.Error(), "--data-dir") {
		t.Fatalf("want a refusal that points at --data-dir, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "docker-commander.db")); !os.IsNotExist(err) {
		t.Fatalf("the backup created a database in its source dir (stat err %v)", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("an archive was written for a data dir with no database")
	}
}

// A file that only carries the name is refused and left byte-for-byte alone.
// The old path ran the schema migration on it, adding our tables to someone
// else's SQLite database.
func TestBackupRefusesAndLeavesAForeignDatabase(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "docker-commander.db")
	raw, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE invoices (id INTEGER PRIMARY KEY, total REAL)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	before := fileSum(t, db)

	_, err = backupDataDir(dir, filepath.Join(t.TempDir(), "b.tar.gz"), "")
	if !errors.Is(err, store.ErrNotADatabase) {
		t.Fatalf("want ErrNotADatabase, got %v", err)
	}
	if fileSum(t, db) != before {
		t.Fatal("the backup modified a database it refused")
	}
}

// Backing up must not migrate its source. Drop a table the migration would
// recreate; after the backup it must still be missing from the source.
func TestBackupDoesNotMigrateItsSource(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "docker-commander.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	raw, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP TABLE alert_deliveries`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	if _, err := backupDataDir(dir, filepath.Join(t.TempDir(), "b.tar.gz"), ""); err != nil {
		t.Fatalf("backup: %v", err)
	}

	raw, err = sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var n int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'alert_deliveries'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("the backup ran the schema migration on its source")
	}
}

// The server keeps its database open in WAL mode while a backup runs. A
// read-only snapshot must still see rows that are only in the WAL so far.
func TestBackupOfALiveDatabaseIncludesTheWAL(t *testing.T) {
	dir := t.TempDir()
	live, err := store.Open(filepath.Join(dir, "docker-commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if _, err := live.CreateUser(context.Background(), &store.User{Username: "in-the-wal", Role: "admin", PasswordHash: "h"}); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "b.tar.gz")
	if _, err := backupDataDir(dir, out, ""); err != nil {
		t.Fatalf("backup of a live database: %v", err)
	}

	restored := t.TempDir()
	if err := backup.Restore(out, restored, "", false); err != nil {
		t.Fatalf("restore: %v", err)
	}
	st, err := store.Open(filepath.Join(restored, "docker-commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.UserByUsername(context.Background(), "in-the-wal"); err != nil {
		t.Fatalf("the row written by the live server is missing from the backup: %v", err)
	}
}

// Something that is not a regular file under the database's name is refused.
func TestBackupRefusesADirectoryNamedLikeTheDatabase(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "docker-commander.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := backupDataDir(dir, filepath.Join(t.TempDir(), "b.tar.gz"), ""); err == nil {
		t.Fatal("a directory named docker-commander.db was accepted as a database")
	}
}

// A file of random bytes is SQLite's "not a database": refused as foreign.
func TestBackupCallsGarbageNotADatabase(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker-commander.db"), []byte("this is not sqlite at all, just text padding it out"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := backupDataDir(dir, filepath.Join(t.TempDir(), "b.tar.gz"), "")
	if !errors.Is(err, store.ErrNotADatabase) {
		t.Fatalf("want ErrNotADatabase, got %v", err)
	}
}

// A real database that can't be read is reported as a read failure with its
// cause, not as "no database" and not as a foreign file.
func TestBackupReportsAnUnreadableDatabaseAsItIs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file anyway")
	}
	dir := t.TempDir()
	db := filepath.Join(dir, "docker-commander.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err := os.Chmod(db, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(db, 0o600) })

	_, err = backupDataDir(dir, filepath.Join(t.TempDir(), "b.tar.gz"), "")
	if err == nil {
		t.Fatal("an unreadable database was backed up")
	}
	if errors.Is(err, store.ErrNotADatabase) || strings.Contains(err.Error(), "no database at") {
		t.Fatalf("an unreadable database was misreported: %v", err)
	}
}
