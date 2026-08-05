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
	hosts     map[string]int64              // containerID -> host it was sampled from
	retention time.Duration
}

func newMemoryStore(retention time.Duration) *memoryStore {
	return &memoryStore{
		series: make(map[string]map[string][]Point), hosts: make(map[string]int64),
		retention: retention,
	}
}

func (m *memoryStore) Record(_ context.Context, samples []Sample) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(-m.retention).UnixMilli()
	m.forgetStale(cutoff)
	for _, s := range samples {
		m.hosts[s.ContainerID] = s.HostID
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

// forgetStale drops containers that have stopped being sampled.
//
// Trimming only ever ran for the containers in the current batch, so a container
// that was deleted kept its whole retention window — seven metrics × up to six
// hours at 15s is ~10k points — plus its hosts entry, for as long as the process
// lived. On a host with churn that is a slow leak with no upper bound.
//
// Caller holds m.mu.
func (m *memoryStore) forgetStale(cutoff int64) {
	for cid, byMetric := range m.series {
		newest := int64(0)
		for _, pts := range byMetric {
			if n := len(pts); n > 0 && pts[n-1].T > newest {
				newest = pts[n-1].T
			}
		}
		if newest < cutoff {
			delete(m.series, cid)
			delete(m.hosts, cid)
		}
	}
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

func (m *memoryStore) HostFor(_ context.Context, containerID string) (int64, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.hosts[containerID]
	return id, ok, nil
}
