package api

import (
	"testing"

	"github.com/koduj-dev/docker-commander/internal/history"
)

// The metric name reaches the storage key in both history backends, so it is
// whitelisted at the handler. This test exists because the whitelist is easy to
// forget when a metric is added: the symptom is not an error but an empty graph,
// which reads as "no traffic" rather than "unsupported metric".
func TestHistoryMetricWhitelistCoversEveryStoredMetric(t *testing.T) {
	// The handler's OWN set, not a copy of it: an earlier version of this test
	// declared its own map and compared that against the engine, so it passed
	// happily while the handler rejected the metrics it listed. A test that
	// restates the thing under test is not testing it.
	accepted := historyMetrics

	// history.AllMetrics is what the engine actually records. Anything recorded
	// but not accepted here is a series the UI could never fetch.
	for _, m := range history.AllMetrics() {
		if !accepted[m] {
			t.Errorf("metric %q is recorded by the engine but not accepted by the history endpoint — its graph would always be empty", m)
		}
	}
	for m := range accepted {
		found := false
		for _, r := range history.AllMetrics() {
			if r == m {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("metric %q is accepted by the endpoint but never recorded — it can only ever return nothing", m)
		}
	}
}
