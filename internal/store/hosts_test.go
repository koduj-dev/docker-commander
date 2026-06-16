package store

import (
	"testing"

	"github.com/koduj-dev/docker-commander/internal/crypto"
)

func newCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	c, err := crypto.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestHostTLSKeyEncryptedAtRest(t *testing.T) {
	s, ctx := newStore(t)
	s.SetCipher(newCipher(t))

	id, err := s.CreateHost(ctx, &Host{Name: "edge", Kind: "tcp", Address: "tcp://x:2376", TLSCA: "CA", TLSCert: "CERT", TLSKey: "PRIVATE-KEY-PEM"})
	if err != nil {
		t.Fatal(err)
	}

	// The private key must be encrypted in the column; CA/cert stay as-is (public).
	var rawKey, rawCA string
	if err := s.db.QueryRowContext(ctx, `SELECT tls_key, tls_ca FROM hosts WHERE id = ?`, id).Scan(&rawKey, &rawCA); err != nil {
		t.Fatal(err)
	}
	if rawKey == "PRIVATE-KEY-PEM" || rawKey == "" {
		t.Errorf("tls_key not encrypted at rest: %q", rawKey)
	}
	if rawCA != "CA" {
		t.Errorf("tls_ca should be stored as-is (public), got %q", rawCA)
	}

	// Reads decrypt transparently.
	h, err := s.HostByID(ctx, id)
	if err != nil || h.TLSKey != "PRIVATE-KEY-PEM" {
		t.Errorf("HostByID key = %q (err %v)", h.TLSKey, err)
	}
	hosts, _ := s.ListHosts(ctx)
	for _, x := range hosts {
		if x.ID == id && x.TLSKey != "PRIVATE-KEY-PEM" {
			t.Errorf("ListHosts key = %q", x.TLSKey)
		}
	}
}

func TestEncryptPlaintextHostKeysMigration(t *testing.T) {
	s, ctx := newStore(t)

	// Simulate a legacy row written before encryption-at-rest: insert the key
	// in plaintext directly, bypassing CreateHost's encryption.
	res, err := s.db.ExecContext(ctx, `INSERT INTO hosts (name, kind, address, tls_ca, tls_cert, tls_key, alert_email, created_at)
		VALUES ('legacy','tcp','tcp://x','','','LEGACY-PLAINTEXT','','2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()

	// Migrate (the store already has a cipher from newStore).
	if err := s.EncryptPlaintextHostKeys(ctx); err != nil {
		t.Fatal(err)
	}
	var raw string
	_ = s.db.QueryRowContext(ctx, `SELECT tls_key FROM hosts WHERE id = ?`, id).Scan(&raw)
	if raw == "LEGACY-PLAINTEXT" {
		t.Error("legacy key was not migrated to ciphertext")
	}
	if h, _ := s.HostByID(ctx, id); h.TLSKey != "LEGACY-PLAINTEXT" {
		t.Errorf("decrypted key = %q, want LEGACY-PLAINTEXT", h.TLSKey)
	}

	// Idempotent: a second run must not double-encrypt.
	if err := s.EncryptPlaintextHostKeys(ctx); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.HostByID(ctx, id); h.TLSKey != "LEGACY-PLAINTEXT" {
		t.Errorf("second migration corrupted the key: %q", h.TLSKey)
	}
}
