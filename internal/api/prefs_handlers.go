package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/koduj-dev/docker-commander/internal/auth"
)

const maxPrefsBytes = 64 * 1024 // generous cap for a small UI-prefs blob

// handleGetPrefs returns the current user's UI preferences (an opaque JSON
// object the frontend owns). Per-user so they follow the account across
// browsers, unlike localStorage.
func (s *Server) handleGetPrefs(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	prefs, err := s.store.UserPrefs(r.Context(), c.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not read preferences")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(prefs))
}

// handleSetPrefs replaces the current user's UI preferences. The body must be a
// JSON object; it's re-encoded canonically before storing.
func (s *Server) handleSetPrefs(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPrefsBytes+1))
	if err != nil || len(body) > maxPrefsBytes {
		writeErr(w, http.StatusBadRequest, "preferences too large")
		return
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		writeErr(w, http.StatusBadRequest, "preferences must be a JSON object")
		return
	}
	clean, err := json.Marshal(obj)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not encode preferences")
		return
	}
	if err := s.store.SetUserPrefs(r.Context(), c.UserID, string(clean)); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save preferences")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
