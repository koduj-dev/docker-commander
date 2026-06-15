package main

import (
	"bytes"
	"context"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/store"
)

func TestLoadOrCreateSecret(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	a, err := loadOrCreateSecret(ctx, st, "k")
	if err != nil || len(a) != 32 {
		t.Fatalf("first call should generate a 32-byte secret: len=%d err=%v", len(a), err)
	}
	b, err := loadOrCreateSecret(ctx, st, "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Error("subsequent calls must return the persisted secret")
	}
	// JWT helper wraps the generic one with a fixed key.
	if s, err := loadOrCreateJWTSecret(ctx, st); err != nil || len(s) != 32 {
		t.Errorf("jwt secret: len=%d err=%v", len(s), err)
	}
}

func TestServeWebFS(t *testing.T) {
	// Production: the embedded dist is returned and contains index.html.
	dist := serveWebFS(config.Config{})
	if dist == nil {
		t.Fatal("expected embedded web assets in non-dev mode")
	}
	if _, err := dist.Open("index.html"); err != nil {
		t.Errorf("embedded dist should contain index.html: %v", err)
	}
	// Dev mode hands the UI to Vite, so no embedded FS is served.
	if serveWebFS(config.Config{Dev: true}) != nil {
		t.Error("dev mode should not serve embedded assets")
	}
}

func TestLogStartup(t *testing.T) {
	// Smoke test: it only logs, so we just make sure both branches run cleanly.
	logStartup(config.Config{Addr: "127.0.0.1:8080", DataDir: "/tmp/dc"})
	logStartup(config.Config{Addr: "127.0.0.1:8080", DataDir: "/tmp/dc", Dev: true})
}

// withArgs swaps os.Args for the duration of fn (the standalone-action helpers
// scan os.Args directly).
func withArgs(args []string, fn func()) {
	old := os.Args
	defer func() { os.Args = old }()
	os.Args = append([]string{"dockercmd"}, args...)
	fn()
}

func TestStandaloneActionArgs(t *testing.T) {
	withArgs([]string{"--version"}, func() {
		if !wantsVersion() {
			t.Error("--version should be recognised")
		}
	})
	withArgs([]string{"version"}, func() {
		if !wantsVersion() {
			t.Error("bare `version` subcommand should be recognised")
		}
	})
	withArgs([]string{"--self-upgrade", "--check"}, func() {
		up, checkOnly := wantsSelfUpgrade()
		if !up || !checkOnly {
			t.Errorf("--self-upgrade --check: up=%v checkOnly=%v", up, checkOnly)
		}
		if wantsVersion() {
			t.Error("--self-upgrade is not a version request")
		}
	})
	withArgs([]string{"--install-service"}, func() {
		if got := serviceAction(); got != "install" {
			t.Errorf("serviceAction = %q, want install", got)
		}
	})
	// Args after `--` must be ignored.
	withArgs([]string{"--", "--version"}, func() {
		if wantsVersion() {
			t.Error("--version after `--` should be ignored")
		}
	})
	// Plain server start: no action.
	withArgs([]string{"-port", "9000"}, func() {
		if wantsVersion() || serviceAction() != "" {
			t.Error("a normal server invocation should not match any standalone action")
		}
	})
}

func TestUsageListsStandaloneActions(t *testing.T) {
	var buf bytes.Buffer
	old := flag.CommandLine.Output()
	flag.CommandLine.SetOutput(&buf)
	defer flag.CommandLine.SetOutput(old)

	usage()
	out := buf.String()
	for _, want := range []string{"--version", "--self-upgrade", "--install-service", "--uninstall-service", "--service-status"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage() output is missing the %q action:\n%s", want, out)
		}
	}
}

// TestManPageDocumentsActions ensures the standalone actions stay documented in
// the man page (the config package test covers the flags).
func TestManPageDocumentsActions(t *testing.T) {
	man, err := os.ReadFile("../../deploy/dockercmd.1")
	if err != nil {
		t.Fatalf("read deploy/dockercmd.1: %v", err)
	}
	manStr := string(man)
	for _, want := range []string{"version", "self-upgrade", "install-service", "uninstall-service", "service-status"} {
		if !strings.Contains(manStr, want) {
			t.Errorf("action %q is not documented in deploy/dockercmd.1", want)
		}
	}
}
