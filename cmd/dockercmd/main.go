// Command dockercmd is the Docker Commander server: a single binary that
// monitors and controls Docker containers and serves the embedded web UI.
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/koduj-dev/docker-commander/internal/api"
	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/history"
	"github.com/koduj-dev/docker-commander/internal/monitor"
	"github.com/koduj-dev/docker-commander/internal/selfupdate"
	"github.com/koduj-dev/docker-commander/internal/service"
	"github.com/koduj-dev/docker-commander/internal/store"
	"github.com/koduj-dev/docker-commander/internal/ws"
	"github.com/koduj-dev/docker-commander/web"
)

// version is set at build time via -ldflags "-X main.version=…"; "dev" otherwise.
var version = "dev"

func main() {
	if err := run(); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

// wantsSelfUpgrade reports whether the user invoked the standalone
// `--self-upgrade` action (intercepted before the server config flags parse),
// and whether `--check` was passed (report only, don't install).
func wantsSelfUpgrade() (yes, checkOnly bool) {
	for _, a := range os.Args[1:] {
		if a == "--" {
			break
		}
		switch a {
		case "-self-upgrade", "--self-upgrade":
			yes = true
		case "-check", "--check":
			checkOnly = true
		}
	}
	return yes, checkOnly
}

// serviceAction reports the standalone service-management action the user asked
// for (install/uninstall/status), or "" to start the server normally. Like
// `--self-upgrade`, these run instead of the server.
func serviceAction() string {
	for _, a := range os.Args[1:] {
		if a == "--" {
			break
		}
		switch a {
		case "-install-service", "--install-service":
			return "install"
		case "-uninstall-service", "--uninstall-service":
			return "uninstall"
		case "-service-status", "--service-status":
			return "status"
		}
	}
	return ""
}

func run() error {
	// Standalone CLI actions run instead of starting the server.
	if up, checkOnly := wantsSelfUpgrade(); up {
		return selfupdate.Run(context.Background(), version, os.Stdout, checkOnly)
	}
	switch serviceAction() {
	case "install":
		return service.Install(os.Stdout)
	case "uninstall":
		return service.Uninstall(os.Stdout)
	case "status":
		return service.Status(os.Stdout)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Version = version // expose the build version to the API/UI

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	if err := st.EnsureLocalHost(ctx); err != nil {
		return err
	}

	secret, err := loadOrCreateJWTSecret(ctx, st)
	if err != nil {
		return err
	}

	// Encryption key for secrets at rest (registry credentials).
	encKey, err := loadOrCreateSecret(ctx, st, "registry_enc_key")
	if err != nil {
		return err
	}
	cipher, err := crypto.New(encKey)
	if err != nil {
		return err
	}
	st.SetCipher(cipher)

	tokens := auth.NewTokenManager(secret, cfg.SessionTTL)
	authSvc := auth.NewService(st, tokens)
	mw := auth.NewMiddleware(tokens)

	dm := docker.NewManager(st)
	defer dm.Close()
	hub := ws.NewHub(dm)

	// Graceful shutdown on SIGINT/SIGTERM (declared early so the monitor binds
	// to the same lifecycle as the HTTP server).
	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Metrics history store (Redis if configured, else in-memory).
	hist := history.Open(shutdownCtx, history.Config{
		RedisAddr:     cfg.RedisAddr,
		RedisPassword: cfg.RedisPassword,
		RedisDB:       cfg.RedisDB,
		Retention:     cfg.MetricsRetention,
	})
	defer hist.Close()

	// Start the alerting engine in the background.
	mon := monitor.New(st, dm, hist)
	go mon.Run(shutdownCtx)

	// Clear any volume-browser helper containers left over from a previous run.
	go dm.ReapAllVolumeHelpers(shutdownCtx)

	// Serve the embedded SPA unless running in dev mode (Vite serves the UI).
	webFS := serveWebFS(cfg)

	srv := api.NewServer(cfg, st, authSvc, mw, dm, hub, mon, hist, webFS)
	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: WebSocket streams are long-lived.
	}
	tlsEnabled := cfg.TLSCert != "" && cfg.TLSKey != ""
	if tlsEnabled {
		httpServer.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	go func() {
		<-shutdownCtx.Done()
		log.Println("shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}()

	logStartup(cfg)
	serve := httpServer.ListenAndServe
	if tlsEnabled {
		// Cert/key paths are passed to ServeTLS; the http.Server reads them.
		serve = func() error { return httpServer.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey) }
	}
	if err := serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// loadOrCreateJWTSecret returns a persistent signing secret, generating one on
// first run. Keeping it stable means sessions survive restarts.
func loadOrCreateJWTSecret(ctx context.Context, st *store.Store) ([]byte, error) {
	return loadOrCreateSecret(ctx, st, "jwt_secret")
}

// loadOrCreateSecret returns a persistent 32-byte secret stored under key in
// the settings table, generating one on first run.
func loadOrCreateSecret(ctx context.Context, st *store.Store, key string) ([]byte, error) {
	existing, err := st.Setting(ctx, key)
	if err != nil {
		return nil, err
	}
	if existing != "" {
		return base64.StdEncoding.DecodeString(existing)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	if err := st.SetSetting(ctx, key, base64.StdEncoding.EncodeToString(buf)); err != nil {
		return nil, err
	}
	return buf, nil
}

func serveWebFS(cfg config.Config) fs.FS {
	if cfg.Dev {
		return nil // Vite dev server hosts the UI on its own port
	}
	dist, err := web.DistFS()
	if err != nil {
		log.Printf("warning: embedded web assets unavailable: %v", err)
		return nil
	}
	return dist
}

func logStartup(cfg config.Config) {
	scheme := "http"
	if cfg.TLSCert != "" && cfg.TLSKey != "" {
		scheme = "https"
	}
	log.Printf("Docker Commander %s listening on %s://%s", version, scheme, cfg.Addr)
	if cfg.ConfigFile != "" {
		log.Printf("config file: %s", cfg.ConfigFile)
	} else {
		log.Printf("config file: none (flags/env only)")
	}
	log.Printf("data dir: %s", cfg.DataDir)
	if cfg.MCPEnabled {
		oauth := "bearer tokens only (set DC_MCP_PUBLIC_URL to enable OAuth)"
		if cfg.MCPPublicURL != "" {
			oauth = "bearer tokens + OAuth (" + cfg.MCPPublicURL + ")"
		}
		log.Printf("MCP server: ENABLED at %s://%s/mcp — auth: %s", scheme, cfg.Addr, oauth)
	} else {
		log.Printf("MCP server: disabled (set DC_MCP_ENABLED=1 to enable)")
	}
	if cfg.Dev {
		log.Printf("dev mode: serving API only; run the Vite dev server for the UI")
	}
}
