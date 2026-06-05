package api

import (
	"net/http"
	"strings"

	"github.com/koduj-dev/docker-commander/internal/auth"
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
	case "users", "settings", "ldap":
		return "__admin"
	default:
		// auth, system, inspect, metrics, ws, … are not section-gated.
		return ""
	}
}

// isWriteRequest reports whether a request performs a mutating action. Most are
// POST/PUT/PATCH/DELETE, but a few writes ride GET WebSocket upgrades.
func isWriteRequest(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	for _, suffix := range []string{"/exec", "/pull", "/push"} {
		if strings.HasSuffix(r.URL.Path, suffix) {
			return true
		}
	}
	return false
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

		if section == "__admin" {
			if !u.IsAdmin() {
				writeErr(w, http.StatusForbidden, "admin only")
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// Admins bypass section + read-only checks.
		if !u.IsAdmin() {
			disabled, _ := s.store.DisabledSections(r.Context())
			if contains(disabled, section) || !contains(u.Sections, section) {
				writeErr(w, http.StatusForbidden, "access to this section is not permitted")
				return
			}
			if u.ReadOnly && isWriteRequest(r) {
				writeErr(w, http.StatusForbidden, "your account is read-only")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
