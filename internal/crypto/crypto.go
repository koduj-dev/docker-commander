// Package crypto provides authenticated symmetric encryption (AES-256-GCM) for
// secrets stored at rest, such as registry credentials. The key is generated
// once and persisted alongside the other server secrets.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// Cipher seals and opens short secrets with AES-GCM. The nonce is random per
// message and prepended to the ciphertext; output is base64 for DB storage.
type Cipher struct {
	aead cipher.AEAD
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
	return &Cipher{aead: aead}, nil
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
