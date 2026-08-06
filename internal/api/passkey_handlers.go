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
	hostname, port := host, ""
	if h, p, err := net.SplitHostPort(host); err == nil {
		hostname, port = h, p
	}
	// Lowercase before this becomes an identity: a host is case-insensitive, but an
	// RP id is compared as a string, so reaching the app as "DC.Example.com" would
	// otherwise mint a credential the browser will not match against the ordinary
	// spelling. That fails closed — nothing is forgeable — but it fails as "your
	// passkey stopped working", which is the outcome worth avoiding.
	//
	// A trailing dot is deliberately KEPT. It denotes the same name, so stripping it
	// looks like the same tidying — but the origin has to be spelled the way the
	// browser will send it, and a browser on "http://host.:8470/" sends exactly that.
	// Stripping the dot from the id alone splits the two, and the library compares
	// origins by string: every registration from such a host then fails. Keeping it
	// leaves id and origin consistent. isSecureLocalHost still ignores the dot,
	// because "is this a secure context" is a different question about the same name.
	hostname = strings.ToLower(hostname)
	if hostname == "" {
		return auth.RelyingParty{}, false
	}
	host = hostname
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	}
	scheme := "http"
	if s.cookieSecure(r) {
		scheme = "https"
	}
	if scheme != "https" && !isSecureLocalHost(hostname) {
		// Plain HTTP to a remote host: the browser will refuse, so offering it here
		// would produce a button that does nothing.
		return auth.RelyingParty{}, false
	}
	// An RP id must be a DOMAIN. An IP literal is not one, and every browser throws
	// SecurityError on it — so reaching this app at https://192.0.2.10/ or at
	// http://127.0.0.1:8470/ cannot do passkeys, however secure the context is.
	// Saying so here is the difference between an explanation and a button that
	// fails when pressed.
	//
	// The trailing dot has to come off for THIS question even though it is kept for
	// the id: net.ParseIP("127.0.0.1.") is nil, so a dotted literal would otherwise
	// walk straight past a check that exists to catch it.
	if net.ParseIP(strings.TrimSuffix(strings.Trim(hostname, "[]"), ".")) != nil {
		return auth.RelyingParty{}, false
	}
	return auth.RelyingParty{
		ID:          hostname,
		Origin:      scheme + "://" + host,
		DisplayName: "Docker Commander",
	}, true
}

// isSecureLocalHost reports whether the browser would treat this host as a secure
// context without TLS. localhost (and anything under it) is special-cased by the
// spec, as are the loopback addresses.
//
// A trailing dot is the same name — "localhost." is how you write the fully
// qualified form — and the browser treats it as a secure context, so it must not
// fall through to "remote host, plain HTTP".
func isSecureLocalHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
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
	stepUp := false
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
		stepUp = true
	}

	creation, err := s.auth.BeginPasskeyRegistration(r.Context(), rp, c.UserID, stepUp)
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
	// The library's decoder reads this body itself, so it never passes through
	// decodeJSON and never inherits its cap. Any signed-in account can open a
	// ceremony, so an uncapped read here is a memory cost anyone can impose.
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

	err := s.auth.FinishPasskeyRegistration(r.Context(), rp, c.UserID, name, r)
	switch {
	case err == nil:
	case errors.Is(err, auth.ErrTooManyFactors):
		writeErr(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, auth.ErrEnrollmentStale):
		s.audit(r, "auth.passkey.add.denied", c.Username, "ceremony predates the account's second factor")
		writeErr(w, http.StatusForbidden, err.Error())
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
	// The library reads the credential from the body, so the challenge token cannot
	// share it — and it must not travel in the URL, which is the one part of a
	// request that proxies and access logs record by default. It is normally spent by
	// the time this returns, but not always: a tripped rate limit refuses before the
	// spend, which would leave a live token sitting in a log file. So: a header.
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	token := r.Header.Get("X-MFA-Token")

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
	case errors.Is(err, auth.ErrInvalidCreds):
		if name := s.auth.ChallengeUsername(token); name != "" {
			s.audit(r, "auth.2fa.failed", name, "passkey rejected")
		}
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	default:
		// Anything else got past the signature check and then failed on our side.
		// Reporting it as a rejected passkey would blame the user for our outage and
		// put a false failure in the audit log.
		writeErr(w, http.StatusInternalServerError, "the passkey verified, but the login could not be completed")
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

// handlePasswordlessBegin issues a discoverable-credential challenge.
//
// Public, and deliberately says nothing: it is asked before anyone has claimed an
// identity, so there is no account to leak and no username to confirm. The reply is
// the same whether or not this server has a single passkey on it.
func (s *Server) handlePasswordlessBegin(w http.ResponseWriter, r *http.Request) {
	rp, ok := s.relyingParty(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, auth.ErrPasskeyUnavailable.Error())
		return
	}
	assertion, id, err := s.auth.BeginPasswordlessLogin(r.RemoteAddr, rp)
	switch {
	case err == nil:
	case errors.Is(err, auth.ErrRateLimited):
		writeErr(w, http.StatusTooManyRequests, err.Error())
		return
	case errors.Is(err, auth.ErrTooBusy):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	default:
		writeErr(w, http.StatusInternalServerError, "could not start")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ceremonyId": id, "publicKey": assertion.Response})
}

// handlePasswordlessFinish verifies the assertion and signs the account in.
func (s *Server) handlePasswordlessFinish(w http.ResponseWriter, r *http.Request) {
	rp, ok := s.relyingParty(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, auth.ErrPasskeyUnavailable.Error())
		return
	}
	// The body is the credential, which the library parses itself, so the ceremony
	// id rides in a header — not the URL, which is the part of a request that gets
	// logged. Capped for the same reason the other finish endpoints are.
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	id := r.Header.Get("X-Passkey-Ceremony")

	res, err := s.auth.FinishPasswordlessLogin(r.Context(), rp, r.RemoteAddr, id, r, sessionInfo(r))
	// Once the assertion verifies, the account is known — and these are the failures
	// worth writing down. This is the only thing between the network and a full
	// session, so a refusal that leaves no trace is a refusal nobody can investigate.
	var named *auth.PasswordlessError
	if errors.As(err, &named) {
		switch {
		case errors.Is(err, auth.ErrClonedAuthenticator):
			s.audit(r, "auth.passkey.cloned", named.Username, "signature counter went backwards")
		case errors.Is(err, auth.ErrUserVerificationRequired):
			s.audit(r, "auth.login.failed", named.Username, "passkey did not verify the user")
		case errors.Is(err, auth.ErrPasswordlessNotAllowed):
			s.audit(r, "auth.login.failed", named.Username, "passwordless sign-in is not enabled for this account")
		}
	}
	switch {
	case err == nil:
	case errors.Is(err, auth.ErrRateLimited):
		writeErr(w, http.StatusTooManyRequests, err.Error())
		return
	case errors.Is(err, auth.ErrTooBusy):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	case errors.Is(err, auth.ErrUserVerificationRequired):
		// Worth saying plainly: the key worked, and the account is fine. What is
		// missing is the PIN or fingerprint, and the fix is either to set one up or
		// to sign in with the password.
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	case errors.Is(err, auth.ErrPasswordlessNotAllowed):
		writeErr(w, http.StatusForbidden, err.Error())
		return
	case errors.Is(err, auth.ErrClonedAuthenticator):
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	case errors.Is(err, auth.ErrInvalidCreds):
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	default:
		// Past the signature check and then failed on our side. Reporting it as a
		// rejected passkey would blame the user for our outage.
		writeErr(w, http.StatusInternalServerError, "the passkey verified, but the login could not be completed")
		return
	}

	s.audit(r, "auth.login", res.User.Username, "passkey (passwordless)")
	s.setSessionCookie(w, r, res.Token, res.ExpiresAt)
	writeJSON(w, http.StatusOK, s.loginResponse(r, res))
}

// handlePasswordlessSetting turns signing in with a passkey alone on or off for the
// caller's own account.
//
// Needs the password, like every other change to what it takes to sign in as you.
// Turning it ON is the significant direction: it makes a passkey a whole login
// rather than a second factor, and for a synced passkey that moves the account onto
// whatever platform account the key syncs through. A stolen session must not be able
// to make that change and then use it.
func (s *Server) handlePasswordlessSetting(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	u, err := s.store.UserByID(r.Context(), c.UserID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		Enabled  bool   `json:"enabled"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.auth.VerifyUserPassword(r.Context(), auth.StepUpKey(u.ID, c.ID), u, body.Password); err != nil {
		if errors.Is(err, auth.ErrRateLimited) {
			writeErr(w, http.StatusTooManyRequests, err.Error())
			return
		}
		s.audit(r, "auth.passwordless.denied", u.Username, "wrong password")
		writeErr(w, http.StatusForbidden, "password required")
		return
	}
	// Only accounts this server owns the password for. An LDAP account's authority
	// is the directory, and the sign-in path refuses it anyway — offering the switch
	// would be a promise this cannot keep.
	if body.Enabled && u.AuthSource != "" && u.AuthSource != "local" {
		writeErr(w, http.StatusForbidden, auth.ErrPasswordlessNotAllowed.Error())
		return
	}
	if err := s.store.SetPasswordless(r.Context(), u.ID, body.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save")
		return
	}
	s.audit(r, "auth.passwordless", u.Username, map[bool]string{true: "enabled", false: "disabled"}[body.Enabled])
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": body.Enabled})
}
