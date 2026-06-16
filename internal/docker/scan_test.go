package docker

import (
	"context"
	"strings"
	"testing"
)

// PENTEST: a ref handed to the trivy CLI must never be able to act as a flag or
// inject shell/argument tokens. ValidImageRef gates this.
func TestValidImageRef(t *testing.T) {
	ok := []string{
		"nginx",
		"nginx:latest",
		"library/nginx:1.25",
		"ghcr.io/owner/app:1.2.3",
		"registry.example.com:5000/team/svc:dev",
		"alpine@sha256:" + strings.Repeat("a", 64),
		"sha256:" + strings.Repeat("b", 64),
	}
	for _, r := range ok {
		if !ValidImageRef(r) {
			t.Errorf("expected %q to be valid", r)
		}
	}
	bad := []string{
		"",                       // empty
		"-it",                    // looks like a flag
		"--config=/etc/passwd",   // argument injection
		"-o/tmp/x",               // flag
		"nginx; rm -rf /",        // shell metachars (no shell, but reject anyway)
		"nginx latest",           // space
		"nginx\n--quiet",         // newline
		"$(id)",                  // command substitution chars
		"`id`",                   // backticks
		"nginx|cat",              // pipe
		strings.Repeat("a", 600), // over the length cap
	}
	for _, r := range bad {
		if ValidImageRef(r) {
			t.Errorf("expected %q to be REJECTED", r)
		}
	}
}

func TestParseTrivyJSON(t *testing.T) {
	data := []byte(`{
		"Results": [
			{"Target": "alpine", "Vulnerabilities": [
				{"VulnerabilityID":"CVE-1","PkgName":"openssl","InstalledVersion":"1.0","FixedVersion":"1.1","Severity":"MEDIUM","Title":"m","PrimaryURL":"u1"},
				{"VulnerabilityID":"CVE-2","PkgName":"zlib","InstalledVersion":"2.0","Severity":"CRITICAL","Title":"c","PrimaryURL":"u2"}
			]},
			{"Target": "app", "Vulnerabilities": [
				{"VulnerabilityID":"CVE-3","PkgName":"libx","InstalledVersion":"3.0","Severity":"HIGH"},
				{"VulnerabilityID":"CVE-4","PkgName":"liby","InstalledVersion":"4.0","Severity":"MEDIUM"}
			]}
		]
	}`)
	res, err := parseTrivyJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary["CRITICAL"] != 1 || res.Summary["HIGH"] != 1 || res.Summary["MEDIUM"] != 2 {
		t.Errorf("summary wrong: %v", res.Summary)
	}
	if len(res.Vulns) != 4 {
		t.Fatalf("expected 4 vulns, got %d", len(res.Vulns))
	}
	// Sorted by severity: CRITICAL first, then HIGH, then the two MEDIUM.
	if res.Vulns[0].Severity != "CRITICAL" || res.Vulns[1].Severity != "HIGH" {
		t.Errorf("not sorted by severity: %v", res.Vulns)
	}
	if res.Vulns[0].ID != "CVE-2" || res.Vulns[0].Package != "zlib" {
		t.Errorf("first vuln fields wrong: %+v", res.Vulns[0])
	}
}

func TestParseTrivyJSON_NoVulns(t *testing.T) {
	res, err := parseTrivyJSON([]byte(`{"Results":[{"Target":"x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Vulns) != 0 || len(res.Summary) != 0 {
		t.Errorf("expected a clean scan, got %+v", res)
	}
}

func TestParseTrivyJSON_Garbage(t *testing.T) {
	if _, err := parseTrivyJSON([]byte("not json")); err == nil {
		t.Error("expected an error for non-JSON trivy output")
	}
}

func TestTrivyProbe_Missing(t *testing.T) {
	if trivyProbe(context.Background(), "definitely-not-a-real-binary-xyz") {
		t.Error("probe should report a missing binary as unavailable")
	}
}

func TestCapBuffer(t *testing.T) {
	b := &capBuffer{cap: 8}
	if _, err := b.Write([]byte("1234")); err != nil {
		t.Fatalf("under cap should succeed: %v", err)
	}
	if _, err := b.Write([]byte("56789")); err == nil {
		t.Error("writing past the cap should error")
	}
}

// The concurrency guard returns a busy error (without spawning trivy) once the
// semaphore is full — bounding how many scans can run at once.
func TestScanImage_ConcurrencyLimited(t *testing.T) {
	for i := 0; i < cap(scanSem); i++ {
		scanSem <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(scanSem); i++ {
			<-scanSem
		}
	}()
	_, err := ScanImage(context.Background(), nil, "nginx:latest")
	if err == nil || !strings.Contains(err.Error(), "too many scans") {
		t.Errorf("expected a busy error when the scan semaphore is full, got %v", err)
	}
}
