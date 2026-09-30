package docker

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeStatsDaemon answers /containers/{id}/stats like a real daemon: with
// one-shot=true it returns a single frame whose precpu_stats is empty; otherwise
// it takes "two samples" and fills precpu_stats in. The container used 4 cores
// between them (4e9 ns of CPU over 1e9 ns... on a 16-CPU host: sysDelta 16e9).
func fakeStatsDaemon(t *testing.T, sawOneShot *bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.43")
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/stats") {
			w.WriteHeader(http.StatusOK)
			return
		}
		frame := map[string]any{
			"read": "2026-09-25T20:00:01Z",
			"cpu_stats": map[string]any{
				"cpu_usage":        map[string]any{"total_usage": 1_004_000_000_000},
				"system_cpu_usage": 900_016_000_000_000,
				"online_cpus":      16,
			},
			"memory_stats": map[string]any{"usage": 1 << 20, "limit": 1 << 30},
		}
		if r.URL.Query().Get("one-shot") == "true" {
			*sawOneShot = true
			frame["precpu_stats"] = map[string]any{"cpu_usage": map[string]any{}}
		} else {
			frame["precpu_stats"] = map[string]any{
				"cpu_usage":        map[string]any{"total_usage": 1_000_000_000_000},
				"system_cpu_usage": 900_000_000_000_000,
			}
		}
		_ = json.NewEncoder(w).Encode(frame)
	}))
}

// One-shot stats carry no previous sample, so the CPU delta was taken against
// zero and a container using four cores showed as ~0.1%. SampleStats must ask
// the daemon for the previous sample.
func TestSampleStatsAsksForThePreviousSampleSoCPUIsReal(t *testing.T) {
	var sawOneShot bool
	srv := fakeStatsDaemon(t, &sawOneShot)
	defer srv.Close()
	m, id := managerFixture(t, "tcp://"+strings.TrimPrefix(srv.URL, "http://"))

	s, err := m.SampleStats(t.Context(), id, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if sawOneShot {
		t.Error("SampleStats sent one-shot=true: the daemon leaves precpu_stats empty and CPU comes out as a lifetime average")
	}
	// (1_004e9 - 1_000e9) / (900_016e9 - 900_000e9) * 16 * 100 = 400
	if math.Abs(s.CPUPercent-400) > 1 {
		t.Errorf("CPUPercent = %.2f, want 400 (four of 16 cores)", s.CPUPercent)
	}
}
