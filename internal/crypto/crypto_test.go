package crypto

import (
	"crypto/rand"
	"strings"
	"testing"
)

func newCipher(t *testing.T) *Cipher {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	c, err := New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c := newCipher(t)
	for _, pt := range []string{"", "hunter2", "a much longer secret with ünïcödé 🐳 and spaces"} {
		enc, err := c.Encrypt(pt)
		if err != nil {
			t.Fatalf("Encrypt(%q): %v", pt, err)
		}
		got, err := c.Decrypt(enc)
		if err != nil {
			t.Fatalf("Decrypt: %v", err)
		}
		if got != pt {
			t.Errorf("round trip: got %q want %q", got, pt)
		}
	}
}

func TestEncryptIsNondeterministic(t *testing.T) {
	c := newCipher(t)
	a, _ := c.Encrypt("same")
	b, _ := c.Encrypt("same")
	if a == b {
		t.Error("two encryptions of the same plaintext should differ (random nonce)")
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	a := newCipher(t)
	enc, _ := a.Encrypt("secret")
	b := newCipher(t) // different key
	if _, err := b.Decrypt(enc); err == nil {
		t.Error("decrypting with the wrong key should fail")
	}
}

func TestDecryptRejectsGarbage(t *testing.T) {
	c := newCipher(t)
	if _, err := c.Decrypt("not-base64-!!!"); err == nil {
		t.Error("invalid base64 should error")
	}
	if _, err := c.Decrypt("YWJj"); err == nil { // valid base64, too short for nonce
		t.Error("too-short ciphertext should error")
	}
}

func TestNewRejectsBadKeyLength(t *testing.T) {
	if _, err := New([]byte("too short")); err == nil {
		t.Error("a non-16/24/32-byte key should be rejected")
	}
}

func TestTamperedCiphertextFails(t *testing.T) {
	c := newCipher(t)
	enc, _ := c.Encrypt("secret")
	// Flip a character in the middle of the base64 to corrupt the ciphertext.
	b := []byte(enc)
	mid := len(b) / 2
	if b[mid] == 'A' {
		b[mid] = 'B'
	} else {
		b[mid] = 'A'
	}
	if _, err := c.Decrypt(string(b)); err == nil && !strings.Contains(enc, string(b)) {
		t.Error("tampered ciphertext should fail authentication")
	}
}
