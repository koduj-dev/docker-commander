package docker

import (
	"context"
	"errors"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

func newTestManager(t *testing.T) (*Manager, context.Context) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return NewManager(st), context.Background()
}

func TestLocalHostProbe_RealInterfaces(t *testing.T) {
	got := localHostProbe()
	if got.Method != "local" {
		t.Errorf("Method = %q, want \"local\"", got.Method)
	}
	if len(got.Ifaces) == 0 {
		t.Fatal("expected at least one interface with an address on this machine")
	}
	for _, hi := range got.Ifaces {
		if len(hi.Subnets) == 0 {
			t.Errorf("interface %q reported with no subnets", hi.Name)
		}
	}
}

func TestAnyDefault(t *testing.T) {
	if anyDefault(nil) {
		t.Error("anyDefault(nil) = true, want false")
	}
	if anyDefault([]hostIface{{Name: "eth0"}, {Name: "eth1"}}) {
		t.Error("anyDefault with no IsDefault set = true, want false")
	}
	if !anyDefault([]hostIface{{Name: "eth0"}, {Name: "eth1", IsDefault: true}}) {
		t.Error("anyDefault with one IsDefault set = false, want true")
	}
}

func TestDockerOwnedIface(t *testing.T) {
	owned := []string{"lo", "docker0", "docker1", "br-abc123", "veth1234abc"}
	for _, n := range owned {
		if !dockerOwnedIface(n) {
			t.Errorf("dockerOwnedIface(%q) = false, want true", n)
		}
	}
	notOwned := []string{"eth0", "en0", "wlan0", "Ethernet"}
	for _, n := range notOwned {
		if dockerOwnedIface(n) {
			t.Errorf("dockerOwnedIface(%q) = true, want false", n)
		}
	}
}

func TestCheckNetworkHostOverlap(t *testing.T) {
	t.Run("probe error is reported as skipped, not a false ok/fail", func(t *testing.T) {
		got := checkNetworkHostOverlap(
			[]NetworkSummary{{Name: "bridge", Subnets: []string{"172.17.0.0/16"}}},
			hostProbeResult{Attempted: []string{"ip -j addr", "ifconfig"}, Err: errors.New("ssh: connection refused")},
		)
		if got.Status != CheckSkipped {
			t.Errorf("status = %s, want skipped", got.Status)
		}
	})

	t.Run("overlap between a docker network and a real host interface fails", func(t *testing.T) {
		got := checkNetworkHostOverlap(
			[]NetworkSummary{{Name: "bridge", Subnets: []string{"172.17.0.0/16"}}},
			hostProbeResult{Method: "local", Ifaces: []hostIface{
				{Name: "eth0", Subnets: []string{"172.17.5.0/24"}}, // a corporate LAN "shaped" like docker0
			}},
		)
		if got.Status != CheckFail {
			t.Errorf("status = %s, want fail", got.Status)
		}
	})

	t.Run("no overlap is ok", func(t *testing.T) {
		got := checkNetworkHostOverlap(
			[]NetworkSummary{{Name: "bridge", Subnets: []string{"172.17.0.0/16"}}},
			hostProbeResult{Method: "local", Ifaces: []hostIface{
				{Name: "eth0", Subnets: []string{"192.168.1.0/24"}},
			}},
		)
		if got.Status != CheckOK {
			t.Errorf("status = %s, want ok", got.Status)
		}
	})

	t.Run("the docker bridge itself is excluded from the comparison", func(t *testing.T) {
		// docker0's own address is INSIDE the network it's the gateway for —
		// that must never self-flag as an overlap.
		got := checkNetworkHostOverlap(
			[]NetworkSummary{{Name: "bridge", Subnets: []string{"172.17.0.0/16"}}},
			hostProbeResult{Method: "local", Ifaces: []hostIface{
				{Name: "docker0", Subnets: []string{"172.17.0.1/16"}},
			}},
		)
		if got.Status != CheckOK {
			t.Errorf("status = %s, want ok (docker0 should be excluded), details: %v", got.Status, got.Details)
		}
	})
}

func TestHostProbeFor(t *testing.T) {
	t.Run("a tcp-kind host has no shell path and is reported, not silently ok", func(t *testing.T) {
		m, ctx := newTestManager(t)
		id, err := m.store.CreateHost(ctx, &store.Host{Name: "remote-tcp", Kind: "tcp", Address: "tcp://10.0.0.5:2376"})
		if err != nil {
			t.Fatal(err)
		}
		got := m.hostProbeFor(ctx, id)
		if got.Method != "" {
			t.Errorf("Method = %q, want empty (no shell path on a tcp host)", got.Method)
		}
		if len(got.Attempted) == 0 {
			t.Error("Attempted should explain why nothing ran")
		}
	})

	t.Run("hostID 0 resolves through the same default-host logic as everywhere else, not a hardcoded local", func(t *testing.T) {
		m, ctx := newTestManager(t)
		// The only configured host is remote — hostID 0 ("unspecified") must
		// resolve to IT, not be silently treated as the local daemon.
		if _, err := m.store.CreateHost(ctx, &store.Host{Name: "only-host", Kind: "tcp", Address: "tcp://10.0.0.5:2376"}); err != nil {
			t.Fatal(err)
		}
		got := m.hostProbeFor(ctx, 0)
		if got.Method != "" {
			t.Errorf("Method = %q, want empty — hostID 0 should have resolved to the tcp host, not local", got.Method)
		}
	})
}

func TestCheckMTUMismatch_SkipPaths(t *testing.T) {
	ctx := context.Background()
	var m *Manager // the skip branches must return before touching the receiver

	t.Run("probe error", func(t *testing.T) {
		got := m.checkMTUMismatch(ctx, 0, nil, hostProbeResult{Err: errors.New("boom")})
		if got.Status != CheckSkipped {
			t.Errorf("status = %s, want skipped", got.Status)
		}
	})

	t.Run("no default interface identified", func(t *testing.T) {
		got := m.checkMTUMismatch(ctx, 0, nil, hostProbeResult{
			Method: "local",
			Ifaces: []hostIface{{Name: "eth0", MTU: 1500, IsDefault: false}},
		})
		if got.Status != CheckSkipped {
			t.Errorf("status = %s, want skipped", got.Status)
		}
	})
}
