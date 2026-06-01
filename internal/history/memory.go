package history

import (
	"context"
	"sort"
	"sync"
	"time"
)

// memoryStore keeps per-container, per-metric point slices in memory, trimmed
// to the retention window. Suitable for a single local instance.
type memoryStore struct {
	mu        sync.RWMutex
	series    map[string]map[string][]Point // containerID -> metric -> points
	retention time.Duration
}

func newMemoryStore(retention time.Duration) *memoryStore {
	return &memoryStore{series: make(map[string]map[string][]Point), retention: retention}
}

func (m *memoryStore) Record(_ context.Context, samples []Sample) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(-m.retention).UnixMilli()
	for _, s := range samples {
		byMetric := m.series[s.ContainerID]
		if byMetric == nil {
			byMetric = make(map[string][]Point)
			m.series[s.ContainerID] = byMetric
		}
		t := s.Time.UnixMilli()
		for _, metric := range allMetrics {
			v, _ := metricValue(s, metric)
			pts := append(byMetric[metric], Point{T: t, V: v})
			// Trim points older than the retention window from the front.
			i := 0
			for i < len(pts) && pts[i].T < cutoff {
				i++
			}
			byMetric[metric] = pts[i:]
		}
	}
	return nil
}

func (m *memoryStore) Query(_ context.Context, containerID, metric string, since time.Time) ([]Point, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pts := m.series[containerID][metric]
	cutoff := since.UnixMilli()
	out := make([]Point, 0, len(pts))
	for _, p := range pts {
		if p.T >= cutoff {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out, nil
}

func (m *memoryStore) Close() error { return nil }
