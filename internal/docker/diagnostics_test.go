package docker

import (
	"testing"

	"github.com/docker/docker/api/types/container"
)

func TestCheckNetworkOverlap(t *testing.T) {
	cases := []struct {
		name     string
		nets     []NetworkSummary
		wantFail bool
	}{
		{
			name: "no overlap",
			nets: []NetworkSummary{
				{Name: "a", Subnets: []string{"10.0.0.0/24"}},
				{Name: "b", Subnets: []string{"10.0.1.0/24"}},
			},
			wantFail: false,
		},
		{
			name: "adjacent but not overlapping",
			nets: []NetworkSummary{
				{Name: "a", Subnets: []string{"10.0.0.0/25"}},
				{Name: "b", Subnets: []string{"10.0.0.128/25"}},
			},
			wantFail: false,
		},
		{
			name: "exact duplicate",
			nets: []NetworkSummary{
				{Name: "a", Subnets: []string{"172.18.0.0/16"}},
				{Name: "b", Subnets: []string{"172.18.0.0/16"}},
			},
			wantFail: true,
		},
		{
			name: "nested (host bridge collides with a corporate LAN)",
			nets: []NetworkSummary{
				{Name: "bridge", Subnets: []string{"172.17.0.0/16"}},
				{Name: "vpn-shaped", Subnets: []string{"172.17.5.0/24"}},
			},
			wantFail: true,
		},
		{
			name: "unparseable subnet is ignored, not fatal",
			nets: []NetworkSummary{
				{Name: "a", Subnets: []string{"not-a-cidr"}},
				{Name: "b", Subnets: []string{"10.0.0.0/24"}},
			},
			wantFail: false,
		},
		{
			name: "same network's own subnets never self-flag",
			nets: []NetworkSummary{
				{Name: "dual-stack", Subnets: []string{"10.0.0.0/24", "10.0.0.0/24"}},
			},
			wantFail: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkNetworkOverlap(tc.nets)
			isFail := got.Status == CheckFail
			if isFail != tc.wantFail {
				t.Errorf("status = %s, want fail=%v (details: %v)", got.Status, tc.wantFail, got.Details)
			}
		})
	}
}

func TestCheckDuplicatePortBindings(t *testing.T) {
	t.Run("no conflict", func(t *testing.T) {
		got := checkDuplicatePortBindings([]ContainerSummary{
			{Name: "a", State: "running", Ports: []PortMapping{{IP: "0.0.0.0", PublicPort: 80, Type: "tcp"}}},
			{Name: "b", State: "running", Ports: []PortMapping{{IP: "0.0.0.0", PublicPort: 81, Type: "tcp"}}},
		})
		if got.Status != CheckOK {
			t.Errorf("status = %s, want ok", got.Status)
		}
	})

	t.Run("stopped containers are ignored", func(t *testing.T) {
		got := checkDuplicatePortBindings([]ContainerSummary{
			{Name: "a", State: "running", Ports: []PortMapping{{IP: "0.0.0.0", PublicPort: 80, Type: "tcp"}}},
			{Name: "b", State: "exited", Ports: []PortMapping{{IP: "0.0.0.0", PublicPort: 80, Type: "tcp"}}},
		})
		if got.Status != CheckOK {
			t.Errorf("status = %s, want ok", got.Status)
		}
	})

	t.Run("same host IP+port is a real conflict", func(t *testing.T) {
		got := checkDuplicatePortBindings([]ContainerSummary{
			{Name: "a", State: "running", Ports: []PortMapping{{IP: "0.0.0.0", PublicPort: 80, Type: "tcp"}}},
			{Name: "b", State: "running", Ports: []PortMapping{{IP: "0.0.0.0", PublicPort: 80, Type: "tcp"}}},
		})
		if got.Status != CheckFail {
			t.Errorf("status = %s, want fail", got.Status)
		}
		if len(got.Details) != 1 {
			t.Errorf("details = %v, want exactly 1 conflict line", got.Details)
		}
	})

	t.Run("same port different host IP is a shadow warning, not a fail", func(t *testing.T) {
		got := checkDuplicatePortBindings([]ContainerSummary{
			{Name: "a", State: "running", Ports: []PortMapping{{IP: "0.0.0.0", PublicPort: 80, Type: "tcp"}}},
			{Name: "b", State: "running", Ports: []PortMapping{{IP: "127.0.0.1", PublicPort: 80, Type: "tcp"}}},
		})
		if got.Status != CheckWarn {
			t.Errorf("status = %s, want warn", got.Status)
		}
	})

	t.Run("different protocol on the same port number is not a conflict", func(t *testing.T) {
		got := checkDuplicatePortBindings([]ContainerSummary{
			{Name: "a", State: "running", Ports: []PortMapping{{IP: "0.0.0.0", PublicPort: 53, Type: "tcp"}}},
			{Name: "b", State: "running", Ports: []PortMapping{{IP: "0.0.0.0", PublicPort: 53, Type: "udp"}}},
		})
		if got.Status != CheckOK {
			t.Errorf("status = %s, want ok", got.Status)
		}
	})
}

func TestCheckDanglingResources(t *testing.T) {
	t.Run("predefined networks are never flagged", func(t *testing.T) {
		got := checkDanglingResources([]NetworkSummary{
			{Name: "bridge", Containers: nil},
			{Name: "host", Containers: nil},
			{Name: "none", Containers: nil},
		}, nil)
		if got.Status != CheckOK {
			t.Errorf("status = %s, want ok; details: %v", got.Status, got.Details)
		}
	})

	t.Run("user network with no attached containers is flagged", func(t *testing.T) {
		got := checkDanglingResources([]NetworkSummary{
			{Name: "leftover", Containers: nil},
		}, nil)
		if got.Status != CheckWarn {
			t.Errorf("status = %s, want warn", got.Status)
		}
	})

	t.Run("volume with no InUseBy is flagged, an attached one is not", func(t *testing.T) {
		got := checkDanglingResources(nil, []VolumeSummary{
			{Name: "orphan", InUseBy: nil},
			{Name: "attached", InUseBy: []string{"app"}},
		})
		if got.Status != CheckWarn {
			t.Errorf("status = %s, want warn", got.Status)
		}
		if len(got.Details) != 1 || got.Details[0] != "volume: orphan" {
			t.Errorf("details = %v, want exactly [volume: orphan]", got.Details)
		}
	})
}

func TestEvaluateLogDrivers(t *testing.T) {
	t.Run("json-file with max-size is fine", func(t *testing.T) {
		got := evaluateLogDrivers(map[string]container.LogConfig{
			"app": {Type: "json-file", Config: map[string]string{"max-size": "10m"}},
		}, "json-file")
		if got.Status != CheckOK {
			t.Errorf("status = %s, want ok", got.Status)
		}
	})

	// This is the daemon-default case: a container created with NO explicit
	// --log-opt, under a daemon whose daemon.json sets a global max-size.
	// Docker resolves that default at creation time and persists it into the
	// container's own HostConfig.LogConfig.Config — verified against a real
	// daemon (see the comment on evaluateLogDrivers) — so from this function's
	// point of view it's indistinguishable from an explicit per-container
	// override, and must not be flagged.
	t.Run("json-file relying on a daemon-level max-size default is fine", func(t *testing.T) {
		got := evaluateLogDrivers(map[string]container.LogConfig{
			"app": {Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "3"}},
		}, "json-file")
		if got.Status != CheckOK {
			t.Errorf("status = %s, want ok — a daemon-level default resolved into the container's own Config must count", got.Status)
		}
	})

	t.Run("json-file with no max-size is flagged", func(t *testing.T) {
		got := evaluateLogDrivers(map[string]container.LogConfig{
			"app": {Type: "json-file"},
		}, "json-file")
		if got.Status != CheckWarn {
			t.Errorf("status = %s, want warn", got.Status)
		}
	})

	t.Run("unset driver falls back to the daemon default", func(t *testing.T) {
		got := evaluateLogDrivers(map[string]container.LogConfig{
			"app": {}, // no per-container override
		}, "json-file")
		if got.Status != CheckWarn {
			t.Errorf("status = %s, want warn (daemon default is json-file, unbounded)", got.Status)
		}
	})

	t.Run("non-json-file driver is not our business", func(t *testing.T) {
		got := evaluateLogDrivers(map[string]container.LogConfig{
			"app": {Type: "journald"},
		}, "json-file")
		if got.Status != CheckOK {
			t.Errorf("status = %s, want ok (journald manages its own rotation)", got.Status)
		}
	})
}

func TestRunDiagnostics_Live(t *testing.T) {
	m, ctx := newManager(t)
	report, err := m.RunDiagnostics(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{
		"network_overlap", "network_host_overlap", "mtu_mismatch",
		"duplicate_port_bindings", "log_driver_rotation", "disk_space", "dangling_resources",
	}
	if len(report.Checks) != len(wantIDs) {
		t.Fatalf("got %d checks, want %d: %+v", len(report.Checks), len(wantIDs), report.Checks)
	}
	for i, id := range wantIDs {
		if report.Checks[i].ID != id {
			t.Errorf("checks[%d].ID = %q, want %q", i, report.Checks[i].ID, id)
		}
		if report.Checks[i].Status == "" {
			t.Errorf("checks[%d] (%s) has no status", i, id)
		}
	}
}
