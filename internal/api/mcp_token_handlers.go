package api

import (
	"crypto/sha256"
	"encoding/hex"
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
	ReadOnly   bool     `json:"readOnly"`
	CreatedAt  string   `json:"createdAt"`
	LastUsedAt string   `json:"lastUsedAt,omitempty"`
	ExpiresAt  string   `json:"expiresAt,omitempty"`
}

func toMCPTokenJSON(t store.APIToken) mcpTokenJSON {
	j := mcpTokenJSON{
		ID: t.ID, Name: t.Name, Sections: t.Sections, ReadOnly: t.ReadOnly,
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
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": s.cfg.MCPEnabled,
		"oauth":   s.cfg.MCPEnabled && s.cfg.MCPPublicURL != "",
	})
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
		ExpiresInDays int      `json:"expiresInDays"`
	}
	_ = decodeJSON(r, &b)
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}

	// Section narrowing: a token may only reference sections the owner actually
	// has (admins may use any valid section). Empty = inherit all of the owner's.
	sections := cleanSections(b.Sections)
	if !u.IsAdmin() {
		// Filter in place. Safe: cleanSections returns a fresh slice (not aliasing
		// u.Sections/b.Sections) and the write index never outruns the read index.
		allowed := sections[:0]
		for _, sec := range sections {
			if contains(u.Sections, sec) {
				allowed = append(allowed, sec)
			}
		}
		sections = allowed
	}
	// A read-only owner can only mint read-only tokens.
	readOnly := b.ReadOnly || u.ReadOnly

	var expiresAt time.Time
	if b.ExpiresInDays > 0 {
		expiresAt = time.Now().Add(time.Duration(b.ExpiresInDays) * 24 * time.Hour)
	}

	secret := randToken(32)
	sum := sha256.Sum256([]byte(secret))
	id, err := s.store.CreateAPIToken(r.Context(), &store.APIToken{
		UserID: u.ID, TokenHash: hex.EncodeToString(sum[:]), Name: b.Name,
		Sections: sections, ReadOnly: readOnly, ExpiresAt: expiresAt,
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
