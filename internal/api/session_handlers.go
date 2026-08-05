package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// sessionView is one row of the caller's own session list.
type sessionView struct {
	ID         string    `json:"id"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"userAgent"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	// Current marks the session making this request, so nobody signs themselves
	// out while hunting for the one they don't recognise.
	Current bool `json:"current"`
}

// handleListSessions returns the caller's own sessions.
//
// Own only, deliberately: an admin view over everyone's sessions would be a
// record of when each person works and from where, which is surveillance rather
// than administration. If it is ever wanted it should be asked for, not arrive as
// a side effect of building revocation.
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	sessions, err := s.store.ListSessions(r.Context(), c.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list sessions")
		return
	}
	out := make([]sessionView, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, sessionView{
			ID: sess.ID, IP: sess.IP, UserAgent: sess.UserAgent,
			CreatedAt: sess.CreatedAt, LastSeenAt: sess.LastSeenAt,
			Current: sess.ID == c.ID,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteSession revokes one of the caller's own sessions.
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	id := chi.URLParam(r, "id")

	// Scoped by user id in the store, so another account's session id — even if
	// somehow known — deletes nothing and reads as missing.
	if err := s.store.DeleteSession(r.Context(), id, c.UserID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "session not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not revoke session")
		return
	}
	// Revoking the session you are using is allowed — it is just signing out, and
	// forbidding it would be a puzzle rather than a protection. Clear the cookie
	// so the browser doesn't keep presenting a token that is now refused.
	if id == c.ID {
		s.clearSessionCookie(w)
	}
	s.audit(r, "auth.session.revoke", c.Username, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleRevokeOtherSessions signs the caller out everywhere except here.
func (s *Server) handleRevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	sessions, err := s.store.ListSessions(r.Context(), c.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list sessions")
		return
	}
	revoked := 0
	for _, sess := range sessions {
		if sess.ID == c.ID {
			continue // keep the one asking
		}
		if err := s.store.DeleteSession(r.Context(), sess.ID, c.UserID); err == nil {
			revoked++
		}
	}
	s.audit(r, "auth.session.revoke_others", c.Username, "")
	writeJSON(w, http.StatusOK, map[string]int{"revoked": revoked})
}
