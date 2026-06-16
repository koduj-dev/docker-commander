package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// sectionForPath maps an API path to its access-control section. It returns:
//   - ""        : ungated (any authenticated user) — shared reads, auth, ws
//   - "__admin" : admin-only (user & settings management)
//   - a section : one of store.Sections, subject to per-user + global gating
func sectionForPath(path string) string {
	p := strings.TrimPrefix(path, "/api/")
	seg := p
	if i := strings.IndexByte(p, '/'); i >= 0 {
		seg = p[:i]
	}
	switch seg {
	case "containers", "stacks":
		return "containers"
	case "projects", "project-templates", "service-blocks", "compose-fragments":
		return "projects"
	case "images":
		return "images"
	case "volumes":
		return "volumes"
	case "networks":
		return "networks"
	case "topology":
		return "topology"
	case "events":
		return "events"
	case "parse-rules":
		return "logs"
	case "alerts", "alert-rules", "webhooks", "smtp":
		return "alerts"
	case "hosts":
		return "hosts"
	case "registries":
		return "registries"
	case "audit":
		return "audit"
	case "users", "settings", "ldap", "update", "mcp-admin":
		return "__admin"
	default:
		// auth, system, inspect, metrics, ws, … are not section-gated.
		return ""
	}
}

// wsChannelSection maps a known WebSocket stream channel to the RBAC section
// that gates it, and reports whether the channel is recognised. The hub only
// streams container-scoped telemetry — a named container's stats or logs — and
// every consumer (the container detail view and the Logs page alike) must
// already hold the "containers" section to obtain a container id in the first
// place, so both known channels gate on "containers". An UNKNOWN channel
// returns ok=false so the caller fails closed (denies) rather than authorising a
// future channel by accident. Previously the hub was ungated, so any
// authenticated user could stream any container's data; this ties it to RBAC.
func wsChannelSection(channel string) (section string, ok bool) {
	switch channel {
	case "stats", "logs":
		return "containers", true
	default:
		return "", false
	}
}

// isWriteRequest reports whether a request performs a mutating action. Most are
// POST/PUT/PATCH/DELETE, but a few writes ride GET WebSocket upgrades.
func isWriteRequest(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	// A few GETs are effectively privileged actions: WebSocket exec, pull/push,
	// and a vulnerability scan (spawns a heavy subprocess + outbound calls), so
	// they need write access — a read-only account must not trigger them.
	for _, suffix := range []string{"/exec", "/pull", "/push", "/scan"} {
		if strings.HasSuffix(r.URL.Path, suffix) {
			return true
		}
	}
	return false
}

// checkAccess is the shared RBAC gate: it decides whether user u may act on
// section with the given write intent. A nil result means allowed; a non-nil
// error describes the denial (always a 403 at the HTTP layer). Both the REST
// permissions middleware and the MCP tool dispatcher route through here, so
// there is exactly one source of truth for section grants and the read-only
// flag — disable a section in the admin UI and the matching MCP tool dies too.
func (s *Server) checkAccess(ctx context.Context, u *store.User, section string, write bool) error {
	if section == "" {
		return nil // ungated
	}
	if section == "__admin" {
		if !u.IsAdmin() {
			return errors.New("admin only")
		}
		return nil
	}
	if u.IsAdmin() {
		return nil // admins bypass section + read-only checks
	}
	disabled, _ := s.store.DisabledSections(ctx)
	if contains(disabled, section) || !contains(u.Sections, section) {
		return errors.New("access to this section is not permitted")
	}
	if u.ReadOnly && write {
		return errors.New("your account is read-only")
	}
	return nil
}

// permissions enforces role, per-user section grants, the read-only flag and
// global feature flags. It runs after RequireSession (claims are present).
func (s *Server) permissions(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		section := sectionForPath(r.URL.Path)
		if section == "" {
			next.ServeHTTP(w, r) // ungated
			return
		}
		claims, ok := auth.ClaimsFrom(r.Context())
		if !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		u, err := s.store.UserByID(r.Context(), claims.UserID)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err := s.checkAccess(r.Context(), u, section, isWriteRequest(r)); err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		next.ServeHTTP(w, r)
	})
}
