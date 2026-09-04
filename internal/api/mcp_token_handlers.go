package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// MCP access tokens are self-service: any authenticated user manages their OWN
// tokens. A token can only NARROW the owner's rights (a subset of their sections
// + a read-only flag) and is re-checked against live RBAC on every MCP call, so
// minting one grants no escalation. All handlers scope strictly to the caller.

type mcpTokenJSON struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Sections   []string `json:"sections"`
	HostIDs    []int64  `json:"hostIds"`
	ReadOnly   bool     `json:"readOnly"`
	CreatedAt  string   `json:"createdAt"`
	LastUsedAt string   `json:"lastUsedAt,omitempty"`
	ExpiresAt  string   `json:"expiresAt,omitempty"`
}

func toMCPTokenJSON(t store.APIToken) mcpTokenJSON {
	j := mcpTokenJSON{
		ID: t.ID, Name: t.Name, Sections: t.Sections, HostIDs: t.HostIDs, ReadOnly: t.ReadOnly,
		CreatedAt: t.CreatedAt.Format(time.RFC3339),
	}
	if !t.LastUsedAt.IsZero() {
		j.LastUsedAt = t.LastUsedAt.Format(time.RFC3339)
	}
	if !t.ExpiresAt.IsZero() {
		j.ExpiresAt = t.ExpiresAt.Format(time.RFC3339)
	}
	return j
}

// handleMCPStatus reports whether the MCP server is enabled and whether the
// OAuth flow is available (needs a public URL), so the UI can guide the user.
func (s *Server) handleMCPStatus(w http.ResponseWriter, r *http.Request) {
	// The token policy rides along so the creation form can offer only lifetimes
	// that will actually be accepted. That is a courtesy, not the enforcement —
	// handleCreateMCPToken re-checks the policy, because a form is not a boundary.
	policy, err := s.store.MCPTokenPolicy(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not read the token policy")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":     s.cfg.MCPEnabled,
		"oauth":       s.cfg.MCPEnabled && s.cfg.MCPPublicURL != "",
		"tokenPolicy": policy,
	})
}

// handleAdminGetMCPTokenPolicy returns the token lifetime policy. Admin only.
func (s *Server) handleAdminGetMCPTokenPolicy(w http.ResponseWriter, r *http.Request) {
	policy, err := s.store.MCPTokenPolicy(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not read the token policy")
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

// handleAdminSetMCPTokenPolicy updates the token lifetime policy. Admin only:
// loosening it — a longer ceiling, or re-enabling never-expiring tokens — is a
// decision about how much credential risk the installation carries, which is not
// something a token holder gets to make for themselves.
func (s *Server) handleAdminSetMCPTokenPolicy(w http.ResponseWriter, r *http.Request) {
	var p store.MCPTokenPolicy
	if err := decodeJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.store.SetMCPTokenPolicy(r.Context(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save the token policy")
		return
	}
	// Read back rather than echoing the request: the store normalises
	// contradictory combinations, and the admin should see what is actually in
	// force, not what they typed.
	saved, err := s.store.MCPTokenPolicy(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not read the token policy")
		return
	}
	s.audit(r, "mcp.token_policy.update", "", fmt.Sprintf(
		"default %dd, max %dd, never-expiring %v", saved.DefaultDays, saved.MaxDays, saved.AllowUnlimited))
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleListMCPTokens(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	toks, err := s.store.ListAPITokens(r.Context(), c.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list tokens")
		return
	}
	out := []mcpTokenJSON{}
	for _, t := range toks {
		if t.Revoked {
			continue
		}
		out = append(out, toMCPTokenJSON(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateMCPToken(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	u, err := s.store.UserByID(r.Context(), c.UserID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var b struct {
		Name          string   `json:"name"`
		ReadOnly      bool     `json:"readOnly"`
		Sections      []string `json:"sections"`
		HostIDs       []int64  `json:"hostIds"`
		ExpiresInDays int      `json:"expiresInDays"`
		// NeverExpires is separate from ExpiresInDays==0 deliberately: zero means
		// "I did not choose", and the safe reading of silence is the policy
		// default, not a credential that outlives everyone who remembers it.
		NeverExpires bool `json:"neverExpires"`
	}
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}

	// Section narrowing: a token may only reference sections the owner actually
	// has (admins may use any valid section). Empty = inherit all of the owner's.
	requested := cleanSections(b.Sections)
	sections := requested
	if !u.IsAdmin() {
		// Effective sections, not u.Sections: access may come from an assigned
		// role, and a token must be scopeable to those too.
		effective, err := s.store.EffectiveSections(r.Context(), u)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not determine your permissions")
			return
		}
		// Filter in place. Safe: cleanSections returns a fresh slice (not aliasing
		// b.Sections) and the write index never outruns the read index.
		allowed := sections[:0]
		for _, sec := range sections {
			if contains(effective, sec) {
				allowed = append(allowed, sec)
			}
		}
		sections = allowed
	}
	// An explicit scope that filters down to nothing must NOT fall through to
	// "empty = inherit everything" — the caller asked to narrow, and silently
	// handing back an unrestricted token would widen their reach instead.
	if len(requested) > 0 && len(sections) == 0 {
		writeErr(w, http.StatusBadRequest,
			"none of the requested sections are granted to your account, so this token would not be scoped to anything")
		return
	}
	// Host narrowing. Unlike sections this isn't filtered against the owner's
	// reach, because it doesn't need to be: the live user check runs on every
	// call, so a token naming a host its owner can't touch simply can't use it.
	// What we do enforce is that an explicit list is a real list of ids — an
	// entry of 0 would mean "the local daemon", which is always in scope and
	// would quietly turn a narrowing list into a no-op for that host.
	hostIDs := cleanHostIDs(b.HostIDs)
	if len(b.HostIDs) > 0 && len(hostIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "the requested host scope contains no valid host ids")
		return
	}
	// A read-only owner can only mint read-only tokens.
	readOnly := b.ReadOnly || u.ReadOnly

	// Lifetime is the administrator's call, not the token holder's. The policy
	// decides the default, the ceiling, and whether "never" is on the menu at all;
	// the UI reflects it, and this check is what actually enforces it, because the
	// UI is not a security boundary.
	policy, err := s.store.MCPTokenPolicy(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not read the token policy")
		return
	}
	expiresAt, err := policy.ResolveExpiry(b.ExpiresInDays, b.NeverExpires, time.Now())
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	secret := randToken(32)
	sum := sha256.Sum256([]byte(secret))
	id, err := s.store.CreateAPIToken(r.Context(), &store.APIToken{
		UserID: u.ID, TokenHash: hex.EncodeToString(sum[:]), Name: b.Name,
		Sections: sections, HostIDs: hostIDs, ReadOnly: readOnly, ExpiresAt: expiresAt,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create token")
		return
	}
	s.audit(r, "mcp.token.create", strconv.FormatInt(id, 10), b.Name)

	// The secret is returned ONCE here and never again — only its hash is stored.
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":       id,
		"token":    secret,
		"name":     b.Name,
		"sections": sections,
		"readOnly": readOnly,
	})
}

func (s *Server) handleRevokeMCPToken(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	revoked, err := s.store.RevokeAPIToken(r.Context(), id, c.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not revoke token")
		return
	}
	if !revoked {
		writeErr(w, http.StatusNotFound, "token not found")
		return
	}
	s.audit(r, "mcp.token.revoke", chi.URLParam(r, "id"), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// mcpSessionJSON is one connector session — an OAuth authorization grant plus
// the refresh-token chain it started. The session id itself is never
// returned as anything sensitive (it isn't a bearer credential, just a
// revoke-by-id lookup key), but it also serves no purpose in the UI, so it is
// omitted like the API-token secret is.
type mcpSessionJSON struct {
	ID         string `json:"id"`
	ClientID   string `json:"clientId"`
	ClientName string `json:"clientName"`
	IP         string `json:"ip"`
	UserAgent  string `json:"userAgent"`
	CreatedAt  string `json:"createdAt"`
	LastUsedAt string `json:"lastUsedAt"`
	ExpiresAt  string `json:"expiresAt"`
}

// clientNameLookup resolves client ids to display names in one pass, falling
// back to the raw id for a client that was since deleted (its sessions are
// purged with it, so in practice this is defensive, not a real path).
func (s *Server) clientNameLookup(ctx context.Context) map[string]string {
	clients, err := s.store.ListOAuthClients(ctx)
	names := make(map[string]string, len(clients))
	if err == nil {
		for _, c := range clients {
			names[c.ID] = c.Name
		}
	}
	return names
}

func toMCPSessionJSON(sess store.MCPOAuthSession, clientNames map[string]string) mcpSessionJSON {
	name := clientNames[sess.ClientID]
	if name == "" {
		name = sess.ClientID
	}
	return mcpSessionJSON{
		ID: sess.ID, ClientID: sess.ClientID, ClientName: name,
		IP: sess.IP, UserAgent: sess.UserAgent,
		CreatedAt: sess.CreatedAt.Format(time.RFC3339), LastUsedAt: sess.LastUsedAt.Format(time.RFC3339),
		ExpiresAt: sess.ExpiresAt.Format(time.RFC3339),
	}
}

// handleListMCPSessions returns the caller's own connector sessions.
func (s *Server) handleListMCPSessions(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	sessions, err := s.store.ListMCPOAuthSessions(r.Context(), c.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list sessions")
		return
	}
	names := s.clientNameLookup(r.Context())
	out := make([]mcpSessionJSON, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, toMCPSessionJSON(sess, names))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRevokeMCPSession revokes one of the caller's own sessions — both the
// session and its current refresh token — without touching the client's other
// sessions or de-registering the client itself.
func (s *Server) handleRevokeMCPSession(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	id := chi.URLParam(r, "id")
	// Scoped by user id in the store, so another account's session id — even if
	// somehow known — revokes nothing and reads as missing.
	if err := s.store.RevokeMCPOAuthSession(r.Context(), id, c.UserID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "session not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not revoke session")
		return
	}
	s.audit(r, "mcp.session.revoke", id, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// cleanHostIDs drops non-positive and duplicate host ids. 0 is dropped rather
// than kept: the local daemon is always in scope, so listing it would not narrow
// anything and would misrepresent the token's reach in the UI.
func cleanHostIDs(in []int64) []int64 {
	out := make([]int64, 0, len(in))
	seen := map[int64]bool{}
	for _, id := range in {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
