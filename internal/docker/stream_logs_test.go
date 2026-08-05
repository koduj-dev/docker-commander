package docker

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/pkg/stdcopy"
)

// muxed builds a Docker-style multiplexed stream from stdout and stderr payloads.
func muxed(t *testing.T, stdout, stderr string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	if stdout != "" {
		if _, err := stdcopy.NewStdWriter(&buf, stdcopy.Stdout).Write([]byte(stdout)); err != nil {
			t.Fatal(err)
		}
	}
	if stderr != "" {
		if _, err := stdcopy.NewStdWriter(&buf, stdcopy.Stderr).Write([]byte(stderr)); err != nil {
			t.Fatal(err)
		}
	}
	return &buf
}

func collect(t *testing.T, r *bytes.Buffer) ([]LogLine, error) {
	t.Helper()
	var mu sync.Mutex
	var lines []LogLine
	err := demuxLines(context.Background(), r, func(l LogLine) {
		mu.Lock()
		lines = append(lines, l)
		mu.Unlock()
	})
	mu.Lock()
	defer mu.Unlock()
	return append([]LogLine(nil), lines...), err
}

// Returning as soon as the FIRST of the two scanners finished dropped whatever
// the other was still emitting. With follow=false — the REST log fetch — stdout
// typically ends while stderr is mid-flight, so a listing that looked complete
// was quietly missing its tail.
func TestDemuxLinesWaitsForBothStreams(t *testing.T) {
	stdout := "2024-01-01T00:00:00Z out-1\n2024-01-01T00:00:01Z out-2\n"
	stderr := "2024-01-01T00:00:02Z err-1\n2024-01-01T00:00:03Z err-2\n2024-01-01T00:00:04Z err-3\n"

	lines, err := collect(t, muxed(t, stdout, stderr))
	if err != nil {
		t.Fatalf("demuxLines: %v", err)
	}
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want 5 (both streams drained): %+v", len(lines), lines)
	}
	var outs, errs int
	for _, l := range lines {
		switch l.Stream {
		case "stdout":
			outs++
		case "stderr":
			errs++
		}
	}
	if outs != 2 || errs != 3 {
		t.Errorf("stdout=%d stderr=%d, want 2 and 3", outs, errs)
	}
}

// A line past the scanner's 1 MiB buffer ends the scan with bufio.ErrTooLong.
// Discarding that error surfaced as a clean end: the WebSocket client saw a
// normal close, and a monitor log-follower stopped silently until the next
// reconcile — missing every match in between. A stream that stopped early has to
// say so.
func TestDemuxLinesReportsAScannerThatGaveUp(t *testing.T) {
	huge := "2024-01-01T00:00:00Z " + strings.Repeat("x", 2<<20) + "\n"

	lines, err := collect(t, muxed(t, huge, ""))
	if err == nil {
		t.Fatalf("an over-long line must be reported, got nil (and %d lines)", len(lines))
	}
	if !strings.Contains(err.Error(), "too long") {
		t.Errorf("error should name the cause, got %v", err)
	}
}

// Ordinary content must still come back clean — a guard that reports errors for
// normal logs would be worse than the silence it replaces.
func TestDemuxLinesOrdinaryOutputHasNoError(t *testing.T) {
	lines, err := collect(t, muxed(t, "2024-01-01T00:00:00Z hello\n", ""))
	if err != nil {
		t.Fatalf("ordinary output: %v", err)
	}
	if len(lines) != 1 || lines[0].Message != "hello" {
		t.Errorf("unexpected lines: %+v", lines)
	}
}
