package api

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/koduj-dev/docker-commander/internal/auth"
)

// relyingParty describes this server the way WebAuthn needs it: an id a credential
// is bound to, and the origin it may be used from.
//
// Derived from the request rather than configured. A credential is bound to
// whatever id it was created with, so getting this wrong makes passkeys stop
// working, not become forgeable — and the browser is what enforces that the id
// matches the page's origin, so a forged Host header can only produce a credential
// that is useless everywhere.
//
// Returns false when this request could not be a passkey ceremony at all, which is
// the same condition the browser applies: a secure context.
func (s *Server) relyingParty(r *http.Request) (auth.RelyingParty, bool) {
	host := r.Host
	if h := firstHeaderValue(r, "X-Forwarded-Host"); h != "" && viaTrustedProxy(r) {
		host = h
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return auth.RelyingParty{}, false
	}
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}
	scheme := "http"
	if s.cookieSecure(r) {
		scheme = "https"
	}
	if scheme != "https" && !isLoopbackHost(hostname) {
		// Plain HTTP to a remote host: the browser will refuse, so offering it here
		// would produce a button that does nothing.
		return auth.RelyingParty{}, false
	}
	return auth.RelyingParty{
		ID:          hostname,
		Origin:      scheme + "://" + host,
		DisplayName: "Docker Commander",
	}, true
}

// isLoopbackHost reports whether the browser would treat this host as a secure
// context without TLS. localhost is special-cased by the spec; so are the loopback
// addresses.
func isLoopbackHost(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// handlePasskeyRegisterBegin starts pairing a passkey for the signed-in account.
func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	rp, ok := s.relyingParty(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, auth.ErrPasskeyUnavailable.Error())
		return
	}
	u, err := s.store.UserByID(r.Context(), c.UserID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Pairing changes what it takes to sign in as this account, so it needs the
	// password once the account already has a factor — the same rule an
	// authenticator app follows, and for the same reason.
	if u.MFAEnabled {
		var body struct {
			Password string `json:"password"`
		}
		if err := decodeJSON(r, &body); err != nil && !errors.Is(err, io.EOF) {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		if err := s.auth.VerifyUserPassword(r.Context(), auth.StepUpKey(u.ID, c.ID), u, body.Password); err != nil {
			if errors.Is(err, auth.ErrRateLimited) {
				writeErr(w, http.StatusTooManyRequests, err.Error())
				return
			}
			s.audit(r, "auth.passkey.add.denied", u.Username, "wrong password")
			writeErr(w, http.StatusForbidden, "password required to add a passkey")
			return
		}
	}

	creation, err := s.auth.BeginPasskeyRegistration(r.Context(), rp, c.UserID)
	switch {
	case err == nil:
	case errors.Is(err, auth.ErrTooManyFactors):
		writeErr(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, auth.ErrPasskeyUnavailable):
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	default:
		writeErr(w, http.StatusInternalServerError, "could not start")
		return
	}
	writeJSON(w, http.StatusOK, creation)
}

// handlePasskeyRegisterFinish stores the new credential.
//
// The credential JSON is read from the body by the WebAuthn library itself, so the
// name travels as a query parameter rather than sharing the body with it.
func (s *Server) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	rp, ok := s.relyingParty(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, auth.ErrPasskeyUnavailable.Error())
		return
	}
	name := r.URL.Query().Get("name")

	err := s.auth.FinishPasskeyRegistration(r.Context(), rp, c.UserID, name, r)
	switch {
	case err == nil:
	case errors.Is(err, auth.ErrTooManyFactors):
		writeErr(w, http.StatusConflict, err.Error())
		return
	default:
		writeErr(w, http.StatusBadRequest, "that passkey was not accepted")
		return
	}
	s.audit(r, "auth.passkey.add", c.Username, "passkey paired")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handlePasskeyLoginBegin issues an assertion challenge for a half-finished login.
//
// Reachable without a session: the caller has the MFA challenge token, which is
// proof the password was right, and nothing else here is secret — the credential
// ids belong to whoever holds that token's account.
func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	rp, ok := s.relyingParty(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, auth.ErrPasskeyUnavailable.Error())
		return
	}
	var body struct {
		MFAToken string `json:"mfaToken"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	assertion, err := s.auth.BeginPasskeyLogin(r.Context(), rp, body.MFAToken)
	switch {
	case err == nil:
	case errors.Is(err, auth.ErrNoPasskeys):
		writeErr(w, http.StatusConflict, err.Error())
		return
	default:
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	writeJSON(w, http.StatusOK, assertion)
}

// handlePasskeyLoginFinish verifies the assertion and signs the account in.
func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	rp, ok := s.relyingParty(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, auth.ErrPasskeyUnavailable.Error())
		return
	}
	// The library reads the credential from the body, so the challenge token comes
	// in as a query parameter. It is a bearer value either way: short-lived, single
	// use, and already spent by the time this returns.
	token := r.URL.Query().Get("mfaToken")

	res, err := s.auth.FinishPasskeyLogin(r.Context(), rp, r.RemoteAddr, token, r, sessionInfo(r))
	switch {
	case err == nil:
	case errors.Is(err, auth.ErrRateLimited):
		writeErr(w, http.StatusTooManyRequests, err.Error())
		return
	case errors.Is(err, auth.ErrClonedAuthenticator):
		// Audited loudly: this is the one failure here that means something is
		// actually wrong rather than someone tapping the wrong key.
		if name := s.auth.ChallengeUsername(token); name != "" {
			s.audit(r, "auth.passkey.cloned", name, "signature counter went backwards")
		}
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	default:
		if name := s.auth.ChallengeUsername(token); name != "" {
			s.audit(r, "auth.2fa.failed", name, "passkey rejected")
		}
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	s.audit(r, "auth.login", res.User.Username, "password + passkey")
	s.setSessionCookie(w, r, res.Token, res.ExpiresAt)
	writeJSON(w, http.StatusOK, s.loginResponse(r, res))
}

// handlePasskeySupport tells the browser whether this connection can do WebAuthn
// at all, so the UI can explain the absence rather than showing a dead button.
func (s *Server) handlePasskeySupport(w http.ResponseWriter, r *http.Request) {
	_, ok := s.relyingParty(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"available": ok,
		"reason":    map[bool]string{true: "", false: auth.ErrPasskeyUnavailable.Error()}[ok],
	})
}
