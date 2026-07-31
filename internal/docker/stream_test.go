package docker

import (
	"testing"

	"github.com/docker/docker/api/types/container"
)

// computeSample turns a raw Docker stats frame into the flat sample the app and
// the browser use. The network part is worth testing because Docker reports it
// per interface in a MAP, and both the summing and the ordering matter.

func TestComputeSampleSumsNetworkInterfaces(t *testing.T) {
	raw := &container.StatsResponse{
		Networks: map[string]container.NetworkStats{
			"eth0": {RxBytes: 1000, TxBytes: 100, RxPackets: 10, TxPackets: 2, RxDropped: 1, TxErrors: 3},
			"eth1": {RxBytes: 500, TxBytes: 50, RxPackets: 5, TxPackets: 1, TxDropped: 2, RxErrors: 4},
		},
	}
	got := computeSample("c1", raw)

	if got.NetRx != 1500 || got.NetTx != 150 {
		t.Errorf("totals: rx=%d tx=%d, want 1500/150", got.NetRx, got.NetTx)
	}
	if got.NetRxPackets != 15 || got.NetTxPackets != 3 {
		t.Errorf("packets: rx=%d tx=%d, want 15/3", got.NetRxPackets, got.NetTxPackets)
	}
	// Drops and errors are usually zero, which is exactly why a bug here would go
	// unnoticed: they are only ever interesting on the day they aren't.
	if got.NetRxDropped != 1 || got.NetTxDropped != 2 {
		t.Errorf("dropped: rx=%d tx=%d, want 1/2", got.NetRxDropped, got.NetTxDropped)
	}
	if got.NetRxErrors != 4 || got.NetTxErrors != 3 {
		t.Errorf("errors: rx=%d tx=%d, want 4/3", got.NetRxErrors, got.NetTxErrors)
	}
	if len(got.Interfaces) != 2 {
		t.Fatalf("expected both interfaces in the breakdown, got %d", len(got.Interfaces))
	}
}

// TestComputeSampleOrdersInterfacesStably: Go map iteration is randomised, so
// without an explicit sort the per-interface table would reorder itself between
// frames — once a second, for as long as the page is open.
func TestComputeSampleOrdersInterfacesStably(t *testing.T) {
	raw := &container.StatsResponse{
		Networks: map[string]container.NetworkStats{
			"eth2": {RxBytes: 3},
			"eth0": {RxBytes: 1},
			"eth1": {RxBytes: 2},
		},
	}
	for i := 0; i < 20; i++ {
		got := computeSample("c1", raw)
		names := make([]string, 0, len(got.Interfaces))
		for _, n := range got.Interfaces {
			names = append(names, n.Name)
		}
		if len(names) != 3 || names[0] != "eth0" || names[1] != "eth1" || names[2] != "eth2" {
			t.Fatalf("interfaces out of order on run %d: %v", i, names)
		}
	}
}

func TestComputeSampleWithNoNetworks(t *testing.T) {
	// A container on `network_mode: none` reports no interfaces at all. That must
	// read as zero traffic, not as a nil-deref or a missing field.
	got := computeSample("c1", &container.StatsResponse{})
	if got.NetRx != 0 || got.NetTx != 0 || len(got.Interfaces) != 0 {
		t.Errorf("expected an empty network picture, got %+v", got)
	}
}

// TestComputeSampleReportsCoreCount guards the figure that makes CPUPercent
// interpretable: without it a reader cannot tell 400% on four cores from 400% on
// forty.
func TestComputeSampleReportsCoreCount(t *testing.T) {
	raw := &container.StatsResponse{}
	raw.CPUStats.OnlineCPUs = 4
	if got := computeSample("c1", raw); got.CPUCores != 4 {
		t.Errorf("CPUCores = %v, want 4", got.CPUCores)
	}

	// Older daemons leave OnlineCPUs at zero and only fill PercpuUsage.
	raw2 := &container.StatsResponse{}
	raw2.CPUStats.CPUUsage.PercpuUsage = []uint64{1, 2}
	if got := computeSample("c1", raw2); got.CPUCores != 2 {
		t.Errorf("CPUCores from PercpuUsage = %v, want 2", got.CPUCores)
	}
}
