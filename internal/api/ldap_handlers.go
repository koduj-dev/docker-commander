package api

import (
	"net/http"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// handleGetLDAP returns the LDAP config with the bind password masked.
func (s *Server) handleGetLDAP(w http.ResponseWriter, r *http.Request) {
	c, _ := s.store.GetLDAP(r.Context())
	mappings := c.GroupMappings
	if mappings == nil {
		mappings = []store.LDAPGroupMapping{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": c.Enabled, "url": c.URL, "startTls": c.StartTLS,
		"bindDn": c.BindDN, "userBaseDn": c.UserBaseDN, "userFilter": c.UserFilter,
		"adminGroupDn": c.AdminGroupDN, "hasBindPassword": c.BindPassword != "",
		"groupMappings": mappings,
	})
}

// handleSetLDAP persists the LDAP config (blank bind password keeps the stored one).
func (s *Server) handleSetLDAP(w http.ResponseWriter, r *http.Request) {
	var c store.LDAPConfig
	if err := decodeJSON(r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.store.SetLDAP(r.Context(), c); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save ldap config")
		return
	}
	s.audit(r, "ldap.configure", c.URL, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTestLDAP checks connectivity/bind/search with the supplied (or stored)
// config.
func (s *Server) handleTestLDAP(w http.ResponseWriter, r *http.Request) {
	stored, _ := s.store.GetLDAP(r.Context())
	var c store.LDAPConfig
	_ = decodeJSON(r, &c)
	if c.URL == "" && c.UserBaseDN == "" {
		c = stored
	} else if c.BindPassword == "" {
		c.BindPassword = stored.BindPassword
	}
	n, err := auth.LDAPTest(c)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "entries": n})
}
