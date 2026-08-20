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
	case "diagnostics":
		return "diagnostics"
	case "users", "roles", "settings", "ldap", "update", "mcp-admin":
		return "__admin"
	case "stats":
		// The dashboard's own data: /stats/overview enumerates every running
		// container with its resource usage, and /stats/ports enumerates every
		// published port AND actively connects to each one to fingerprint it.
		// Both were ungated "for the shell", which meant an account with no
		// sections at all could inventory a host and make it dial its own ports.
		return "dashboard"
	case "system":
		// /api/system is version/health for the shell (ungated), but
		// /api/system/df is the dashboard's disk-usage breakdown.
		if strings.HasPrefix(p, "system/df") {
			return "dashboard"
		}
		return ""
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
	// /stats/ports is a GET that opens a TCP connection to every published port
	// on the host and fingerprints what answers — an active network action, the
	// same category as /scan, so a read-only account must not be able to launch it.
	for _, suffix := range []string{"/exec", "/pull", "/push", "/scan", "/stats/ports", "/bulk-pull"} {
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
	if !g.HasHost(s.store.NormalizeHostID(ctx, hostID)) {
		return errors.New("your access to this section does not include that host")
	}
	return nil
}

// permissions enforces role, per-user section grants, the read-only flag and
// global feature flags. It runs after RequireSession (claims are present).
func (s *Server) permissions(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		section := sectionForPath(r.URL.Path)
		hostID, hostErr := hostParam(r)
		if section == "" {
			// An ungated route can still NAME a host: /api/stats/overview,
			// /api/system/df and /api/stats/ports are dashboard reads that take
			// ?host=. They belong to no section, so there is nothing to check them
			// against — which is exactly how they slipped past the phase-2 gate and
			// leaked another host's counts, disk usage and published ports. Ungated
			// means "no section required", not "any host you like", so a named host
			// must at least be one the caller's grants reach somewhere.
			if hostErr != nil {
				writeErr(w, http.StatusBadRequest, "invalid host id")
				return
			}
			if hostID != 0 && !s.callerCanReachHost(r, hostID) {
				writeErr(w, http.StatusForbidden, "your access does not include that host")
				return
			}
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
		if hostErr != nil {
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
//
// Anything <= 0 normalises to 0, matching docker.Manager.Client, which resolves a
// non-positive id to the local daemon. Without that, ?host=-1 would be served by
// the local daemon while being authorised (and audited) as host -1: a scoped user
// would be refused something they are in fact allowed, and the audit log would
// name a host that doesn't exist.
func hostParam(r *http.Request) (int64, error) {
	q := r.URL.Query().Get("host")
	if q == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(q, 10, 64)
	if err != nil {
		return 0, err
	}
	if id < 0 {
		return 0, nil
	}
	return id, nil
}

// callerCanReachHost reports whether the signed-in caller may see anything on
// hostID. Used for the routes that name a host but belong to no section, where
// there is no section grant to consult. Fails CLOSED: an unidentifiable caller or
// a store error denies rather than falling through to the handler.
func (s *Server) callerCanReachHost(r *http.Request, hostID int64) bool {
	claims, ok := auth.ClaimsFrom(r.Context())
	if !ok {
		return false
	}
	u, err := s.store.UserByID(r.Context(), claims.UserID)
	if err != nil {
		return false
	}
	if u.IsAdmin() {
		return true
	}
	ok, err = s.store.CanReachHost(r.Context(), u, hostID)
	return err == nil && ok
}

// visibleHosts returns a predicate for filtering aggregate views to the hosts the
// caller may see. Everything about host scoping is enforcement — this is the
// other half, hiding what the caller can't act on so a list doesn't advertise
// names, images and ports from a host they were deliberately scoped away from.
//
// It fails CLOSED in a specific sense: if the caller can't be identified or their
// grants can't be computed, only the local daemon is visible. That degrades an
// aggregate view rather than leaking one.
func (s *Server) visibleHosts(r *http.Request) func(hostID int64) bool {
	local := func(hostID int64) bool { return s.store.NormalizeHostID(r.Context(), hostID) == 0 }
	claims, ok := auth.ClaimsFrom(r.Context())
	if !ok {
		return local
	}
	u, err := s.store.UserByID(r.Context(), claims.UserID)
	if err != nil {
		return local
	}
	if u.IsAdmin() {
		return func(int64) bool { return true }
	}
	hosts, all, err := s.store.ReachableHosts(r.Context(), u)
	if err != nil {
		return local
	}
	if all {
		return func(int64) bool { return true }
	}
	return func(hostID int64) bool {
		return s.store.NormalizeHostID(r.Context(), hostID) == 0 || hosts[hostID]
	}
}

// visibleHostIDs is visibleHosts as a set rather than a predicate, for callers
// that must push the scope into a SQL query instead of filtering afterwards.
// The second return is true when every host is allowed, in which case the slice
// is meaningless.
//
// Paging is why this exists: filtering a page after fetching it silently
// produces short pages and a total that counts rows the caller may not see.
func (s *Server) visibleHostIDs(r *http.Request) ([]int64, bool) {
	ctx := r.Context()
	// The local daemon is always in scope, and events record its real row id
	// rather than 0, so resolve that id and include it explicitly.
	localOnly := func() []int64 {
		ids := []int64{0}
		if hosts, err := s.store.ListHosts(ctx); err == nil {
			for _, h := range hosts {
				if h.Kind == "local" {
					ids = append(ids, h.ID)
				}
			}
		}
		return ids
	}

	claims, ok := auth.ClaimsFrom(ctx)
	if !ok {
		return localOnly(), false
	}
	u, err := s.store.UserByID(ctx, claims.UserID)
	if err != nil {
		return localOnly(), false
	}
	if u.IsAdmin() {
		return nil, true
	}
	hosts, all, err := s.store.ReachableHosts(ctx, u)
	if err != nil {
		return localOnly(), false
	}
	if all {
		return nil, true
	}
	ids := localOnly()
	for id := range hosts {
		ids = append(ids, id)
	}
	return ids, false
}
