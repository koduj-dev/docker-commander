package tlscert

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func parseCert(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("not a CERTIFICATE PEM block")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return c
}

func TestGenerateSelfSigned(t *testing.T) {
	certPEM, keyPEM, err := GenerateSelfSigned([]string{"example.lan", "10.0.0.5"})
	if err != nil {
		t.Fatal(err)
	}
	cert := parseCert(t, certPEM)

	// Self-signed leaf: the signature verifies against the cert's own key.
	// (CheckSignatureFrom would wrongly require CA constraints on the parent.)
	if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
		t.Errorf("self-signature does not verify: %v", err)
	}
	// Server cert, not a CA.
	if cert.IsCA {
		t.Error("expected a leaf cert, got IsCA=true")
	}
	if !hasExtKeyUsage(cert, x509.ExtKeyUsageServerAuth) {
		t.Error("missing ExtKeyUsageServerAuth")
	}
	// Modern ECDSA key.
	if cert.PublicKeyAlgorithm != x509.ECDSA {
		t.Errorf("public key algorithm = %v, want ECDSA", cert.PublicKeyAlgorithm)
	}
	// Validity window covers now and stays under the 398-day ceiling that
	// Apple/Chrome enforce for TLS server certs (else the cert is unusable even
	// when manually trusted).
	now := time.Now()
	if !cert.NotBefore.Before(now) || !cert.NotAfter.After(now) {
		t.Errorf("validity window doesn't cover now: %s .. %s", cert.NotBefore, cert.NotAfter)
	}
	if d := cert.NotAfter.Sub(cert.NotBefore); d > 398*24*time.Hour {
		t.Errorf("validity too long (Apple/Chrome reject >398d): %s", d)
	}
	// Loopback is always covered, plus the requested DNS name + IP.
	for _, dns := range []string{"localhost", "example.lan"} {
		if !contains(cert.DNSNames, dns) {
			t.Errorf("missing DNS SAN %q (have %v)", dns, cert.DNSNames)
		}
	}
	for _, want := range []string{"127.0.0.1", "::1", "10.0.0.5"} {
		if !hasIP(cert.IPAddresses, want) {
			t.Errorf("missing IP SAN %q (have %v)", want, cert.IPAddresses)
		}
	}
	// The cert and key form a usable TLS keypair.
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		t.Errorf("cert/key do not form a valid keypair: %v", err)
	}
}

func TestGenerateSelfSigned_DefaultsToLoopback(t *testing.T) {
	certPEM, _, err := GenerateSelfSigned(nil)
	if err != nil {
		t.Fatal(err)
	}
	cert := parseCert(t, certPEM)
	if !contains(cert.DNSNames, "localhost") || !hasIP(cert.IPAddresses, "127.0.0.1") {
		t.Errorf("loopback not covered by default: DNS=%v IP=%v", cert.DNSNames, cert.IPAddresses)
	}
}

// Security: the private key must never be group/world-readable, even if a key
// file already existed with looser permissions.
func TestWriteCertPair_KeyNotReadableByOthers(t *testing.T) {
	certPEM, keyPEM, err := GenerateSelfSigned(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "tls")

	// Pre-seed a world-readable key.pem to prove WriteCertPair locks it down.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, keyPath, err := WriteCertPair(dir, certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("key.pem mode = %#o, want 0600 (must not be group/world-readable)", perm)
	}
}

// Security: a pre-planted symlink at key.pem must not redirect the write onto
// another file (O_NOFOLLOW). Without it, an attacker who can plant a symlink in
// the data dir could get an arbitrary file overwritten / chmod'd to 0600.
func TestWriteCertPair_RefusesKeySymlink(t *testing.T) {
	certPEM, keyPEM, err := GenerateSelfSigned(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "tls")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("do-not-touch"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "key.pem")); err != nil {
		t.Fatal(err)
	}

	if _, _, err := WriteCertPair(dir, certPEM, keyPEM); err == nil {
		t.Error("WriteCertPair followed a symlink instead of refusing it")
	}
	// The victim file must be untouched.
	if b, err := os.ReadFile(victim); err != nil || string(b) != "do-not-touch" {
		t.Errorf("symlink target was modified: %q (err %v)", b, err)
	}
}

func hasExtKeyUsage(c *x509.Certificate, want x509.ExtKeyUsage) bool {
	for _, u := range c.ExtKeyUsage {
		if u == want {
			return true
		}
	}
	return false
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func hasIP(ips []net.IP, want string) bool {
	w := net.ParseIP(want)
	for _, ip := range ips {
		if ip.Equal(w) {
			return true
		}
	}
	return false
}
