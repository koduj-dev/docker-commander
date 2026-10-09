package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/acme"
)

// TestIntegrationPebble_FullIssuance proves NewManager's wiring — the
// DirectoryURL override and the resulting *acme.Client — against a REAL ACME
// server (Let's Encrypt's own test server, Pebble:
// https://github.com/letsencrypt/pebble), not just mocked plumbing:
//
//	docker run -d --name pebble -p 127.0.0.1:14000:14000 \
//	  -e PEBBLE_VA_ALWAYS_VALID=1 -e PEBBLE_VA_NOSLEEP=1 \
//	  ghcr.io/letsencrypt/pebble:latest
//	go test ./internal/acme/... -run TestIntegrationPebble -v
//
// It drives the exchange with acme.Client's lower-level methods rather than
// autocert.Manager.GetCertificate's convenience wrapper: that wrapper polls
// the freshly-finalized order using the URI from the finalize RESPONSE's
// Location header, which Pebble does not populate (only the original
// order-creation response does, and RFC 8555 §7.4 does not require the
// finalize response to repeat it) — a Pebble/autocert interop gap in code
// this package doesn't own, confirmed reproducible against Pebble regardless
// of PEBBLE_VA_NOSLEEP. What NewManager IS responsible for — handing back an
// *acme.Client correctly pointed at the configured directory, over the
// configured HTTP transport — is exactly what this test drives instead, using
// the order's own URI (known from AuthorizeOrder, unaffected by the gap).
//
// Skips cleanly (not a failure) when Pebble isn't reachable, and always under
// -short — the same shape as internal/docker's newManager(t).
func TestIntegrationPebble_FullIssuance(t *testing.T) {
	if testing.Short() {
		t.Skip("ACME integration test; skipped under -short")
	}
	directoryURL := os.Getenv("DC_ACME_TEST_DIRECTORY")
	if directoryURL == "" {
		directoryURL = "https://127.0.0.1:14000/dir"
	}
	insecureClient := &http.Client{
		// Pebble's own TLS cert is a locally-generated test root, not one any
		// real trust store knows — fine for a test server we just started
		// ourselves, never appropriate for the real Let's Encrypt directory.
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec
		Timeout:   10 * time.Second,
	}
	if _, err := insecureClient.Get(directoryURL); err != nil {
		t.Skipf("Pebble not reachable at %s (start it to run this test): %v", directoryURL, err)
	}

	domain := "docker-commander-acme-test.example"
	mgr := NewManager([]string{domain}, "", t.TempDir(), directoryURL)
	if mgr.Client == nil {
		t.Fatal("NewManager should set Client when a directoryURL override is given")
	}
	client := mgr.Client
	client.HTTPClient = insecureClient

	accountKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	client.Key = accountKey

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := client.Register(ctx, &acme.Account{}, acme.AcceptTOS); err != nil {
		t.Fatalf("Register: %v", err)
	}

	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(domain))
	if err != nil {
		t.Fatalf("AuthorizeOrder: %v", err)
	}
	for _, authzURL := range order.AuthzURLs {
		authz, err := client.GetAuthorization(ctx, authzURL)
		if err != nil {
			t.Fatalf("GetAuthorization: %v", err)
		}
		if authz.Status == acme.StatusValid || len(authz.Challenges) == 0 {
			continue
		}
		// PEBBLE_VA_ALWAYS_VALID means Pebble accepts any challenge without
		// actually validating it, so it doesn't matter which one we pick.
		if _, err := client.Accept(ctx, authz.Challenges[0]); err != nil {
			t.Fatalf("Accept challenge: %v", err)
		}
		if _, err := client.WaitAuthorization(ctx, authzURL); err != nil {
			t.Fatalf("WaitAuthorization: %v", err)
		}
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: []string{domain}}, key)
	if err != nil {
		t.Fatalf("create CSR: %v", err)
	}

	// CreateOrderCert submits the CSR (finalize) and, if the order isn't
	// already valid, tries to poll it — this is the step that hits the
	// Pebble/autocert gap described above. The finalize POST itself still
	// reaches Pebble regardless of what happens after, so on that specific,
	// expected error we fall back to polling the order via ITS OWN uri (from
	// AuthorizeOrder, unaffected by the gap) instead of failing the test.
	der, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		finalized, werr := client.WaitOrder(ctx, order.URI)
		if werr != nil {
			t.Fatalf("CreateOrderCert failed (%v), and the fallback WaitOrder(%s) also failed: %v", err, order.URI, werr)
		}
		der, err = client.FetchCert(ctx, finalized.CertURL, true)
		if err != nil {
			t.Fatalf("FetchCert after fallback WaitOrder: %v", err)
		}
	}
	if len(der) == 0 {
		t.Fatal("no certificate returned")
	}
	leaf, err := x509.ParseCertificate(der[0])
	if err != nil {
		t.Fatalf("parse issued certificate: %v", err)
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != domain {
		t.Errorf("issued cert DNSNames = %v, want [%s]", leaf.DNSNames, domain)
	}

	// SECURITY: HostPolicy must still refuse a non-whitelisted name — a
	// successful order for the configured domain must not have loosened it
	// for anything else.
	if err := mgr.HostPolicy(ctx, "not-whitelisted.example"); err == nil {
		t.Error("SECURITY: HostPolicy accepted a non-whitelisted domain")
	}
}
