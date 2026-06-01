// Package api wires the HTTP surface together: REST endpoints for auth and
// Docker operations, the WebSocket upgrade, and serving the embedded SPA.
package api

import (
	"context"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
	"github.com/koduj-dev/docker-commander/internal/ws"
)

// Server bundles all dependencies needed to serve HTTP.
type Server struct {
	cfg    config.Config
	store  *store.Store
	auth   *auth.Service
	mw     *auth.Middleware
	docker *docker.Manager
	hub    *ws.Hub
	webFS  fs.FS // built SPA assets, or nil in dev mode
}

// NewServer constructs the API server.
func NewServer(cfg config.Config, st *store.Store, authSvc *auth.Service, mw *auth.Middleware, dm *docker.Manager, hub *ws.Hub, webFS fs.FS) *Server {
	return &Server{cfg: cfg, store: st, auth: authSvc, mw: mw, docker: dm, hub: hub, webFS: webFS}
}

// Handler builds the root http.Handler with all routes mounted.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)
	if s.cfg.Dev {
		r.Use(devCORS)
	}

	r.Route("/api", func(r chi.Router) {
		// Public auth endpoints (no session required).
		r.Group(func(r chi.Router) {
			r.Get("/auth/status", s.handleAuthStatus)
			r.Post("/auth/setup", s.handleSetup)
			r.Post("/auth/login", s.handleLogin)
			r.Post("/auth/2fa", s.handleVerify2FA)
		})

		// Authenticated endpoints.
		r.Group(func(r chi.Router) {
			r.Use(s.mw.RequireSession)

			r.Get("/auth/me", s.handleMe)
			r.Post("/auth/logout", s.handleLogout)
			r.Post("/auth/totp/setup", s.handleTOTPSetup)
			r.Post("/auth/totp/enable", s.handleTOTPEnable)

			r.Get("/hosts", s.handleListHosts)

			r.Get("/containers", s.handleListContainers)
			r.Get("/containers/{id}", s.handleInspectContainer)
			r.Post("/containers/{id}/{action}", s.handleContainerAction)

			r.Get("/networks", s.handleListNetworks)
			r.Get("/system", s.handleSystemInfo)
			r.Get("/audit", s.handleAudit)

			// WebSocket for live stats/logs.
			r.Get("/ws", s.handleWebSocket)
		})
	})

	// Everything else serves the embedded single-page app (if present).
	if s.webFS != nil {
		r.Handle("/*", s.spaHandler())
	}
	return r
}

// resolveHostID returns the host id from the "host" query param, or 0 to mean
// "the default local host" (the docker Manager resolves 0 to the local daemon).
func (s *Server) resolveHostID(r *http.Request) (int64, error) {
	if q := r.URL.Query().Get("host"); q != "" {
		return strconv.ParseInt(q, 10, 64)
	}
	return 0, nil
}

// audit records an action, ignoring failures (best-effort).
func (s *Server) audit(r *http.Request, action, target, detail string) {
	var uid int64
	var uname string
	if c, ok := auth.ClaimsFrom(r.Context()); ok {
		uid, uname = c.UserID, c.Username
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.store.Audit(ctx, store.AuditEntry{
		UserID: uid, Username: uname, Action: action, Target: target,
		Detail: detail, IP: r.RemoteAddr,
	})
}
