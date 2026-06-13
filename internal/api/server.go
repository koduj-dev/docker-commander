// Package api wires the HTTP surface together: REST endpoints for auth and
// Docker operations, the WebSocket upgrade, and serving the embedded SPA.
package api

import (
	"context"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/history"
	"github.com/koduj-dev/docker-commander/internal/mcp"
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

	// mcpSigningKey signs MCP OAuth access tokens; lazily loaded by mountMCP.
	mcpSigningKey []byte
	// mcpRateLimiter throttles the unauthenticated OAuth endpoints (DCR + token).
	mcpRateLimiter *auth.LoginLimiter
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
			r.Post("/projects/{id}/validate", s.handleValidateProject)
			r.Post("/projects/{id}/resolve", s.handleResolveProject)
			r.Post("/projects/{id}/summary", s.handleProjectSummary)
			r.Post("/projects/{id}/dockerfile-check", s.handleCheckDockerfile)
			r.Post("/projects/{id}/deploy", s.handleDeployProject)
			r.Post("/projects/{id}/down", s.handleDownProject)
			r.Post("/projects/{id}/restart", s.handleRestartProject)

			// Project templates (presets) + builder service blocks. Built-in
			// ones are embedded; user-saved ones live in the store.
			r.Get("/project-templates", s.handleListProjectTemplates)
			r.Post("/project-templates", s.handleCreateProjectTemplate)
			r.Post("/project-templates/preview", s.handlePreviewTemplate)
			r.Post("/project-templates/{id}/duplicate", s.handleDuplicateProjectTemplate)
			r.Get("/project-templates/{id}", s.handleGetProjectTemplate)
			r.Put("/project-templates/{id}", s.handleUpdateProjectTemplate)
			r.Delete("/project-templates/{id}", s.handleDeleteProjectTemplate)
			r.Get("/project-templates/{id}/files", s.handleListTemplateFiles)
			r.Put("/project-templates/{id}/files", s.handleWriteTemplateFile)
			r.Post("/project-templates/{id}/files/raw", s.handleUploadTemplateFileRaw)
			r.Get("/project-templates/{id}/files/raw", s.handleDownloadTemplateFile)
			r.Delete("/project-templates/{id}/files", s.handleDeleteTemplateFile)
			r.Post("/project-templates/{id}/files/dir", s.handleMakeTemplateDir)
			r.Get("/project-templates/{id}/download", s.handleDownloadTemplate)
			r.Get("/service-blocks", s.handleListServiceBlocks)
			r.Post("/service-blocks", s.handleCreateServiceBlock)
			r.Get("/service-blocks/{id}", s.handleGetServiceBlock)
			r.Put("/service-blocks/{id}", s.handleUpdateServiceBlock)
			r.Post("/service-blocks/{id}/duplicate", s.handleDuplicateServiceBlock)
			r.Delete("/service-blocks/{id}", s.handleDeleteServiceBlock)
			r.Get("/compose-fragments", s.handleListComposeFragments)
			r.Post("/compose-fragments", s.handleCreateComposeFragment)
			r.Get("/compose-fragments/{id}", s.handleGetComposeFragment)
			r.Put("/compose-fragments/{id}", s.handleUpdateComposeFragment)
			r.Post("/compose-fragments/{id}/duplicate", s.handleDuplicateComposeFragment)
			r.Delete("/compose-fragments/{id}", s.handleDeleteComposeFragment)

			r.Get("/images", s.handleListImages)
			r.Get("/images/search", s.handleSearchImages)
			r.Get("/images/tags", s.handleImageTags)
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
			r.Post("/networks", s.handleCreateNetwork)
			r.Post("/networks/prune", s.handlePruneNetworks)
			r.Delete("/networks/{id}", s.handleRemoveNetwork)
			r.Post("/networks/{id}/connect", s.handleConnectNetwork)
			r.Post("/networks/{id}/disconnect", s.handleDisconnectNetwork)
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

	// Remote MCP server. Mounted ONLY when explicitly enabled — when off, these
	// paths fall through to the SPA/404 with no hint the feature exists. The
	// transport is bearer-gated and tools share the REST RBAC gate (checkAccess).
	if s.cfg.MCPEnabled {
		s.mountMCP(r)
	}

	// Everything else serves the embedded single-page app (if present).
	if s.webFS != nil {
		r.Handle("/*", s.spaHandler())
	}
	return r
}

// mountMCP wires the bearer-gated MCP transport and its OAuth protected-resource
// metadata. The public base URL (DC_MCP_PUBLIC_URL) is only needed for the OAuth
// discovery flow; bearer (API-token) clients work without it.
func (s *Server) mountMCP(r chi.Router) {
	base := strings.TrimRight(s.cfg.MCPPublicURL, "/")
	deps := mcp.Deps{
		Store:         s.store,
		Docker:        s.docker,
		History:       s.history,
		CheckAccess:   s.checkAccess,
		Version:       s.cfg.Version,
		ListProjects:  s.mcpListProjects,
		DeployProject: s.mcpDeployProject,
		DownProject:   s.mcpDownProject,
	}
	// OAuth (for interactive clients like Claude Desktop) needs a public URL for
	// audience binding + discovery. Without one, only bearer/API-token auth works.
	if base != "" {
		s.mcpSigningKey = s.mcpSecret(context.Background())
		deps.ResourceURL = base + "/mcp"
		deps.MetadataURL = base + "/.well-known/oauth-protected-resource"
		deps.IssuerURL = base
		deps.SigningKey = s.mcpSigningKey
	}
	mcpHandler, metadataHandler := deps.Handlers()
	r.Handle("/mcp", mcpHandler)
	if base != "" {
		// Throttle the unauthenticated programmatic endpoints (open DCR + token).
		s.mcpRateLimiter = auth.NewLoginLimiter(30, time.Minute)
		// Serve discovery at both the root and the resource-path-suffixed form;
		// strict clients probe the path-aware variant for a resource at /mcp.
		r.Handle("/.well-known/oauth-protected-resource", metadataHandler)
		r.Handle("/.well-known/oauth-protected-resource/mcp", metadataHandler)
		r.Get("/.well-known/oauth-authorization-server", s.handleOAuthASMetadata)
		r.Get("/.well-known/oauth-authorization-server/mcp", s.handleOAuthASMetadata)
		r.Post("/oauth/register", s.oauthThrottle(s.handleOAuthRegister))
		r.Get("/oauth/authorize", s.handleOAuthAuthorize)
		r.Post("/oauth/authorize", s.handleOAuthAuthorizeDecision)
		r.Post("/oauth/token", s.oauthThrottle(s.handleOAuthToken))
		s.startOAuthSweeper()
	}
}

// oauthThrottle rate-limits an unauthenticated OAuth endpoint per client IP
// (RealIP-normalized), failing closed with 429 once the per-minute budget is hit.
func (s *Server) oauthThrottle(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.mcpRateLimiter != nil {
			if !s.mcpRateLimiter.Allow(r.RemoteAddr) {
				w.Header().Set("Retry-After", "60")
				writeErr(w, http.StatusTooManyRequests, "rate limited")
				return
			}
			s.mcpRateLimiter.Fail(r.RemoteAddr) // count this request toward the window
		}
		next(w, r)
	}
}

// startOAuthSweeper periodically purges expired OAuth codes/refresh tokens so
// the tables don't grow unbounded from issued-but-unredeemed grants.
func (s *Server) startOAuthSweeper() {
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = s.store.DeleteExpiredOAuth(ctx)
			cancel()
		}
	}()
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
