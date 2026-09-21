package docker

import (
	"net/netip"
	"testing"
)

// addrString is what keeps the SDK's zero netip.Addr from becoming the literal
// "invalid IP" in our API output — and, more importantly, in the reverse
// proxy's bindDialAddr, which treats "" (not that text) as the unset/wildcard
// spelling.
func TestAddrString(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   netip.Addr
		want string
	}{
		{"unset (the zero value)", netip.Addr{}, ""},
		{"IPv4", netip.MustParseAddr("192.0.2.10"), "192.0.2.10"},
		{"IPv4 wildcard", netip.MustParseAddr("0.0.0.0"), "0.0.0.0"},
		{"IPv6", netip.MustParseAddr("2001:db8::1"), "2001:db8::1"},
		{"IPv6 wildcard", netip.MustParseAddr("::"), "::"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := addrString(tc.in); got != tc.want {
				t.Errorf("addrString(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
