// Package crypto provides authenticated symmetric encryption (AES-256-GCM) for
// secrets stored at rest, such as registry credentials. The key is generated
// once and persisted alongside the other server secrets.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// fingerprintHKDFInfo is the fixed HKDF "info" context that separates
// Fingerprint's HMAC subkey from the raw AES-GCM key it's derived from — see
// New. Changing this string changes every Fingerprint output.
const fingerprintHKDFInfo = "docker-commander fingerprint hmac subkey v1"

// Cipher seals and opens short secrets with AES-GCM. The nonce is random per
// message and prepended to the ciphertext; output is base64 for DB storage.
type Cipher struct {
	aead    cipher.AEAD
	hmacKey []byte // Fingerprint's key — HKDF-derived from the AES key, never the raw key itself (key separation: the two primitives must not share key material)
}

// New returns a Cipher for a 16/24/32-byte key (32 = AES-256).
func New(key []byte) (*Cipher, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: bad key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	hmacKey, err := hkdf.Key(sha256.New, key, nil, fingerprintHKDFInfo, sha256.Size)
	if err != nil {
		return nil, fmt.Errorf("crypto: deriving fingerprint subkey: %w", err)
	}
	return &Cipher{aead: aead, hmacKey: hmacKey}, nil
}

// Fingerprint returns a deterministic, keyed digest of plain: the same value
// always yields the same fingerprint, a different value yields a different
// one, and the digest cannot be reversed to recover plain. It must stay a
// keyed HMAC, never a bare sha256(plain) — an unkeyed hash would let an
// attacker precompute digests of common/weak values and match them against
// a fingerprint shown in a UI, defeating the point of masking. The HMAC key
// is a subkey HKDF-derived from the AES key (see New), never the raw AES key
// itself, so the two primitives never share key material.
func (c *Cipher) Fingerprint(plain string) string {
	mac := hmac.New(sha256.New, c.hmacKey)
	mac.Write([]byte(plain))
	return hex.EncodeToString(mac.Sum(nil))
}

// Encrypt seals plaintext and returns base64(nonce || ciphertext).
func (c *Cipher) Encrypt(plain string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt. A wrong key or tampered ciphertext yields an error.
func (c *Cipher) Decrypt(enc string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("crypto: ciphertext too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	plain, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("crypto: decrypt failed: %w", err)
	}
	return string(plain), nil
}
