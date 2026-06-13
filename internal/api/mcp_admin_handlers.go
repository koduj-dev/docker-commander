package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// MCP admin overview: a fleet-wide view of MCP credentials for administrators.
// Unlike the self-service handlers in mcp_token_handlers.go (each user manages
// only their own tokens), these list EVERY user's tokens and all registered
// OAuth clients, and can revoke/delete any of them. They are mounted under
// /api/mcp-admin/… which sectionForPath maps to "__admin", so the permissions
// middleware rejects non-admins with 403 before any handler runs. Secrets are
// never exposed here — only token hashes/metadata and public OAuth client rows.

type adminMCPTokenJSON struct {
	ID         int64    `json:"id"`
	UserID     int64    `json:"userId"`
	Username   string   `json:"username"`
	Name       string   `json:"name"`
	Sections   []string `json:"sections"`
	ReadOnly   bool     `json:"readOnly"`
	CreatedAt  string   `json:"createdAt"`
	LastUsedAt string   `json:"lastUsedAt,omitempty"`
	ExpiresAt  string   `json:"expiresAt,omitempty"`
}

func toAdminMCPTokenJSON(t store.APITokenWithUser) adminMCPTokenJSON {
	j := adminMCPTokenJSON{
		ID: t.ID, UserID: t.UserID, Username: t.Username, Name: t.Name,
		Sections: t.Sections, ReadOnly: t.ReadOnly,
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

// handleAdminListMCPTokens returns every user's active API tokens, annotated
// with the owner's username. Revoked tokens are omitted (they no longer work).
func (s *Server) handleAdminListMCPTokens(w http.ResponseWriter, r *http.Request) {
	toks, err := s.store.ListAllAPITokens(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list tokens")
		return
	}
	out := []adminMCPTokenJSON{}
	for _, t := range toks {
		if t.Revoked {
			continue
		}
		out = append(out, toAdminMCPTokenJSON(t))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAdminRevokeMCPToken revokes any user's token by id (not scoped to the
// caller). 404 if the id is unknown or already revoked.
func (s *Server) handleAdminRevokeMCPToken(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	revoked, err := s.store.AdminRevokeAPIToken(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not revoke token")
		return
	}
	if !revoked {
		writeErr(w, http.StatusNotFound, "token not found")
		return
	}
	s.audit(r, "mcp.admin.token.revoke", chi.URLParam(r, "id"), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type adminOAuthClientJSON struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirectUris"`
	CreatedAt    string   `json:"createdAt"`
}

// handleAdminListOAuthClients lists every dynamically-registered MCP OAuth
// client. Clients are public (no secret), so the full row is safe to surface.
func (s *Server) handleAdminListOAuthClients(w http.ResponseWriter, r *http.Request) {
	clients, err := s.store.ListOAuthClients(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list clients")
		return
	}
	out := []adminOAuthClientJSON{}
	for _, c := range clients {
		out = append(out, adminOAuthClientJSON{
			ID: c.ID, Name: c.Name, RedirectURIs: c.RedirectURIs,
			CreatedAt: c.CreatedAt.Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAdminDeleteOAuthClient de-registers a client and purges every code and
// refresh token issued to it, severing all access derived from it. 404 if the
// client id is unknown.
func (s *Server) handleAdminDeleteOAuthClient(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	deleted, err := s.store.DeleteOAuthClient(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not delete client")
		return
	}
	if !deleted {
		writeErr(w, http.StatusNotFound, "client not found")
		return
	}
	s.audit(r, "mcp.admin.oauth_client.delete", id, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
