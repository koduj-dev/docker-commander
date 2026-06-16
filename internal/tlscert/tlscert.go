// Package tlscert generates a self-signed TLS certificate + key so the server
// can serve HTTPS without external tooling (the `dockercmd --make-certs`
// action). For public hosts, use a real CA / ACME instead.
package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Validity is how long a generated self-signed certificate is valid for. It is
// kept under the 398-day ceiling that Apple/Chrome enforce for TLS server certs
// (they reject longer-lived leaves even when the cert is manually trusted).
const Validity = 397 * 24 * time.Hour

// loopback hosts are always covered so a local HTTPS run validates out of the box.
var loopback = []string{"localhost", "127.0.0.1", "::1"}

// GenerateSelfSigned returns PEM-encoded certificate and private key for an
// ECDSA P-256 self-signed certificate covering the given hosts (DNS names and/or
// IP addresses), plus localhost / 127.0.0.1 / ::1.
func GenerateSelfSigned(hosts []string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial: %w", err)
	}

	names := dedupe(append(append([]string{}, loopback...), hosts...))
	// CommonName is the first operator-supplied host (falling back to loopback).
	cn := names[0]
	if len(hosts) > 0 {
		if h := strings.TrimSpace(hosts[0]); h != "" {
			cn = h
		}
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"Docker Commander (self-signed)"}},
		NotBefore:             time.Now().Add(-1 * time.Hour), // tolerate clock skew
		NotAfter:              time.Now().Add(Validity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}
	for _, h := range names {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// WriteCertPair writes the cert (0644) and key (0600) into dir (created 0700 if
// needed) and returns their paths. The private key is never group/world-readable
// even if the file already existed.
func WriteCertPair(dir string, certPEM, keyPEM []byte) (certPath, keyPath string, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	// MkdirAll is a no-op on an existing dir, so tighten one that was pre-created
	// with looser perms (best-effort; ignore if we don't own it).
	_ = os.Chmod(dir, 0o700)
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return "", "", err
	}
	// Open the key with O_NOFOLLOW so a pre-planted symlink can't redirect the
	// write (or a later chmod) onto another file. O_TRUNC|0600 also guarantees
	// the key is never group/world-readable, even if a looser file existed.
	f, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", "", err
	}
	if _, err := f.Write(keyPEM); err != nil {
		f.Close()
		return "", "", err
	}
	if err := f.Close(); err != nil {
		return "", "", err
	}
	// Force 0600 in case key.pem already existed (O_CREATE keeps the old mode).
	if err := os.Chmod(keyPath, 0o600); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
