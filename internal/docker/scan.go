package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Image vulnerability scanning via the Trivy CLI (https://trivy.dev). Trivy is an
// optional runtime dependency, probed once like the compose CLI; when it's
// absent the feature degrades gracefully. A scan targets the selected host's
// daemon (the caller passes ComposeHostEnv's DOCKER_HOST env), so the image is
// read from wherever it lives. Results are returned live (not persisted) — a
// cached scan goes stale as new CVEs are published.

const scanTimeout = 6 * time.Minute // first run also downloads Trivy's vuln DB

var (
	trivyOnce sync.Once
	trivyOK   bool
)

// TrivyAvailable reports whether the `trivy` CLI is usable on the host running
// Docker Commander. Probed once and cached for the process lifetime.
func TrivyAvailable(ctx context.Context) bool {
	_ = ctx
	trivyOnce.Do(func() { trivyOK = trivyProbe(context.Background(), "trivy") })
	return trivyOK
}

// trivyProbe runs `<bin> version`; split out so tests can exercise the
// not-found path with a bogus binary.
func trivyProbe(ctx context.Context, bin string) bool {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, bin, "version").Run() == nil
}

// Vulnerability is one finding from a scan.
type Vulnerability struct {
	ID           string `json:"id"`
	Severity     string `json:"severity"`
	Package      string `json:"package"`
	Version      string `json:"version"`
	FixedVersion string `json:"fixedVersion,omitempty"`
	Title        string `json:"title,omitempty"`
	URL          string `json:"url,omitempty"`
}

// ScanResult is the parsed outcome of an image scan.
type ScanResult struct {
	Ref     string          `json:"ref"`
	Summary map[string]int  `json:"summary"` // severity → count
	Vulns   []Vulnerability `json:"vulns"`
}

// safeImageRef guards what we hand to the trivy CLI. A ref is
// "[host[:port]/]repo[:tag][@sha256:digest]" — all lowercase-ish with a limited
// punctuation set, and crucially it must NOT start with '-' (which trivy would
// read as a flag — argument injection).
var safeImageRef = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/@-]{0,511}$`)

// ValidImageRef reports whether ref is a safe image reference to pass to an
// external CLI as a positional argument.
func ValidImageRef(ref string) bool {
	return safeImageRef.MatchString(ref)
}

const (
	maxScanVulns = 5000     // cap the returned list defensively
	maxScanBytes = 64 << 20 // cap Trivy's JSON output (guards against an OOM-sized report)
)

// scanSem bounds concurrent trivy processes so scans (heavy on CPU, disk and
// network) can't be fan-fired to exhaust the host.
var scanSem = make(chan struct{}, 2)

// capBuffer is a bytes.Buffer that errors once it exceeds cap, so a runaway
// report can't be buffered unbounded into memory.
type capBuffer struct {
	buf bytes.Buffer
	cap int
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.cap {
		return 0, fmt.Errorf("scan output exceeded %d bytes", c.cap)
	}
	return c.buf.Write(p)
}

// ScanImage scans an image reference for vulnerabilities with Trivy, targeting
// the daemon described by env (from ComposeHostEnv; nil = local). The ref must
// pass ValidImageRef. Returns a summary + the vulnerability list.
func ScanImage(ctx context.Context, env []string, ref string) (ScanResult, error) {
	if !ValidImageRef(ref) {
		return ScanResult{}, fmt.Errorf("invalid image reference")
	}
	select {
	case scanSem <- struct{}{}:
		defer func() { <-scanSem }()
	default:
		return ScanResult{}, fmt.Errorf("too many scans in progress — try again shortly")
	}

	cctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()

	// --quiet silences progress; --scanners vuln keeps it to CVEs (no secret/license
	// scanning); the ref is a validated positional arg, so it can't be a flag.
	cmd := exec.CommandContext(cctx, "trivy", "image", "--quiet", "--format", "json", "--scanners", "vuln", ref)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	stdout := &capBuffer{cap: maxScanBytes}
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := trimTrivyErr(stderr.String()); msg != "" {
			return ScanResult{}, fmt.Errorf("trivy: %s", msg)
		}
		return ScanResult{}, err
	}
	res, err := parseTrivyJSON(stdout.buf.Bytes())
	if err != nil {
		return ScanResult{}, err
	}
	res.Ref = ref
	return res, nil
}

// parseTrivyJSON flattens Trivy's report into a ScanResult (summary + vulns),
// sorted by severity then ID, capped at maxScanVulns.
func parseTrivyJSON(data []byte) (ScanResult, error) {
	var report struct {
		Results []struct {
			Vulnerabilities []struct {
				VulnerabilityID  string `json:"VulnerabilityID"`
				PkgName          string `json:"PkgName"`
				InstalledVersion string `json:"InstalledVersion"`
				FixedVersion     string `json:"FixedVersion"`
				Severity         string `json:"Severity"`
				Title            string `json:"Title"`
				PrimaryURL       string `json:"PrimaryURL"`
			} `json:"Vulnerabilities"`
		} `json:"Results"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		return ScanResult{}, fmt.Errorf("parse trivy output: %w", err)
	}
	out := ScanResult{Summary: map[string]int{}, Vulns: []Vulnerability{}}
	for _, r := range report.Results {
		for _, v := range r.Vulnerabilities {
			out.Summary[v.Severity]++
			if len(out.Vulns) < maxScanVulns {
				out.Vulns = append(out.Vulns, Vulnerability{
					ID: v.VulnerabilityID, Severity: v.Severity, Package: v.PkgName,
					Version: v.InstalledVersion, FixedVersion: v.FixedVersion,
					Title: v.Title, URL: v.PrimaryURL,
				})
			}
		}
	}
	sort.SliceStable(out.Vulns, func(i, j int) bool {
		ri, rj := severityRank(out.Vulns[i].Severity), severityRank(out.Vulns[j].Severity)
		if ri != rj {
			return ri < rj
		}
		return out.Vulns[i].ID < out.Vulns[j].ID
	})
	return out, nil
}

func severityRank(s string) int {
	switch s {
	case "CRITICAL":
		return 0
	case "HIGH":
		return 1
	case "MEDIUM":
		return 2
	case "LOW":
		return 3
	default:
		return 4
	}
}

// trimTrivyErr returns the last non-empty line of trivy's stderr (its errors end
// with the useful message), bounded so we never echo a huge blob.
func trimTrivyErr(s string) string {
	const max = 400
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			if len(t) > max {
				t = t[:max]
			}
			return t
		}
	}
	return ""
}
