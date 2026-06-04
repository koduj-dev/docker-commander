package docker

import (
	"context"
	"sort"
	"sync"
)

// statsConcurrency bounds how many per-container stats calls run at once, so a
// host with many containers doesn't open a connection per container.
const statsConcurrency = 8

// ResourceOverview samples every running container and reports each one's share
// of the host's total CPU and memory. Per-container stats are gathered
// concurrently (bounded); a container that fails to sample is simply omitted.
func (m *Manager) ResourceOverview(ctx context.Context, hostID int64) (ResourceOverview, error) {
	info, err := m.SystemInfo(ctx, hostID)
	if err != nil {
		return ResourceOverview{}, err
	}
	containers, err := m.ListContainers(ctx, hostID)
	if err != nil {
		return ResourceOverview{}, err
	}

	var running []ContainerSummary
	for _, c := range containers {
		if c.State == "running" {
			running = append(running, c)
		}
	}

	out := ResourceOverview{CPUs: info.CPUs, MemTotal: info.MemTotal}
	results := make([]ResourceUsage, len(running))
	sem := make(chan struct{}, statsConcurrency)
	var wg sync.WaitGroup
	for i, c := range running {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, c ContainerSummary) {
			defer wg.Done()
			defer func() { <-sem }()

			s, err := m.SampleStats(ctx, hostID, c.ID)
			if err != nil {
				return // leaves results[i].ID == "" so it's dropped below
			}
			// CPUPercent is scaled so one core == 100%; divide by the core count
			// to get the share of the whole host (0..100).
			cpuShare := s.CPUPercent
			if info.CPUs > 0 {
				cpuShare = s.CPUPercent / float64(info.CPUs)
			}
			var memShare float64
			if info.MemTotal > 0 {
				memShare = float64(s.MemUsage) / float64(info.MemTotal) * 100
			}
			results[i] = ResourceUsage{
				ID:         c.ID,
				Name:       c.Name,
				CPUPercent: cpuShare,
				MemBytes:   s.MemUsage,
				MemPercent: memShare,
			}
		}(i, c)
	}
	wg.Wait()

	for _, r := range results {
		if r.ID != "" {
			out.Containers = append(out.Containers, r)
		}
	}
	// Busiest first (by CPU, then memory) so the UI can show the top consumers.
	sort.SliceStable(out.Containers, func(i, j int) bool {
		a, b := out.Containers[i], out.Containers[j]
		if a.CPUPercent != b.CPUPercent {
			return a.CPUPercent > b.CPUPercent
		}
		return a.MemBytes > b.MemBytes
	})
	return out, nil
}
