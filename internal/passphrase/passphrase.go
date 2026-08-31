// Package passphrase provides the passphrase-based archive encryption shared
// by internal/backup and the recovery bundle: a caller-chosen "magic" prefix
// identifies the archive format and doubles as the AEAD's associated data (so
// a sealed payload can't be replayed under a different format's magic), and
// the passphrase itself is stretched with Argon2id before sealing the payload
// with AES-256-GCM. Without a passphrase, the payload is written plain,
// behind the same magic+flag framing, so a reader can always tell which case
// it's looking at from the first few bytes alone.
package passphrase

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

const (
	flagPlain     byte = 0
	flagEncrypted byte = 1

	saltLen  = 16
	nonceLen = 12

	// Argon2id parameters. Deliberately heavier than the login hash: an
	// archive is attacked offline, at leisure, and derivation happens once.
	argonTime    = 4
	argonMemory  = 128 * 1024 // 128 MiB
	argonThreads = 4
	argonKeyLen  = 32
)

// ErrPassphraseRequired is returned when opening an encrypted archive without
// one (or with the wrong one — the two are indistinguishable by design).
var ErrPassphraseRequired = errors.New("passphrase: this archive is encrypted; a passphrase is required")

// ErrBadMagic is returned when the leading bytes don't match the caller's
// expected magic — the file isn't this kind of archive at all.
var ErrBadMagic = errors.New("passphrase: not a recognised archive for this magic")

// WritePlainTo writes magic || flagPlain, then streams r into w unmodified.
// Used when the caller passed no passphrase: the payload is never buffered
// in memory just to satisfy this framing.
func WritePlainTo(w io.Writer, magic []byte, r io.Reader) error {
	if _, err := w.Write(magic); err != nil {
		return err
	}
	if _, err := w.Write([]byte{flagPlain}); err != nil {
		return err
	}
	_, err := io.Copy(w, r)
	return err
}

// SealTo writes magic || flagEncrypted || salt || nonce || len || sealed, where
// sealed is AES-256-GCM(plain) under an Argon2id-derived key, with magic as
// the AEAD's associated data. The whole plaintext is sealed as one unit —
// chunking would invite a truncation attack for no benefit here — so plain
// must fit in memory, same as the encrypted path this replaces in
// internal/backup.
func SealTo(w io.Writer, magic []byte, plain []byte, passphrase string) error {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	gcm, err := newGCM(passphrase, salt)
	if err != nil {
		return err
	}
	sealed := gcm.Seal(nil, nonce, plain, magic)

	if _, err := w.Write(magic); err != nil {
		return err
	}
	if _, err := w.Write([]byte{flagEncrypted}); err != nil {
		return err
	}
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

// ReadFlag reads and verifies the magic prefix from r and returns whether the
// payload that follows is encrypted. Callers read this first, then dispatch
// to either a plain io.Copy of the remainder or OpenFrom.
func ReadFlag(r io.Reader, magic []byte) (encrypted bool, err error) {
	hdr := make([]byte, len(magic)+1)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return false, ErrBadMagic
	}
	if string(hdr[:len(magic)]) != string(magic) {
		return false, ErrBadMagic
	}
	return hdr[len(magic)] == flagEncrypted, nil
}

// OpenFrom reads salt || nonce || len || sealed from r (immediately after
// ReadFlag has consumed the magic+flag prefix) and returns the decrypted
// plaintext.
func OpenFrom(r io.Reader, magic []byte, passphrase string) ([]byte, error) {
	if passphrase == "" {
		return nil, ErrPassphraseRequired
	}
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(r, salt); err != nil {
		return nil, ErrBadMagic
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(r, nonce); err != nil {
		return nil, ErrBadMagic
	}
	var n [8]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, ErrBadMagic
	}
	sealed := make([]byte, binary.BigEndian.Uint64(n[:]))
	if _, err := io.ReadFull(r, sealed); err != nil {
		return nil, ErrBadMagic
	}
	gcm, err := newGCM(passphrase, salt)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, sealed, magic)
	if err != nil {
		// GCM can't tell "wrong passphrase" from "tampered": both are auth failures.
		return nil, fmt.Errorf("passphrase: could not decrypt — wrong passphrase, or the archive was modified")
	}
	return plain, nil
}

func newGCM(passphrase string, salt []byte) (cipher.AEAD, error) {
	key := argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
