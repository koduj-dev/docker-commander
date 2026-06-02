package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// handleListUsers returns all accounts (admin only).
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list users")
		return
	}
	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		out = append(out, map[string]any{
			"id": u.ID, "username": u.Username, "role": u.Role, "readOnly": u.ReadOnly,
			"sections": u.Sections, "totpEnabled": u.TOTPEnabled,
			"lastLoginAt": u.LastLoginAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type userBody struct {
	Username string   `json:"username"`
	Password string   `json:"password"`
	Role     string   `json:"role"`
	ReadOnly bool     `json:"readOnly"`
	Sections []string `json:"sections"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var b userBody
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	u, err := s.auth.CreateAccount(r.Context(), b.Username, b.Password, b.Role, b.ReadOnly, cleanSections(b.Sections))
	if err != nil {
		if errors.Is(err, auth.ErrWeakPassword) || errors.Is(err, auth.ErrInvalidUsername) {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.audit(r, "user.create", b.Username, b.Role)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": u.ID})
}

// handleUpdateUser changes a user's role, read-only flag and section grants.
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var b userBody
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	// Guard against demoting the last admin (would lock everyone out).
	if b.Role != "admin" {
		if cur, err := s.store.UserByID(r.Context(), id); err == nil && cur.IsAdmin() {
			if n, _ := s.store.CountAdmins(r.Context()); n <= 1 {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "cannot demote the last admin"})
				return
			}
		}
	}
	if err := s.store.UpdateUserAccess(r.Context(), id, b.Role, b.ReadOnly, cleanSections(b.Sections)); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not update user")
		return
	}
	s.audit(r, "user.update", chi.URLParam(r, "id"), b.Role)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleResetUserPassword sets a new password for a user.
func (s *Server) handleResetUserPassword(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var b struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.auth.SetPassword(r.Context(), id, b.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "user.password_reset", chi.URLParam(r, "id"), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	claims, _ := auth.ClaimsFrom(r.Context())
	if claims != nil && claims.UserID == id {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "you cannot delete your own account"})
		return
	}
	// Never remove the final admin.
	if cur, err := s.store.UserByID(r.Context(), id); err == nil && cur.IsAdmin() {
		if n, _ := s.store.CountAdmins(r.Context()); n <= 1 {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "cannot delete the last admin"})
			return
		}
	}
	if err := s.store.DeleteUser(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not delete user")
		return
	}
	s.audit(r, "user.delete", chi.URLParam(r, "id"), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// cleanSections keeps only valid section keys.
func cleanSections(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if store.ValidSection(s) {
			out = append(out, s)
		}
	}
	return out
}
