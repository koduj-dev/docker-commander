// Package backup creates and restores a complete snapshot of a Docker Commander
// installation: the SQLite database plus the on-disk project and template files.
//
// Everything the app needs lives under the data dir, and — importantly — that
// includes BOTH secrets keys (the JWT signing secret and the at-rest encryption
// key are rows in the database itself). That makes a backup self-contained: it can
// be restored onto a fresh machine and simply works.
//
// It also means the archive is, in practice, equivalent to the plaintext of every
// stored secret — host TLS keys, the SMTP and LDAP passwords, registry
// credentials — because the key sits next to the ciphertext. So the archive is
// written 0600, and a passphrase can be supplied to encrypt it (AES-256-GCM with
// an Argon2id-derived key). Without one, Create still works but the caller is
// expected to warn.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/argon2"
)

// magic identifies a Docker Commander backup and its format version, so a restore
// fails loudly on an unrelated or future file rather than misreading it.
var magic = []byte("DCBAK1\n")

const (
	// flagPlain / flagEncrypted follow the magic and say how the payload is stored.
	flagPlain     byte = 0
	flagEncrypted byte = 1

	saltLen  = 16
	nonceLen = 12

	// Argon2id parameters for the passphrase. Deliberately heavier than the login
	// hash: a backup is attacked offline, at leisure, and derivation happens once.
	argonTime    = 4
	argonMemory  = 128 * 1024 // 128 MiB
	argonThreads = 4
	argonKeyLen  = 32
)

// ErrPassphraseRequired is returned when restoring an encrypted archive without
// one (or with the wrong one — the two are indistinguishable by design).
var ErrPassphraseRequired = errors.New("backup: this archive is encrypted; a passphrase is required")

// ErrNotABackup is returned when the file isn't a Docker Commander backup.
var ErrNotABackup = errors.New("backup: not a Docker Commander backup archive")

// dataDirEntries are the directories copied verbatim alongside the database.
// Anything else under the data dir (e.g. a tls/ folder written by --make-certs)
// is deliberately left out: it is reproducible and may be machine-specific.
var dataDirEntries = []string{"projects", "project-templates"}

// dbFileName is the database's name inside the archive and in the data dir.
const dbFileName = "docker-commander.db"

// SQLiteBackuper snapshots the live database. The store implements it with
// `VACUUM INTO`, which is the only safe way to copy a WAL-mode database that is
// in use: a plain file copy can miss committed data still in the -wal file, or
// catch a write in progress.
type SQLiteBackuper interface {
	BackupTo(path string) error
}

// Report describes what a backup did and, more importantly, what it left out.
type Report struct {
	// Bytes is the uncompressed size of everything that went in, so an
	// unexpectedly large data dir is visible rather than discovered later.
	Bytes int64
	// SkippedLinks are paths inside the data dir that are symbolic links. Their
	// CONTENTS are not in the backup, and never were — filepath.Walk does not
	// follow links, so the old code stored the link and silently left the data
	// behind. Now they are skipped outright and named here, because a backup that
	// quietly omits a directory is worse than one that refuses to.
	SkippedLinks []string
}

// Create writes a backup of dataDir to out. When passphrase is non-empty the
// payload is encrypted. db may be nil, in which case the database file is copied
// as-is — only safe when the app is not running.
func Create(dataDir, out string, db SQLiteBackuper, passphrase string) (*Report, error) {
	tmpDir, err := os.MkdirTemp("", "dc-backup-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	rep := &Report{}

	// 1. Consistent database snapshot.
	dbSnapshot := filepath.Join(tmpDir, dbFileName)
	if db != nil {
		if err := db.BackupTo(dbSnapshot); err != nil {
			return nil, fmt.Errorf("snapshot database: %w", err)
		}
	} else {
		if err := copyFile(filepath.Join(dataDir, dbFileName), dbSnapshot); err != nil {
			return nil, fmt.Errorf("copy database: %w", err)
		}
	}

	// 2. Build the tar.gz payload in memory-free streaming fashion to a temp file.
	payload := filepath.Join(tmpDir, "payload.tgz")
	if err := writeArchive(payload, dataDir, dbSnapshot, rep); err != nil {
		return nil, err
	}

	// 3. Emit, encrypting if asked. 0600 either way: even encrypted, this file is
	// the whole installation.
	f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Write(magic); err != nil {
		return nil, err
	}
	if passphrase == "" {
		if _, err := f.Write([]byte{flagPlain}); err != nil {
			return nil, err
		}
		if err := streamFile(payload, f); err != nil {
			return nil, err
		}
		return rep, nil
	}
	if _, err := f.Write([]byte{flagEncrypted}); err != nil {
		return nil, err
	}
	if err := encryptTo(payload, f, passphrase); err != nil {
		return nil, err
	}
	return rep, nil
}

// writeArchive tars the database snapshot and the data directories.
func writeArchive(out, dataDir, dbSnapshot string, rep *Report) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	if err := addFile(tw, dbSnapshot, dbFileName); err != nil {
		return err
	}
	// The database counts too. It is usually the largest single thing in the
	// archive, and leaving it out of the total made "1.2 MiB of files" mean the
	// files that happen not to be the database — a number nobody asked for.
	if fi, err := os.Stat(dbSnapshot); err == nil {
		rep.Bytes += fi.Size()
	}
	for _, dir := range dataDirEntries {
		src := filepath.Join(dataDir, dir)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue // nothing created yet
		}
		if err := addTree(tw, src, dir, rep); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func addTree(tw *tar.Writer, root, prefix string, rep *Report) error {
	return filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		name := filepath.ToSlash(filepath.Join(prefix, rel))
		if fi.Mode()&os.ModeSymlink != 0 {
			// Skipped, not stored. Walk never descends into a link, so its contents
			// were never in the archive — storing the link itself only made the
			// backup look complete. Naming it is the useful part: whoever pointed
			// projects/ at another disk has to back that up themselves.
			rep.SkippedLinks = append(rep.SkippedLinks, name)
			return nil
		}
		if fi.Mode().IsRegular() {
			rep.Bytes += fi.Size()
		}
		return addOne(tw, p, name, fi)
	})
}

func addFile(tw *tar.Writer, path, name string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return addOne(tw, path, name, fi)
}

// addOne writes one regular file or directory entry.
func addOne(tw *tar.Writer, path, name string, fi os.FileInfo) error {
	if !fi.Mode().IsRegular() && !fi.IsDir() {
		// Sockets, devices, fifos: nothing to restore. Symlinks are filtered out
		// by the caller, which records them in the report.
		return nil
	}
	hdr, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	if fi.IsDir() && !strings.HasSuffix(hdr.Name, "/") {
		hdr.Name += "/"
	}
	// uid/gid are meaningless on the restore host and would leak the account the
	// server runs as.
	hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return nil
	}
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	_, err = io.Copy(tw, src)
	return err
}

// Restore unpacks an archive into dataDir. It refuses to write over an existing
// installation unless force is set, so a mistyped path can't destroy a running
// instance. The server must not be running: the database is replaced wholesale.
func Restore(archive, dataDir, passphrase string, force bool) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()

	hdr := make([]byte, len(magic)+1)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return ErrNotABackup
	}
	if string(hdr[:len(magic)]) != string(magic) {
		return ErrNotABackup
	}
	encrypted := hdr[len(magic)] == flagEncrypted
	if encrypted && passphrase == "" {
		return ErrPassphraseRequired
	}

	if err := checkTarget(dataDir, force); err != nil {
		return err
	}

	var payload io.Reader = f
	if encrypted {
		plain, derr := decryptAll(f, passphrase)
		if derr != nil {
			return derr
		}
		payload = plain
	}
	return extract(payload, dataDir)
}

// checkTarget refuses to clobber an existing installation without --force.
func checkTarget(dataDir string, force bool) error {
	if force {
		return os.MkdirAll(dataDir, 0o700)
	}
	if _, err := os.Stat(filepath.Join(dataDir, dbFileName)); err == nil {
		return fmt.Errorf("backup: %s already contains an installation — stop the server and pass --force to overwrite it", dataDir)
	}
	return os.MkdirAll(dataDir, 0o700)
}

// extract unpacks the tar.gz into dataDir, jailing every path inside it. A backup
// is normally your own file, but it may have travelled — so a crafted archive
// must not be able to write outside the data dir (the classic "tar slip").
func extract(r io.Reader, dataDir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("backup: unreadable payload (wrong passphrase?): %w", err)
	}
	defer gz.Close()
	root, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("backup: corrupt archive: %w", err)
		}
		dest, err := safeJoin(root, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o700); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Refused outright. Backups no longer contain symlinks — they are
			// skipped and reported at creation — so one here is either from an
			// older archive or from someone crafting it. Either way a symlink is a
			// write path: restoring one and then writing "through" it is how an
			// archive escapes the data dir, and the cheapest way not to have that
			// class of bug is not to create links at all.
			return fmt.Errorf("backup: refusing symlink entry %q → %q: backups do not carry symlinks", hdr.Name, hdr.Linkname)
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				return err
			}
			out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		default:
			// Ignore anything else rather than trying to recreate it.
		}
	}
}

// safeJoin resolves name under root and fails if it would escape.
func safeJoin(root, name string) (string, error) {
	clean := filepath.Clean(filepath.Join(root, filepath.FromSlash(name)))
	if clean != root && !strings.HasPrefix(clean, root+string(filepath.Separator)) {
		return "", fmt.Errorf("backup: refusing entry %q which escapes the data dir", name)
	}
	return clean, nil
}

// --- passphrase encryption -------------------------------------------------

// deriveKey stretches the passphrase with Argon2id.
func deriveKey(passphrase string, salt []byte) []byte {
	return argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
}

// encryptTo writes salt || nonce || ciphertext. The payload is sealed in one
// piece: a backup is read and written whole, and chunking would invite a
// truncation attack for no benefit here.
func encryptTo(payloadPath string, w io.Writer, passphrase string) error {
	plain, err := os.ReadFile(payloadPath)
	if err != nil {
		return err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	block, err := aes.NewCipher(deriveKey(passphrase, salt))
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	sealed := gcm.Seal(nil, nonce, plain, magic) // magic as AAD binds the format
	if _, err := w.Write(salt); err != nil {
		return err
	}
	if _, err := w.Write(nonce); err != nil {
		return err
	}
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(sealed)))
	if _, err := w.Write(n[:]); err != nil {
		return err
	}
	_, err = w.Write(sealed)
	return err
}

// decryptAll reads the remainder of r and returns the plaintext payload.
func decryptAll(r io.Reader, passphrase string) (io.Reader, error) {
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(r, salt); err != nil {
		return nil, ErrNotABackup
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(r, nonce); err != nil {
		return nil, ErrNotABackup
	}
	var n [8]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, ErrNotABackup
	}
	sealed := make([]byte, binary.BigEndian.Uint64(n[:]))
	if _, err := io.ReadFull(r, sealed); err != nil {
		return nil, ErrNotABackup
	}
	block, err := aes.NewCipher(deriveKey(passphrase, salt))
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, sealed, magic)
	if err != nil {
		// GCM can't tell "wrong passphrase" from "tampered": both are auth failures.
		return nil, errors.New("backup: could not decrypt — wrong passphrase, or the archive was modified")
	}
	return strings.NewReader(string(plain)), nil
}

// --- small helpers ---------------------------------------------------------

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func streamFile(path string, w io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}
