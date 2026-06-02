package api

import (
	"net/http"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// handleGetSettings returns admin-configurable app settings: which sections are
// disabled app-wide, the localhost-2FA exemption, and the full section catalog.
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	disabled, _ := s.store.DisabledSections(r.Context())
	no2fa, _ := s.store.LocalhostNo2FA(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"allSections":      store.Sections,
		"disabledSections": disabled,
		"localhostNo2fa":   no2fa,
	})
}

type settingsBody struct {
	DisabledSections []string `json:"disabledSections"`
	LocalhostNo2fa   bool     `json:"localhostNo2fa"`
}

// handleSetSettings persists the feature flags and the localhost-2FA setting.
func (s *Server) handleSetSettings(w http.ResponseWriter, r *http.Request) {
	var b settingsBody
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.store.SetDisabledSections(r.Context(), b.DisabledSections); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save settings")
		return
	}
	if err := s.store.SetLocalhostNo2FA(r.Context(), b.LocalhostNo2fa); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save settings")
		return
	}
	s.audit(r, "settings.update", "", "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
