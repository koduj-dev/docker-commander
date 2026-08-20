// Package acme wraps golang.org/x/crypto/acme/autocert into the shape this
// app needs: automatic HTTPS via ACME (Let's Encrypt by default), for the
// case where the server sits directly on the public internet with no reverse
// proxy in front — --make-certs (a self-signed cert) is the local/dev
// equivalent, and a reverse proxy terminating TLS itself needs neither.
package acme

import (
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// NewManager builds an autocert.Manager for domains, caching issued
// certificates and ACME account state under cacheDir so a restart doesn't
// re-request a certificate and spend into the CA's rate limits.
//
// The manager answers the "tls-alpn-01" challenge directly during the TLS
// handshake (autocert also supports "http-01", which needs a separate port-80
// listener — deliberately not used here, since this app's HTTPS port is
// whatever the operator configured, and requiring port 80 too would be a
// second thing to open). HostPolicy is a strict whitelist: an unlisted SNI
// name is refused a certificate rather than silently attempted, the same
// protection autocert's own docs call out — accepting any SNI name invites a
// client to make this server burn its rate-limit budget requesting
// certificates for names it was never configured for.
//
// email is optional (attached to the ACME account so the CA can reach the
// operator about renewal problems). directoryURL overrides the CA (empty =
// Let's Encrypt production); pass its staging directory to exercise the flow
// without spending production rate-limit budget. NOT a local Pebble
// instance — through THIS Manager's real GetCertificate path (as opposed to
// the lower-level acme.Client calls pebble_integration_test.go makes
// instead) Pebble doesn't work at all: its directory endpoint's TLS cert is
// locally-generated and untrusted (so the resulting *acme.Client, with no
// custom trust root configured, correctly refuses to talk to it — same as
// it would refuse any other unverifiable server), and separately, its
// finalize-order response is missing a header autocert's polling relies on.
// See docs/gotchas.md for both.
func NewManager(domains []string, email, cacheDir, directoryURL string) *autocert.Manager {
	mgr := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(cacheDir),
		HostPolicy: autocert.HostWhitelist(domains...),
		Email:      email,
	}
	if directoryURL != "" {
		mgr.Client = &acme.Client{DirectoryURL: directoryURL}
	}
	return mgr
}
