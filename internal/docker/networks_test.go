package docker

import (
	"strings"
	"testing"
)

// ipamFor is the one place a user-typed subnet/gateway becomes a typed SDK
// value, so it is where a malformed one must be refused — with a message that
// names the field, not a bare parse error.
func TestIpamFor(t *testing.T) {
	t.Run("neither → nil, the daemon picks both", func(t *testing.T) {
		got, err := ipamFor("", "")
		if err != nil || got != nil {
			t.Fatalf("got %+v, %v; want nil, nil", got, err)
		}
	})
	t.Run("valid subnet and gateway", func(t *testing.T) {
		got, err := ipamFor("172.30.0.0/16", "172.30.0.1")
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Config) != 1 || got.Config[0].Subnet.String() != "172.30.0.0/16" || got.Config[0].Gateway.String() != "172.30.0.1" {
			t.Errorf("unexpected IPAM: %+v", got)
		}
	})
	t.Run("IPv6 subnet", func(t *testing.T) {
		got, err := ipamFor("fd00:dead:beef::/48", "")
		if err != nil {
			t.Fatal(err)
		}
		if got.Config[0].Subnet.String() != "fd00:dead:beef::/48" || got.Config[0].Gateway.IsValid() {
			t.Errorf("unexpected IPAM: %+v", got)
		}
	})
	t.Run("gateway only leaves the subnet unset", func(t *testing.T) {
		got, err := ipamFor("", "172.30.0.1")
		if err != nil {
			t.Fatal(err)
		}
		if got.Config[0].Subnet.IsValid() || got.Config[0].Gateway.String() != "172.30.0.1" {
			t.Errorf("unexpected IPAM: %+v", got)
		}
	})

	for _, tc := range []struct {
		name, subnet, gateway, wantField string
	}{
		{"subnet without a prefix length", "172.30.0.0", "", `subnet "172.30.0.0"`},
		{"subnet that is not an address", "not-a-cidr", "", `subnet "not-a-cidr"`},
		{"subnet with a bad octet", "172.300.0.0/16", "", "subnet"},
		{"gateway that is a CIDR", "172.30.0.0/16", "172.30.0.1/16", `gateway "172.30.0.1/16"`},
		{"gateway that is a hostname", "", "gw.example.com", `gateway "gw.example.com"`},
		{"gateway only, malformed", "", "999.1.1.1", "gateway"},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			got, err := ipamFor(tc.subnet, tc.gateway)
			if err == nil {
				t.Fatalf("expected an error, got %+v", got)
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Errorf("error %q should name %s", err, tc.wantField)
			}
		})
	}
}
