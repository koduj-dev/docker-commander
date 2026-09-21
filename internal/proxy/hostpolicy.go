package proxy

import (
	"context"
	"fmt"
)

// HostPolicy is the dynamic replacement for autocert.HostWhitelist: it
// allows certificate issuance for host only when a domain_mappings row
// exists for it AND that mapping's project is eligible for this phase
// (exists, and is local — see eligibleMapping in proxy.go). This is the
// actual security boundary for what this instance will request/serve a
// certificate for — it does not lean on the app-level project-delete
// cascade (domain_mappings rows are deleted when their project is) as the
// only thing standing between "mapping row still exists" and "safe to
// issue a cert for," in case that invariant is ever violated by a bug
// elsewhere or a row edited directly.
//
// The returned error is intentionally uniform: it never tells a caller
// (autocert's own TLS-handshake-time caller, ultimately a network client)
// WHY a host was refused — "unmapped", "mapped to a remote project", and
// "mapped to a since-deleted project" all look identical from outside,
// so probing SNI values can't be used to enumerate which domains exist.
func (p *Proxy) HostPolicy(ctx context.Context, host string) error {
	if _, _, ok := p.eligibleMapping(ctx, host); !ok {
		return fmt.Errorf("proxy: %q is not a locally-routable domain", host)
	}
	return nil
}
