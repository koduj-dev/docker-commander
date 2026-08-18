package service

import (
	"context"
	"encoding/xml"
	"os"
	"runtime"
	"strings"
	"testing"
	"text/template"
)

// TestSystemdUnitMatchesDeployFile keeps the embedded unit and the canonical
// deploy/dockercmd.service in lockstep — go:embed can't reach across ../.. into
// deploy/, so the file is duplicated and this test is the single-source-of-truth
// guard. If it fails, copy deploy/dockercmd.service over internal/service/unit.service.
func TestSystemdUnitMatchesDeployFile(t *testing.T) {
	want, err := os.ReadFile("../../deploy/dockercmd.service")
	if err != nil {
		t.Fatalf("read deploy unit: %v", err)
	}
	if string(want) != systemdUnit {
		t.Error("internal/service/unit.service drifted from deploy/dockercmd.service — " +
			"keep them byte-identical (cp deploy/dockercmd.service internal/service/unit.service)")
	}
}

// TestManPageMatchesDeployFile keeps the embedded man page and the canonical
// deploy/dockercmd.1 in lockstep (go:embed can't reach across ../.. into
// deploy/). If it fails, copy deploy/dockercmd.1 over internal/service/dockercmd.1.
func TestManPageMatchesDeployFile(t *testing.T) {
	want, err := os.ReadFile("../../deploy/dockercmd.1")
	if err != nil {
		t.Fatalf("read deploy man page: %v", err)
	}
	if string(want) != manPage {
		t.Error("internal/service/dockercmd.1 drifted from deploy/dockercmd.1 — " +
			"keep them byte-identical (cp deploy/dockercmd.1 internal/service/dockercmd.1)")
	}
}

// TestLaunchdPlistEscapesValues renders the embedded plist template the same way
// the macOS installer does and checks that XML-special characters in a path are
// escaped — so the result stays valid XML that launchctl can load.
func TestLaunchdPlistEscapesValues(t *testing.T) {
	tmpl, err := template.New("plist").Funcs(template.FuncMap{"xml": xmlEscape}).Parse(launchdPlistTmpl)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	var out strings.Builder
	data := map[string]string{
		"Label":   launchdLabel,
		"BinPath": "/Users/a&b/<dir>/dockercmd",
		"DataDir": "/Users/a&b/data",
		"LogPath": "/Users/a&b/log",
	}
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "a&b") || strings.Contains(got, "<dir>") {
		t.Error("raw XML-special characters leaked into the plist (not escaped)")
	}
	if !strings.Contains(got, "a&amp;b") {
		t.Errorf("expected '&' to be escaped to '&amp;'; got:\n%s", got)
	}
	// The whole document must parse as well-formed XML.
	if err := xml.Unmarshal([]byte(got), new(struct{ XMLName xml.Name })); err != nil {
		t.Errorf("rendered plist is not valid XML: %v", err)
	}
}

// TestIsWindowsServiceFalseInTestBinary runs on every OS: a `go test` process
// is never started by the Windows Service Control Manager, so IsWindowsService
// must report false everywhere — including the real windows/svc-backed check
// on a Windows build, not just the !windows stub.
func TestIsWindowsServiceFalseInTestBinary(t *testing.T) {
	if IsWindowsService() {
		t.Error("IsWindowsService() = true in a go test binary, want false")
	}
}

// TestRunWindowsServiceStubErrorsOnNonWindows guards the !windows stub in
// service_notwindows.go: cmd/dockercmd calls RunWindowsService unconditionally
// (behind an IsWindowsService() check that's always false here), so the stub
// must fail loudly instead of silently doing nothing if it were ever reached.
// Skipped on windows, where RunWindowsService is the real svc.Run and has
// entirely different (blocking, SCM-dependent) behavior.
func TestRunWindowsServiceStubErrorsOnNonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("RunWindowsService is the real SCM entry point on windows, not this stub")
	}
	if err := RunWindowsService(func(context.Context) error { return nil }); err == nil {
		t.Error("RunWindowsService() on a non-Windows stub returned nil error, want an error")
	}
}
