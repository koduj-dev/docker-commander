package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// factorView is one paired second factor, as its owner sees it.
//
// The secret never appears here. It leaves the server exactly once — in the QR
// code at pairing time — and there is no reason for a list to carry it.
type factorView struct {
	ID         int64     `json:"id"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
}

// handleListFactors returns the caller's own paired factors.
func (s *Server) handleListFactors(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	factors, err := s.auth.ListFactors(r.Context(), c.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list authenticators")
		return
	}
	out := make([]factorView, 0, len(factors))
	for _, f := range factors {
		out = append(out, factorView{
			ID: f.ID, Kind: f.Kind, Name: f.Name,
			CreatedAt: f.CreatedAt, LastUsedAt: f.LastUsedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteFactor unpairs one of the caller's own factors.
//
// It asks for the password, for the same reason pairing does: removing a factor
// changes what it takes to sign in as this account, and a session on its own must
// not be enough to do that. An attacker holding a stolen session could otherwise
// strip the owner's authenticators one by one.
func (s *Server) handleDeleteFactor(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	u, err := s.store.UserByID(r.Context(), c.UserID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var body struct {
		Password string `json:"password"`
	}
	// An empty body is a missing password — a failed step-up, not a malformed
	// request. Anything else in it is still a client bug.
	if err := decodeJSON(r, &body); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if !s.auth.VerifyUserPassword(r.Context(), r.RemoteAddr, u, body.Password) {
		s.audit(r, "auth.2fa.remove.denied", u.Username, "wrong password")
		writeErr(w, http.StatusForbidden, "password required to remove an authenticator")
		return
	}

	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	switch err := s.auth.RemoveFactor(r.Context(), c.UserID, id); {
	case err == nil:
	case errors.Is(err, auth.ErrLastFactor):
		writeErr(w, http.StatusConflict,
			"this is the only second factor on your account — pair another one first")
		return
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "authenticator not found")
		return
	default:
		writeErr(w, http.StatusInternalServerError, "could not remove that authenticator")
		return
	}
	s.audit(r, "auth.2fa.remove", u.Username, "authenticator unpaired")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
