package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/koduj-dev/docker-commander/internal/auth"
)

// credentials is the shared shape for setup/login bodies.
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

// handleSetup creates the first admin account and logs them straight in so the
// frontend can immediately walk them through mandatory 2FA enrollment.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var body credentials
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
	res, err := s.auth.Login(r.Context(), r.RemoteAddr, body.Username, body.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "login after setup failed")
		return
	}
	s.audit(r, "auth.setup", u.Username, "first admin created")
	s.setSessionCookie(w, res.Token, res.ExpiresAt)
	writeJSON(w, http.StatusOK, loginResponse(res))
}

// handleLogin verifies password and either logs in or starts the 2FA step.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body credentials
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	res, err := s.auth.Login(r.Context(), r.RemoteAddr, body.Username, body.Password)
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
	writeJSON(w, http.StatusOK, loginResponse(res))
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
	writeJSON(w, http.StatusOK, loginResponse(res))
}

// handleMe returns the current user's profile.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	u, err := s.store.UserByID(r.Context(), c.UserID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": u.ID, "username": u.Username, "role": u.Role, "totpEnabled": u.TOTPEnabled,
	})
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
func loginResponse(res *auth.LoginResult) map[string]any {
	return map[string]any{
		"user": map[string]any{
			"id": res.User.ID, "username": res.User.Username,
			"role": res.User.Role, "totpEnabled": res.User.TOTPEnabled,
		},
		"expiresAt": res.ExpiresAt,
	}
}
