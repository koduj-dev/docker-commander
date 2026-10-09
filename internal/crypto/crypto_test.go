package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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

func TestFingerprintIsDeterministic(t *testing.T) {
	c := newCipher(t)
	a := c.Fingerprint("same-value")
	b := c.Fingerprint("same-value")
	if a != b {
		t.Errorf("Fingerprint should be deterministic: got %q and %q for the same input", a, b)
	}
}

func TestFingerprintDiffersForDifferentInput(t *testing.T) {
	c := newCipher(t)
	values := []string{"secret", "secreT", "secret:0000", "", "a", "b"}
	seen := map[string]string{}
	for _, v := range values {
		fp := c.Fingerprint(v)
		if prev, ok := seen[fp]; ok {
			t.Errorf("Fingerprint(%q) == Fingerprint(%q) == %q, want distinct fingerprints", v, prev, fp)
		}
		seen[fp] = v
	}
}

func TestFingerprintDoesNotLeakPlaintext(t *testing.T) {
	c := newCipher(t)
	for _, pt := range []string{"hunter2", "secret:0000000000000000", "***"} {
		fp := c.Fingerprint(pt)
		if strings.Contains(fp, pt) {
			t.Errorf("Fingerprint(%q) = %q contains the plaintext", pt, fp)
		}
	}
}

// TestFingerprintKeyIsNotTheRawAESKey guards key separation: Fingerprint's
// HMAC must use a subkey derived from the AES key, never the raw key
// itself, so a weakness in one primitive's use of the key can't bleed into
// the other. A raw-key HMAC of an empty message is directly computable here
// and must NOT equal any real fingerprint output.
func TestFingerprintKeyIsNotTheRawAESKey(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	c, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	rawKeyHMAC := func(plain string) string {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(plain))
		return hex.EncodeToString(mac.Sum(nil))
	}
	for _, v := range []string{"", "a", "secret"} {
		if got, raw := c.Fingerprint(v), rawKeyHMAC(v); got == raw {
			t.Errorf("Fingerprint(%q) used the raw AES key directly as its HMAC key: got %q", v, got)
		}
	}
}

func TestFingerprintDiffersAcrossKeys(t *testing.T) {
	a := newCipher(t)
	b := newCipher(t)
	if a.Fingerprint("same") == b.Fingerprint("same") {
		t.Error("Fingerprint must be keyed — two different keys should (almost certainly) yield different fingerprints for the same value")
	}
}
