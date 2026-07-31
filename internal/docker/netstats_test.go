package docker

import "testing"

// Attribution is the whole point of this file, so it is what gets tested.
//
// Docker gives no way to split a multi-network container's counters per network
// (the stats API's endpoint_id is Windows-only). Guessing would produce a number
// that looks authoritative and is wrong, so the rule is: attribute a container
// only when it is attached to exactly one network, and count the rest as
// unattributed rather than folding them in.

func TestAttachedTo(t *testing.T) {
	if !attachedTo([]string{"frontend", "backend"}, "backend") {
		t.Error("should match an attached network")
	}
	if attachedTo([]string{"frontend"}, "front") {
		t.Error("must not match on a prefix")
	}
	if attachedTo(nil, "frontend") {
		t.Error("a container on no networks matches nothing")
	}
}

func TestNetworkEndpointAttribution(t *testing.T) {
	// Two containers on this network: one attached only here, one also elsewhere.
	containers := []ContainerSummary{
		{ID: "solo", Name: "web", Networks: []string{"appnet"}},
		{ID: "multi", Name: "proxy", Networks: []string{"appnet", "edge"}},
		{ID: "other", Name: "db", Networks: []string{"edge"}},
	}
	stats := map[string][6]uint64{
		"solo":  {1000, 200, 1, 0, 0, 2},
		"multi": {9999, 8888, 5, 5, 5, 5},
	}
	lookup := func(id string) (uint64, uint64, uint64, uint64, uint64, uint64, bool) {
		v, ok := stats[id]
		return v[0], v[1], v[2], v[3], v[4], v[5], ok
	}

	out := buildNetworkStats(containers, "appnet", lookup)

	if out.Endpoints != 2 {
		t.Fatalf("endpoints = %d, want 2", out.Endpoints)
	}
	if out.Unattributed != 1 {
		t.Errorf("unattributed = %d, want 1 (the multi-homed proxy)", out.Unattributed)
	}
	// The multi-homed container's 9999 bytes cover BOTH its networks, so folding
	// them in here would overstate this network by an unknown amount.
	if out.RxBytes != 1000 || out.TxBytes != 200 {
		t.Errorf("totals rx=%d tx=%d, want only the single-attachment container (1000/200)", out.RxBytes, out.TxBytes)
	}
	if out.RxDropped != 1 || out.TxErrors != 2 {
		t.Errorf("drops/errors should follow the same rule: %+v", out)
	}

	// Both containers are still listed — the operator should see the one that
	// could not be attributed, not have it silently omitted.
	if len(out.Containers) != 2 {
		t.Fatalf("expected both endpoints listed, got %d", len(out.Containers))
	}
	byName := map[string]NetworkEndpointStats{}
	for _, c := range out.Containers {
		byName[c.ContainerName] = c
	}
	if !byName["web"].Attributable {
		t.Error("a single-attachment container must be attributable")
	}
	if byName["proxy"].Attributable {
		t.Error("a multi-attachment container must NOT be attributable")
	}
	// Its own counters are still shown, just not summed.
	if byName["proxy"].RxBytes != 9999 {
		t.Errorf("the unattributable container's own counters should still be reported: %+v", byName["proxy"])
	}
}

func TestNetworkEndpointSortedAndEmpty(t *testing.T) {
	containers := []ContainerSummary{
		{ID: "b", Name: "zeta", Networks: []string{"n"}},
		{ID: "a", Name: "alpha", Networks: []string{"n"}},
	}
	out := buildNetworkStats(containers, "n", nil)
	if out.Containers[0].ContainerName != "alpha" {
		t.Errorf("endpoints should be sorted by name, got %s first", out.Containers[0].ContainerName)
	}

	// A network with nothing attached is an empty result, not a nil slice: the
	// SPA maps over it.
	empty := buildNetworkStats(containers, "unused", nil)
	if empty.Containers == nil {
		t.Error("Containers must marshal as [] rather than null")
	}
	if empty.Endpoints != 0 {
		t.Errorf("endpoints = %d, want 0", empty.Endpoints)
	}
}
