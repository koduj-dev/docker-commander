package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
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
	case "alerts", "alert-rules", "webhooks":
		return "alerts"
	case "smtp":
		// The SMTP config is a single INSTANCE-WIDE outbound mail relay with a
		// stored credential — the same category as the LDAP bind password, and it
		// is growing beyond alerting into system notifications. It used to sit
		// under "alerts", which let any non-admin holding that section repoint the
		// whole instance's mail (and thus receive its notifications).
		return "__admin"
	case "hosts":
		return "hosts"
	case "registries":
		return "registries"
	case "audit":
		return "audit"
	case "users", "roles", "settings", "ldap", "update", "mcp-admin":
		return "__admin"
	case "inspect":
		// /api/inspect/{kind} returns the RAW docker inspect payload, which for a
		// container includes Config.Env — database passwords, API keys. It must be
		// gated by whichever section owns the kind being inspected, not left open
		// to any signed-in account.
		return sectionForInspectKind(p)
	default:
		// auth, system, metrics, ws, … are not section-gated.
		return ""
	}
}

// sectionForInspectKind maps "inspect/{kind}" to the section that owns that kind.
// An unrecognised kind falls back to "containers", the most privileged of them, so
// a new kind fails closed rather than becoming readable by everyone.
func sectionForInspectKind(p string) string {
	kind := ""
	if i := strings.IndexByte(p, '/'); i >= 0 {
		kind = p[i+1:]
		if j := strings.IndexByte(kind, '/'); j >= 0 {
			kind = kind[:j]
		}
	}
	switch kind {
	case "image":
		return "images"
	case "volume":
		return "volumes"
	case "network":
		return "networks"
	default:
		// "container" and anything unknown.
		return "containers"
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
// section with the given write intent, on the Docker host identified by hostID
// (0 = the local daemon, which is always in scope). A nil result means allowed; a non-nil
// error describes the denial (always a 403 at the HTTP layer). Both the REST
// permissions middleware and the MCP tool dispatcher route through here, so
// there is exactly one source of truth for section grants and the read-only
// flag — disable a section in the admin UI and the matching MCP tool dies too.
func (s *Server) checkAccess(ctx context.Context, u *store.User, section string, write bool, hostID int64) error {
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
	// Effective grants are the union of the user's assigned roles and their own
	// per-user section list, capped by the account's read-only flag, with
	// app-wide disabled sections removed. A user with no roles behaves exactly as
	// before roles existed.
	grants, err := s.store.EffectiveGrants(ctx, u)
	if err != nil {
		// Fail closed: if we can't establish what this user may do, deny. An
		// error here is a store problem, not a grant.
		return errors.New("could not determine your permissions")
	}
	g, ok := grants[section]
	if !ok || !g.Granted {
		return errors.New("access to this section is not permitted")
	}
	if write && !g.Write {
		if u.ReadOnly {
			return errors.New("your account is read-only")
		}
		return errors.New("your access to this section is read-only")
	}
	// Host scope. Until now a grant reached every daemon: holding "containers"
	// let you act on any host by passing ?host=N, including hosts you couldn't
	// see on the Hosts page. A role may now be limited to specific hosts, and an
	// unscoped grant still reaches all of them so nobody's reach changed on
	// upgrade.
	if !g.HasHost(hostID) {
		return errors.New("your access to this section does not include that host")
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
		// The host is authorised HERE rather than at the ~60 resolveHostID call
		// sites. Every host-targeting route funnels through this middleware, so a
		// route added later is covered without anyone remembering to check — the
		// failure mode that left ?host= unauthorised in the first place.
		hostID, err := hostParam(r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid host id")
			return
		}
		if err := s.checkAccess(r.Context(), u, section, isWriteRequest(r), hostID); err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostParam reads the "host" query parameter, the one way a REST request names a
// Docker host. Absent means 0 — the local daemon.
func hostParam(r *http.Request) (int64, error) {
	q := r.URL.Query().Get("host")
	if q == "" {
		return 0, nil
	}
	return strconv.ParseInt(q, 10, 64)
}
