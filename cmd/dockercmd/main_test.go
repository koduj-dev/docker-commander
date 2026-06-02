package main

import (
	"context"
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
