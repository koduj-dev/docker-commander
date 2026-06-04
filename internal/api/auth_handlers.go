package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// credentials is the shape for login bodies (setup uses setupBody).
type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleAuthStatus reports whether first-run setup is still required, so the
// frontend knows to show the setup wizard instead of the login screen.
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	needs, err := s.auth.NeedsSetup(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "status check failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"needsSetup": needs})
}

// NOTE: login resolves credentials locally first; if a username has no local
// account (or is LDAP-provisioned) and LDAP is enabled, it authenticates via
// LDAP and provisions a local record. See auth.Service.Login.

// setupBody is the first-run payload: credentials plus the admin's choice of
// whether to enable 2FA right away or defer it (leaving it optional on
// localhost, to be turned on later from Settings).
type setupBody struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	Enable2FA bool   `json:"enable2fa"`
}

// handleSetup creates the first admin account and logs them straight in. If the
// admin chose to enable 2FA, the frontend then walks them through enrollment;
// if they deferred it, we turn on the localhost-no-2FA exemption so they aren't
// forced to enroll now.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var body setupBody
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	u, err := s.auth.Setup(r.Context(), body.Username, body.Password)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrSetupDone):
			writeErr(w, http.StatusConflict, "setup already completed")
		case errors.Is(err, auth.ErrWeakPassword), errors.Is(err, auth.ErrInvalidUsername):
			writeErr(w, http.StatusBadRequest, err.Error())
		default:
			writeErr(w, http.StatusInternalServerError, "setup failed")
		}
		return
	}
	// Deferring 2FA enables the localhost exemption so the admin can finish
	// setup without enrolling; they can require 2FA again from Settings.
	if !body.Enable2FA {
		if err := s.store.SetLocalhostNo2FA(r.Context(), true); err != nil {
			writeErr(w, http.StatusInternalServerError, "setup failed")
			return
		}
	}
	res, err := s.auth.Login(r.Context(), r.RemoteAddr, body.Username, body.Password, s.mfaExempt(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "login after setup failed")
		return
	}
	s.audit(r, "auth.setup", u.Username, "first admin created")
	s.setSessionCookie(w, res.Token, res.ExpiresAt)
	writeJSON(w, http.StatusOK, s.loginResponse(r, res))
}

// handleLogin verifies password and either logs in or starts the 2FA step.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body credentials
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	res, err := s.auth.Login(r.Context(), r.RemoteAddr, body.Username, body.Password, s.mfaExempt(r))
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrRateLimited):
			writeErr(w, http.StatusTooManyRequests, err.Error())
		default:
			writeErr(w, http.StatusUnauthorized, "invalid credentials")
		}
		return
	}
	if res.MFARequired {
		// Return the short-lived challenge token in the body; the browser keeps
		// it only for the immediate 2FA call.
		writeJSON(w, http.StatusOK, map[string]any{"mfaRequired": true, "mfaToken": res.Token})
		return
	}
	s.audit(r, "auth.login", res.User.Username, "password only")
	s.setSessionCookie(w, res.Token, res.ExpiresAt)
	writeJSON(w, http.StatusOK, s.loginResponse(r, res))
}

// verify2FABody carries the MFA challenge token and the user's TOTP code.
type verify2FABody struct {
	MFAToken string `json:"mfaToken"`
	Code     string `json:"code"`
}

// handleVerify2FA completes a login that required 2FA.
func (s *Server) handleVerify2FA(w http.ResponseWriter, r *http.Request) {
	var body verify2FABody
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	res, err := s.auth.VerifyMFA(r.Context(), body.MFAToken, body.Code)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid code")
		return
	}
	s.audit(r, "auth.login", res.User.Username, "password + 2fa")
	s.setSessionCookie(w, res.Token, res.ExpiresAt)
	writeJSON(w, http.StatusOK, s.loginResponse(r, res))
}

// handleMe returns the current user's profile, including their effective
// (after global feature flags) accessible sections and whether 2FA enrollment
// is enforced for this connection.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	u, err := s.store.UserByID(r.Context(), c.UserID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, s.userView(r, u))
}

// userView shapes the authenticated user for the frontend (no secrets).
func (s *Server) userView(r *http.Request, u *store.User) map[string]any {
	return map[string]any{
		"id": u.ID, "username": u.Username, "role": u.Role,
		"readOnly": u.ReadOnly, "totpEnabled": u.TOTPEnabled,
		"sections":    s.effectiveSections(r.Context(), u),
		"mfaEnforced": !s.mfaExempt(r),
	}
}

// effectiveSections is the set of menu sections a user may access: the globally
// enabled sections, intersected with the user's grant (admins get them all).
func (s *Server) effectiveSections(ctx context.Context, u *store.User) []string {
	disabled, _ := s.store.DisabledSections(ctx)
	enabled := make([]string, 0, len(store.Sections))
	for _, sec := range store.Sections {
		if !contains(disabled, sec) {
			enabled = append(enabled, sec)
		}
	}
	if u.IsAdmin() {
		return enabled
	}
	out := make([]string, 0, len(enabled))
	for _, sec := range enabled {
		if contains(u.Sections, sec) {
			out = append(out, sec)
		}
	}
	return out
}

// mfaExempt reports whether 2FA may be skipped for this request: the admin has
// enabled the localhost exemption and the request comes from a loopback address.
func (s *Server) mfaExempt(r *http.Request) bool {
	on, _ := s.store.LocalhostNo2FA(r.Context())
	return on && isLoopback(r)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// isLoopback reports whether the request originates from a loopback address.
// Only RemoteAddr is trusted (not forwarded headers), so a reverse proxy does
// not accidentally grant the exemption to remote clients.
func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// handleLogout clears the session cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: auth.SessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTOTPSetup begins 2FA enrollment and returns the QR + secret.
func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	enr, err := s.auth.BeginTOTPEnrollment(r.Context(), c.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not start enrollment")
		return
	}
	writeJSON(w, http.StatusOK, enr)
}

// handleTOTPEnable confirms enrollment with the first valid code.
func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.auth.ConfirmTOTPEnrollment(r.Context(), c.UserID, body.Code); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid code")
		return
	}
	s.audit(r, "auth.2fa.enable", c.Username, "totp enabled")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// setSessionCookie writes the httpOnly session cookie. Secure is omitted so it
// works over plain HTTP on localhost; behind TLS the browser still upgrades.
func (s *Server) setSessionCookie(w http.ResponseWriter, token string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// loginResponse shapes the JSON returned on a successful login.
func (s *Server) loginResponse(r *http.Request, res *auth.LoginResult) map[string]any {
	return map[string]any{
		"user":      s.userView(r, res.User),
		"expiresAt": res.ExpiresAt,
	}
}
