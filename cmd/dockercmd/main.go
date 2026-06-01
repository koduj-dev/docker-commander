// Command dockercmd is the Docker Commander server: a single binary that
// monitors and controls Docker containers and serves the embedded web UI.
package main

import (
	"context"
	"crypto/rand"
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
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/history"
	"github.com/koduj-dev/docker-commander/internal/monitor"
	"github.com/koduj-dev/docker-commander/internal/store"
	"github.com/koduj-dev/docker-commander/internal/ws"
	"github.com/koduj-dev/docker-commander/web"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

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

	// Serve the embedded SPA unless running in dev mode (Vite serves the UI).
	webFS := serveWebFS(cfg)

	srv := api.NewServer(cfg, st, authSvc, mw, dm, hub, mon, hist, webFS)
	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: WebSocket streams are long-lived.
	}

	go func() {
		<-shutdownCtx.Done()
		log.Println("shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}()

	logStartup(cfg)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// loadOrCreateJWTSecret returns a persistent signing secret, generating one on
// first run. Keeping it stable means sessions survive restarts.
func loadOrCreateJWTSecret(ctx context.Context, st *store.Store) ([]byte, error) {
	const key = "jwt_secret"
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
	log.Printf("Docker Commander listening on http://%s", cfg.Addr)
	log.Printf("data dir: %s", cfg.DataDir)
	if cfg.Dev {
		log.Printf("dev mode: serving API only; run the Vite dev server for the UI")
	}
}
