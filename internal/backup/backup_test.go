package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeDB stands in for the store's VACUUM INTO snapshot.
type fakeDB struct{ content string }

func (f fakeDB) BackupTo(path string) error {
	return os.WriteFile(path, []byte(f.content), 0o600)
}

// seedDataDir builds a data dir that looks like a real installation.
func seedDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, dbFileName), "ORIGINAL-DB")
	mustWrite(t, filepath.Join(dir, "projects", "shop", "compose.yml"), "services: {}")
	mustWrite(t, filepath.Join(dir, "projects", "shop", "html", "index.html"), "<h1>hi</h1>")
	mustWrite(t, filepath.Join(dir, "project-templates", "7", "compose.yml"), "name: t")
	return dir
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestBackupRestoreRoundTrip(t *testing.T) {
	src := seedDataDir(t)
	archive := filepath.Join(t.TempDir(), "b.tar.gz")

	if err := Create(src, archive, fakeDB{"SNAPSHOT-DB"}, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "restored")
	if err := Restore(archive, dst, "", false); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// The database comes from the SNAPSHOT, not from a raw copy of the live file —
	// that's the whole point of going through VACUUM INTO.
	if got := read(t, filepath.Join(dst, dbFileName)); got != "SNAPSHOT-DB" {
		t.Errorf("database = %q, want the consistent snapshot", got)
	}
	if got := read(t, filepath.Join(dst, "projects", "shop", "compose.yml")); got != "services: {}" {
		t.Errorf("project file not restored: %q", got)
	}
	if got := read(t, filepath.Join(dst, "projects", "shop", "html", "index.html")); got != "<h1>hi</h1>" {
		t.Errorf("nested project file not restored: %q", got)
	}
	if got := read(t, filepath.Join(dst, "project-templates", "7", "compose.yml")); got != "name: t" {
		t.Errorf("template not restored: %q", got)
	}
}

func TestBackupRestoreRoundTrip_Encrypted(t *testing.T) {
	src := seedDataDir(t)
	archive := filepath.Join(t.TempDir(), "b.enc")

	if err := Create(src, archive, fakeDB{"SNAPSHOT-DB"}, "correct horse battery"); err != nil {
		t.Fatalf("create: %v", err)
	}
	// The payload must not be readable without the passphrase.
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("SNAPSHOT-DB")) || bytes.Contains(raw, []byte("services: {}")) {
		t.Error("SECURITY: an encrypted archive still contains plaintext content")
	}

	dst := filepath.Join(t.TempDir(), "restored")
	if err := Restore(archive, dst, "correct horse battery", false); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := read(t, filepath.Join(dst, dbFileName)); got != "SNAPSHOT-DB" {
		t.Errorf("database = %q", got)
	}
}

// PENTEST: a wrong passphrase must fail closed, and must not be distinguishable
// from a tampered archive (GCM treats both as an auth failure).
func TestPentestWrongPassphraseFails(t *testing.T) {
	src := seedDataDir(t)
	archive := filepath.Join(t.TempDir(), "b.enc")
	if err := Create(src, archive, fakeDB{"DB"}, "right"); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "restored")
	err := Restore(archive, dst, "wrong", false)
	if err == nil {
		t.Fatal("SECURITY: restore succeeded with the wrong passphrase")
	}
	if !strings.Contains(err.Error(), "wrong passphrase") {
		t.Errorf("unhelpful error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, dbFileName)); err == nil {
		t.Error("SECURITY: a database was written despite the failure")
	}
}

// PENTEST: an encrypted archive restored without a passphrase must say so rather
// than producing garbage or a partial restore.
func TestPentestEncryptedArchiveNeedsPassphrase(t *testing.T) {
	src := seedDataDir(t)
	archive := filepath.Join(t.TempDir(), "b.enc")
	if err := Create(src, archive, fakeDB{"DB"}, "pw"); err != nil {
		t.Fatal(err)
	}
	if err := Restore(archive, filepath.Join(t.TempDir(), "r"), "", false); !errors.Is(err, ErrPassphraseRequired) {
		t.Errorf("want ErrPassphraseRequired, got %v", err)
	}
}

// PENTEST: tampering with one byte of the ciphertext must be detected.
func TestPentestTamperedArchiveRejected(t *testing.T) {
	src := seedDataDir(t)
	archive := filepath.Join(t.TempDir(), "b.enc")
	if err := Create(src, archive, fakeDB{"DB"}, "pw"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xff // flip a bit in the sealed payload
	if err := os.WriteFile(archive, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(archive, filepath.Join(t.TempDir(), "r"), "pw", false); err == nil {
		t.Error("SECURITY: a tampered archive restored without complaint")
	}
}

func TestRestoreRefusesUnrelatedFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "notabackup.tar.gz")
	mustWrite(t, f, "just some bytes")
	if err := Restore(f, filepath.Join(t.TempDir(), "r"), "", false); !errors.Is(err, ErrNotABackup) {
		t.Errorf("want ErrNotABackup, got %v", err)
	}
}

// Restoring over a live installation must be refused unless forced — a mistyped
// path should not be able to destroy a running instance.
func TestRestoreRefusesExistingInstallation(t *testing.T) {
	src := seedDataDir(t)
	archive := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := Create(src, archive, fakeDB{"NEW"}, ""); err != nil {
		t.Fatal(err)
	}
	existing := t.TempDir()
	mustWrite(t, filepath.Join(existing, dbFileName), "LIVE-DB")

	err := Restore(archive, existing, "", false)
	if err == nil {
		t.Fatal("restore over an existing installation should be refused")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("the error should point at --force: %v", err)
	}
	if got := read(t, filepath.Join(existing, dbFileName)); got != "LIVE-DB" {
		t.Errorf("the live database was modified: %q", got)
	}

	// With --force it goes through.
	if err := Restore(archive, existing, "", true); err != nil {
		t.Fatalf("forced restore: %v", err)
	}
	if got := read(t, filepath.Join(existing, dbFileName)); got != "NEW" {
		t.Errorf("forced restore did not replace the database: %q", got)
	}
}

// writeEvilArchive hand-builds an archive whose entries try to escape the data dir.
func writeEvilArchive(t *testing.T, path string, entries map[string]string, symlinks map[string]string) {
	t.Helper()
	var payload bytes.Buffer
	gz := gzip.NewWriter(&payload)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range symlinks {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: target,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	out.Write(magic)
	out.WriteByte(flagPlain)
	out.Write(payload.Bytes())
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// PENTEST: "tar slip" — an entry whose path climbs out of the data dir must be
// refused. A backup is usually your own file, but it may have been mailed around,
// and restore runs as the service account.
func TestPentestTarSlipRefused(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	outside := filepath.Join(base, "pwned.txt")

	for _, name := range []string{
		"../pwned.txt",
		"../../pwned.txt",
		"projects/../../pwned.txt",
		"/etc/pwned.txt",
	} {
		archive := filepath.Join(t.TempDir(), "evil.tar.gz")
		writeEvilArchive(t, archive, map[string]string{name: "OWNED"}, nil)

		err := Restore(archive, dataDir, "", true)
		if err == nil && name != "/etc/pwned.txt" {
			t.Errorf("SECURITY: entry %q was accepted", name)
		}
		if _, statErr := os.Stat(outside); statErr == nil {
			t.Fatalf("SECURITY: entry %q wrote outside the data dir", name)
		}
	}
}

// PENTEST: a symlink whose target escapes the data dir must be refused too —
// otherwise a later write through that link lands anywhere on the filesystem.
func TestPentestEscapingSymlinkRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	archive := filepath.Join(t.TempDir(), "evil.tar.gz")
	writeEvilArchive(t, archive, nil, map[string]string{
		"projects/escape": "../../../../etc",
	})

	if err := Restore(archive, dataDir, "", true); err == nil {
		t.Error("SECURITY: a symlink escaping the data dir was accepted")
	}
	if fi, err := os.Lstat(filepath.Join(dataDir, "projects", "escape")); err == nil {
		t.Errorf("SECURITY: the escaping symlink was created (%v)", fi.Mode())
	}
}

// The archive must be 0600: it carries the encryption key next to every
// ciphertext, so a world-readable backup is a secret leak.
func TestBackupFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	src := seedDataDir(t)
	for _, pass := range []string{"", "pw"} {
		archive := filepath.Join(t.TempDir(), "b")
		if err := Create(src, archive, fakeDB{"DB"}, pass); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(archive)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("archive mode = %v, want 0600 (passphrase=%q)", fi.Mode().Perm(), pass)
		}
	}
}

// A data dir with no projects yet must still back up and restore cleanly.
func TestBackupWithNoProjects(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, dbFileName), "DB")
	archive := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := Create(dir, archive, fakeDB{"DB"}, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "r")
	if err := Restore(archive, dst, "", false); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := read(t, filepath.Join(dst, dbFileName)); got != "DB" {
		t.Errorf("db = %q", got)
	}
}

// Ownership must not travel: uids mean something different on the restore host.
func TestArchiveDropsOwnership(t *testing.T) {
	src := seedDataDir(t)
	archive := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := Create(src, archive, fakeDB{"DB"}, ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw[len(magic)+1:]))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Uid != 0 || hdr.Gid != 0 || hdr.Uname != "" || hdr.Gname != "" {
			t.Errorf("entry %q leaks ownership: uid=%d gid=%d", hdr.Name, hdr.Uid, hdr.Gid)
		}
	}
}
