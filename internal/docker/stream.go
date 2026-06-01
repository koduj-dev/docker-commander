package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// ErrUnknownAction is returned by ContainerAction for unsupported actions.
var ErrUnknownAction = errors.New("docker: unknown container action")

// StreamStats subscribes to a container's stats stream and invokes emit for
// each computed sample until ctx is cancelled or the stream ends.
func (m *Manager) StreamStats(ctx context.Context, hostID int64, id string, emit func(StatsSample)) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	resp, err := cli.ContainerStats(ctx, id, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	dec := json.NewDecoder(resp.Body)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var raw container.StatsResponse
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		emit(computeSample(id, &raw))
	}
}

// SampleStats fetches a single (non-streaming) stats frame and computes one
// sample. Used by the monitor for periodic polling and the Prometheus exporter.
func (m *Manager) SampleStats(ctx context.Context, hostID int64, id string) (StatsSample, error) {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return StatsSample{}, err
	}
	resp, err := cli.ContainerStats(ctx, id, false)
	if err != nil {
		return StatsSample{}, err
	}
	defer resp.Body.Close()
	var raw container.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return StatsSample{}, err
	}
	return computeSample(id, &raw), nil
}

// computeSample converts a raw Docker stats frame into a flat StatsSample,
// applying the standard CPU/memory percentage formulas used by `docker stats`.
func computeSample(id string, s *container.StatsResponse) StatsSample {
	sample := StatsSample{
		ContainerID: id,
		Timestamp:   time.Now().UnixMilli(),
		PIDs:        s.PidsStats.Current,
	}

	// CPU percentage relative to the whole host (all cores = 100% * nCPU).
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
	cpus := float64(s.CPUStats.OnlineCPUs)
	if cpus == 0 {
		cpus = float64(len(s.CPUStats.CPUUsage.PercpuUsage))
	}
	if sysDelta > 0 && cpuDelta > 0 {
		sample.CPUPercent = (cpuDelta / sysDelta) * cpus * 100.0
	}

	// Memory: subtract page cache so the figure matches `docker stats`.
	usage := s.MemoryStats.Usage
	if cache, ok := s.MemoryStats.Stats["inactive_file"]; ok && cache < usage {
		usage -= cache
	} else if cache, ok := s.MemoryStats.Stats["cache"]; ok && cache < usage {
		usage -= cache
	}
	sample.MemUsage = usage
	sample.MemLimit = s.MemoryStats.Limit
	if s.MemoryStats.Limit > 0 {
		sample.MemPercent = float64(usage) / float64(s.MemoryStats.Limit) * 100.0
	}

	for _, n := range s.Networks {
		sample.NetRx += n.RxBytes
		sample.NetTx += n.TxBytes
	}
	for _, b := range s.BlkioStats.IoServiceBytesRecursive {
		switch b.Op {
		case "read", "Read":
			sample.BlkRead += b.Value
		case "write", "Write":
			sample.BlkWrite += b.Value
		}
	}
	return sample
}

// LogLine is one line emitted from a container log stream.
type LogLine struct {
	Stream    string `json:"stream"` // "stdout" | "stderr"
	Message   string `json:"message"`
	Timestamp string `json:"timestamp,omitempty"`
}

// StreamLogs tails a container's logs, invoking emit per line. When follow is
// true it streams until ctx is cancelled; tail bounds the initial backlog.
func (m *Manager) StreamLogs(ctx context.Context, hostID int64, id string, follow bool, tail string, emit func(LogLine)) error {
	cli, err := m.Client(ctx, hostID)
	if err != nil {
		return err
	}
	if tail == "" {
		tail = "200"
	}
	reader, err := cli.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Timestamps: true,
		Tail:       tail,
	})
	if err != nil {
		return err
	}
	defer reader.Close()

	// Docker multiplexes stdout/stderr; demux into two pipes we read linewise.
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	go func() {
		_, _ = stdcopy.StdCopy(stdoutW, stderrW, reader)
		_ = stdoutW.Close()
		_ = stderrW.Close()
	}()

	done := make(chan struct{}, 2)
	scan := func(r io.Reader, stream string) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			emit(splitTimestamp(stream, sc.Text()))
		}
		done <- struct{}{}
	}
	go scan(stdoutR, "stdout")
	go scan(stderrR, "stderr")

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

// splitTimestamp separates the RFC3339Nano timestamp Docker prepends (because
// Timestamps:true) from the log message body.
func splitTimestamp(stream, line string) LogLine {
	if i := indexByte(line, ' '); i > 0 {
		return LogLine{Stream: stream, Timestamp: line[:i], Message: line[i+1:]}
	}
	return LogLine{Stream: stream, Message: line}
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
