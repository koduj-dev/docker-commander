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
	"github.com/koduj-dev/docker-commander/internal/history"
	"github.com/koduj-dev/docker-commander/internal/monitor"
	"github.com/koduj-dev/docker-commander/internal/store"
	"github.com/koduj-dev/docker-commander/internal/ws"
)

// Server bundles all dependencies needed to serve HTTP.
type Server struct {
	cfg          config.Config
	store        *store.Store
	auth         *auth.Service
	mw           *auth.Middleware
	docker       *docker.Manager
	hub          *ws.Hub
	monitor      *monitor.Monitor
	history      history.Store
	metricsToken string
	webFS        fs.FS // built SPA assets, or nil in dev mode
	update       *updateChecker
}

// NewServer constructs the API server.
func NewServer(cfg config.Config, st *store.Store, authSvc *auth.Service, mw *auth.Middleware, dm *docker.Manager, hub *ws.Hub, mon *monitor.Monitor, hist history.Store, webFS fs.FS) *Server {
	return &Server{
		cfg: cfg, store: st, auth: authSvc, mw: mw, docker: dm, hub: hub,
		monitor: mon, history: hist, metricsToken: cfg.MetricsToken, webFS: webFS,
		update: newUpdateChecker(cfg.Version, cfg.UpdateCheck),
	}
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
			r.Use(s.permissions) // role / section / read-only / feature-flag enforcement

			r.Get("/auth/me", s.handleMe)
			r.Post("/auth/logout", s.handleLogout)
			r.Post("/auth/totp/setup", s.handleTOTPSetup)
			r.Post("/auth/totp/enable", s.handleTOTPEnable)

			// User management + app settings (admin only, enforced by section "__admin").
			r.Get("/users", s.handleListUsers)
			r.Post("/users", s.handleCreateUser)
			r.Patch("/users/{id}", s.handleUpdateUser)
			r.Post("/users/{id}/password", s.handleResetUserPassword)
			r.Delete("/users/{id}", s.handleDeleteUser)
			r.Get("/settings", s.handleGetSettings)
			r.Put("/settings", s.handleSetSettings)
			r.Get("/ldap", s.handleGetLDAP)
			r.Put("/ldap", s.handleSetLDAP)
			r.Post("/ldap/test", s.handleTestLDAP)
			r.Get("/update", s.handleUpdateStatus) // admin-only (section "__admin")

			r.Get("/hosts", s.handleListHosts)
			r.Post("/hosts", s.handleCreateHost)
			r.Patch("/hosts/{id}", s.handleUpdateHost)
			r.Delete("/hosts/{id}", s.handleDeleteHost)
			r.Get("/hosts/{id}/test", s.handleTestHost)
			r.Post("/hosts/{id}/trust", s.handleTrustHost)

			r.Get("/containers", s.handleListContainers)
			r.Post("/containers", s.handleCreateContainer)
			r.Get("/containers/{id}", s.handleInspectContainer)
			r.Get("/containers/{id}/diff", s.handleContainerDiff)
			r.Get("/containers/{id}/top", s.handleContainerTop)
			r.Get("/containers/{id}/export", s.handleExportContainer)
			// In-container file browser (docker cp + ls/rm via exec).
			r.Get("/containers/{id}/files", s.handleListFiles)
			r.Get("/containers/{id}/files/download", s.handleDownloadFile)
			r.Post("/containers/{id}/files/upload", s.handleUploadFile)
			r.Post("/containers/{id}/files/mkdir", s.handleMakeDir)
			r.Post("/containers/{id}/files/extract", s.handleExtractFile)
			r.Delete("/containers/{id}/files", s.handleDeleteFile)
			// Static sub-routes registered before the {action} catch-all.
			r.Post("/containers/{id}/rename", s.handleRenameContainer)
			r.Post("/containers/{id}/update", s.handleUpdateContainer)
			r.Post("/containers/{id}/commit", s.handleCommitContainer)
			r.Post("/containers/{id}/probe", s.handleProbePorts)
			r.Post("/containers/{id}/{action}", s.handleContainerAction)

			// Compose stacks (grouped by the compose project label).
			r.Get("/stacks", s.handleListStacks)
			r.Get("/stacks/{project}/compose", s.handleStackCompose)
			r.Post("/stacks/{project}/{action}", s.handleStackAction)

			// Compose projects: managed folders deployed via the docker compose
			// CLI on the host running DC (local-only — these routes ignore ?host=).
			r.Get("/projects", s.handleListProjects)
			r.Post("/projects", s.handleCreateProject)
			r.Post("/projects/import", s.handleImportProject)
			r.Get("/projects/{id}", s.handleGetProject)
			r.Patch("/projects/{id}", s.handleRenameProject)
			r.Delete("/projects/{id}", s.handleDeleteProject)
			r.Get("/projects/{id}/files", s.handleListProjectFiles)
			r.Put("/projects/{id}/files", s.handleWriteProjectFile)
			r.Post("/projects/{id}/files/raw", s.handleUploadProjectFileRaw)
			r.Get("/projects/{id}/files/raw", s.handleDownloadProjectFile)
			r.Delete("/projects/{id}/files", s.handleDeleteProjectFile)
			r.Post("/projects/{id}/files/dir", s.handleMakeProjectDir)
			r.Get("/projects/{id}/download", s.handleDownloadProject)
			r.Get("/projects/{id}/profiles", s.handleProjectProfiles)
			r.Post("/projects/{id}/deploy", s.handleDeployProject)
			r.Post("/projects/{id}/down", s.handleDownProject)
			r.Post("/projects/{id}/restart", s.handleRestartProject)

			r.Get("/images", s.handleListImages)
			r.Get("/images/history", s.handleImageHistory)
			r.Get("/images/save", s.handleSaveImage)
			r.Post("/images/load", s.handleLoadImage)
			r.Post("/images/import", s.handleImportImage)
			r.Post("/images/tag", s.handleTagImage)
			r.Post("/images/build", s.handleBuildImage)
			// ref is a query param, not a path segment: image references contain
			// ':' and '/' (e.g. sha256:… or registry/owner/app:tag) which do not
			// round-trip cleanly through path matching/decoding.
			r.Delete("/images", s.handleRemoveImage)
			r.Post("/images/prune", s.handlePruneImages)

			// Registry credentials (secrets encrypted at rest).
			r.Get("/registries", s.handleListRegistries)
			r.Post("/registries", s.handleCreateRegistry)
			r.Delete("/registries/{id}", s.handleDeleteRegistry)
			r.Post("/registries/{id}/test", s.handleTestRegistry)

			// Generic raw inspect for any object kind (id/ref via query param).
			r.Get("/inspect/{kind}", s.handleInspect)

			r.Get("/networks", s.handleListNetworks)
			r.Delete("/networks/{id}", s.handleRemoveNetwork)
			r.Get("/topology", s.handleTopology)

			r.Get("/volumes", s.handleListVolumes)
			r.Post("/volumes", s.handleCreateVolume)
			r.Post("/volumes/prune", s.handlePruneVolumes)
			r.Delete("/volumes/{name}", s.handleRemoveVolume)
			// Volume file browser (via a throwaway helper container).
			r.Get("/volumes/{name}/files", s.handleListVolumeFiles)
			r.Get("/volumes/{name}/files/download", s.handleDownloadVolumeFile)
			r.Post("/volumes/{name}/files/upload", s.handleUploadVolumeFile)
			r.Post("/volumes/{name}/files/mkdir", s.handleMakeVolumeDir)
			r.Post("/volumes/{name}/files/extract", s.handleExtractVolumeFile)
			r.Delete("/volumes/{name}/files", s.handleDeleteVolumeFile)
			r.Delete("/volumes/{name}/browse", s.handleCloseVolumeBrowser)
			r.Get("/version", s.handleVersion)
			r.Get("/prefs", s.handleGetPrefs)
			r.Put("/prefs", s.handleSetPrefs)
			r.Get("/system", s.handleSystemInfo)
			r.Get("/system/df", s.handleDiskUsage)
			r.Get("/stats/overview", s.handleStatsOverview)
			r.Get("/stats/ports", s.handleHostPorts)
			r.Get("/metrics/history", s.handleMetricsHistory)
			r.Get("/audit", s.handleAudit)

			// Alerting: webhooks, rules, and the in-app event feed.
			r.Get("/webhooks", s.handleListWebhooks)
			r.Post("/webhooks", s.handleCreateWebhook)
			r.Delete("/webhooks/{id}", s.handleDeleteWebhook)
			r.Get("/alert-rules", s.handleListAlertRules)
			r.Post("/alert-rules", s.handleCreateAlertRule)
			r.Put("/alert-rules/{id}", s.handleUpdateAlertRule)
			r.Patch("/alert-rules/{id}", s.handleToggleAlertRule)
			r.Delete("/alert-rules/{id}", s.handleDeleteAlertRule)
			r.Get("/alerts", s.handleListAlertEvents)
			r.Post("/alerts/{id}/ack", s.handleAckAlertEvent)
			// Saved log parsing rules (applied client-side in the Logs view).
			r.Get("/parse-rules", s.handleListParseRules)
			r.Post("/parse-rules", s.handleCreateParseRule)
			r.Delete("/parse-rules/{id}", s.handleDeleteParseRule)
			// Email (SMTP) alert channel config.
			r.Get("/smtp", s.handleGetSMTP)
			r.Put("/smtp", s.handleSetSMTP)
			r.Post("/smtp/test", s.handleTestSMTP)

			// WebSocket for live stats/logs.
			r.Get("/ws", s.handleWebSocket)
			// WebSocket for an interactive container shell (exec TTY).
			r.Get("/containers/{id}/exec", s.handleExec)
			// WebSocket streaming image pull / push progress.
			r.Get("/images/pull", s.handlePullImage)
			r.Get("/images/push", s.handlePushImage)
			// WebSocket streaming live Docker daemon events.
			r.Get("/events", s.handleEvents)
		})
	})

	// Unauthenticated health probe (LB / uptime / k8s); /health is an alias.
	r.Get("/healthz", s.handleHealthz)
	r.Get("/health", s.handleHealthz)

	// Prometheus exporter (own optional-token auth; Prometheus can't do cookies).
	r.Get("/metrics", s.handleMetrics)

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
