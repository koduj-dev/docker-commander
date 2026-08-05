package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
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
	res, err := s.auth.Login(r.Context(), r.RemoteAddr, body.Username, body.Password, s.mfaExempt(r), sessionInfo(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "login after setup failed")
		return
	}
	s.audit(r, "auth.setup", u.Username, "first admin created")
	s.setSessionCookie(w, r, res.Token, res.ExpiresAt)
	writeJSON(w, http.StatusOK, s.loginResponse(r, res))
}

// handleLogin verifies password and either logs in or starts the 2FA step.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body credentials
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	res, err := s.auth.Login(r.Context(), r.RemoteAddr, body.Username, body.Password, s.mfaExempt(r), sessionInfo(r))
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
	s.setSessionCookie(w, r, res.Token, res.ExpiresAt)
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
	res, err := s.auth.VerifyMFA(r.Context(), r.RemoteAddr, body.MFAToken, body.Code, sessionInfo(r))
	if err != nil {
		// Audited because this is the last factor standing between an attacker
		// who already has the password and a session: unaudited, a brute-force
		// attempt leaves no trace at all. The username comes from the challenge
		// token, which is signed, so it cannot be used to write arbitrary names
		// into the log.
		if name := s.auth.ChallengeUsername(body.MFAToken); name != "" {
			s.audit(r, "auth.2fa.failed", name, "invalid code")
		}
		if errors.Is(err, auth.ErrRateLimited) {
			writeErr(w, http.StatusTooManyRequests, err.Error())
			return
		}
		writeErr(w, http.StatusUnauthorized, "invalid code")
		return
	}
	s.audit(r, "auth.login", res.User.Username, "password + 2fa")
	s.setSessionCookie(w, r, res.Token, res.ExpiresAt)
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
		"email":    u.Email,
		"readOnly": u.ReadOnly, "totpEnabled": u.TOTPEnabled,
		"authSource": u.AuthSource,
		"createdAt":  u.CreatedAt, "lastLoginAt": u.LastLoginAt,
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

// isLoopback reports whether the request originates from a loopback address AND
// reached the server directly, with no proxy in the path.
//
// Both halves matter. r.RemoteAddr has been resolved by the clientIP middleware,
// which honours forwarded headers only from a configured trusted proxy — but
// that is not enough on its own here, because a proxy on the same machine is
// itself loopback. Two ways that bites: a proxy that forwards the client's own
// X-Forwarded-For unchanged (the nginx default, if you don't set it) lets a
// remote client claim 127.0.0.1; and a proxy that sends no forwarded header at
// all leaves every remote request looking loopback, since the peer is the local
// proxy. So a proxied request never qualifies, whatever the address says —
// "skip 2FA on localhost" has to mean the machine itself.
func isLoopback(r *http.Request) bool {
	if viaTrustedProxy(r) {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// sessionInfo describes the client for the session list the account later reads
// in its own profile. Both fields are attacker-controlled to some degree — the
// address can be a proxy, the user agent is whatever the client claims — so they
// are recognition aids, never an authorization input.
func sessionInfo(r *http.Request) auth.SessionInfo {
	// RemoteAddr is already the real client by this point: the clientIP middleware
	// rewrites it from X-Forwarded-For when the peer is a trusted proxy.
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	return auth.SessionInfo{IP: ip, UserAgent: r.UserAgent()}
}

// handleLogout ends the session: it revokes the row AND clears the cookie.
//
// Clearing the cookie alone would only make the browser forget a token that
// still worked — which is exactly the token someone would have copied.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, ok := auth.ClaimsFrom(r.Context()); ok {
		_ = s.store.DeleteSession(r.Context(), c.ID, c.UserID)
	}
	s.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// clearSessionCookie tells the browser to drop the session cookie. Shared by
// logout and by revoking the session you are currently using.
func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: auth.SessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

// handleTOTPSetup begins 2FA enrollment and returns the QR + secret.
func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	c, _ := auth.ClaimsFrom(r.Context())
	u, err := s.store.UserByID(r.Context(), c.UserID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Re-pairing needs the password; first-time enrolment does not.
	//
	// Pairing a new authenticator replaces the second factor, so a session on its
	// own must not be enough — otherwise any session takeover (a shared machine, a
	// token pasted into a URL) becomes a permanent authenticator takeover: the
	// attacker pairs their own device and satisfies 2FA from then on, while the
	// owner's app quietly stops working.
	//
	// A first enrolment is different: there is no factor to replace, and the
	// first-run wizard walks straight into it.
	if u.TOTPEnabled {
		var body struct {
			Password string `json:"password"`
		}
		// An empty body is a missing password, not a malformed request: it is the
		// shape an older client sends, and answering 400 there would say "your
		// request was wrong" about something that is simply a failed step-up.
		// Anything else in the body is still a client bug and still a 400.
		if err := decodeJSON(r, &body); err != nil && !errors.Is(err, io.EOF) {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		if !s.auth.VerifyUserPassword(r.Context(), r.RemoteAddr, u, body.Password) {
			s.audit(r, "auth.2fa.repair.denied", u.Username, "wrong password")
			writeErr(w, http.StatusForbidden, "password required to pair a new authenticator")
			return
		}
	}

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
		// What the owner calls this device. Theirs to choose, shown only back to
		// them; the store bounds and defaults it.
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.auth.ConfirmTOTPEnrollment(r.Context(), c.UserID, body.Code, body.Name); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid code")
		return
	}
	s.audit(r, "auth.2fa.enable", c.Username, "authenticator paired")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// setSessionCookie writes the httpOnly session cookie, marked Secure whenever
// the connection is actually HTTPS (see cookieSecure).
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		Secure:   s.cookieSecure(r),
		SameSite: http.SameSiteStrictMode,
	})
}

// cookieSecure reports whether the session cookie may be marked Secure.
//
// Without the flag a browser sends the session JWT over any plaintext request to
// the same host — one stray http:// link, or an attacker who can inject one, is
// enough to hand over twelve hours of Docker control. SameSite=Strict does not
// help here: it constrains cross-site requests, not the scheme.
//
// It cannot be set unconditionally: a plain-HTTP loopback install (the default)
// would stop being able to log in at all, since the browser would refuse to send
// the cookie back. So it follows the actual transport — native TLS, or a trusted
// proxy that says it terminated TLS. X-Forwarded-Proto is honoured only from a
// configured trusted proxy, for the same reason the client IP is.
func (s *Server) cookieSecure(r *http.Request) bool {
	if r.TLS != nil || s.cfg.TLSCert != "" {
		return true
	}
	if !viaTrustedProxy(r) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(firstHeaderValue(r, "X-Forwarded-Proto")), "https")
}

// firstHeaderValue returns the first comma-separated entry of a header, which is
// the one the outermost proxy set.
func firstHeaderValue(r *http.Request, name string) string {
	v := r.Header.Get(name)
	if i := strings.IndexByte(v, ','); i >= 0 {
		return v[:i]
	}
	return v
}

// loginResponse shapes the JSON returned on a successful login.
func (s *Server) loginResponse(r *http.Request, res *auth.LoginResult) map[string]any {
	return map[string]any{
		"user":      s.userView(r, res.User),
		"expiresAt": res.ExpiresAt,
	}
}

// handleSetMyEmail records the signed-in account's own alert address. Self-service
// and ungated: it only ever affects where that account's own alerts go, and the
// value is validated as an address before it is stored.
func (s *Server) handleSetMyEmail(w http.ResponseWriter, r *http.Request) {
	c, ok := auth.ClaimsFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var b struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	email := strings.TrimSpace(b.Email)
	// Empty clears it, which is how a user opts out of prefilled recipients.
	if email != "" && !validEmail(email) {
		writeErr(w, http.StatusBadRequest, "that does not look like an e-mail address")
		return
	}
	if err := s.store.SetUserEmail(r.Context(), c.UserID, email); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save the address")
		return
	}
	s.audit(r, "user.email", c.Username, email)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// validEmail is a deliberately loose check: exactly one @, something either side,
// a dot in the domain, and no spaces. Anything stricter rejects addresses that are
// perfectly deliverable, and the real proof is the Send test button.
func validEmail(s string) bool {
	if strings.ContainsAny(s, " \t\r\n,;<>") {
		return false
	}
	at := strings.Index(s, "@")
	if at <= 0 || at != strings.LastIndex(s, "@") || at == len(s)-1 {
		return false
	}
	domain := s[at+1:]
	// A trailing dot is a valid FQDN root label, but in a hand-typed address it is
	// a typo far more often than intent — and a wrong address means alerts that
	// silently never arrive.
	if strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	return strings.Contains(domain, ".")
}

// handleMyAccess returns the caller's own roles and the resulting per-section
// grants, so the profile page can show where each permission comes from.
//
// Self-service and ungated: it reads nothing but the signed-in account. Role
// management stays admin-only — this exposes the roles a user already holds, not
// the ability to see or change any others.
func (s *Server) handleMyAccess(w http.ResponseWriter, r *http.Request) {
	c, ok := auth.ClaimsFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	u, err := s.store.UserByID(r.Context(), c.UserID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}

	roles, err := s.store.RolesForUser(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not read your roles")
		return
	}
	roleOut := make([]map[string]any, 0, len(roles))
	for _, role := range roles {
		roleOut = append(roleOut, map[string]any{
			"id": role.ID, "name": role.Name, "description": role.Description,
			"builtin": role.Builtin, "sections": role.Sections, "hostIds": role.HostIDs,
		})
	}

	// The effective tree, with provenance. An admin bypasses the grant system
	// entirely, so say that rather than inventing a list that isn't consulted.
	type grantOut struct {
		Section string   `json:"section"`
		Write   bool     `json:"write"`
		From    []string `json:"from"`
		// AllHosts false means the grant is limited to Hosts (plus the local
		// daemon). Reported so the page can't imply reach the account hasn't got.
		AllHosts bool    `json:"allHosts"`
		Hosts    []int64 `json:"hosts"`
	}
	out := map[string]any{
		"admin":    u.IsAdmin(),
		"readOnly": u.ReadOnly,
		"roles":    roleOut,
		"sections": u.Sections,
	}
	if u.IsAdmin() {
		// An admin bypasses grants, so there is no overlay to compute — but
		// "you can reach everything" is not something a reader can check. Send the
		// concrete list so the page can show WHAT everything is, rather than
		// asking them to take it on faith.
		hosts, err := s.store.ListHosts(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not list hosts")
			return
		}
		out["allSections"] = store.Sections
		out["hostCount"] = len(hosts)
		writeJSON(w, http.StatusOK, out)
		return
	}

	grants, err := s.store.EffectiveGrants(r.Context(), u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not determine your permissions")
		return
	}
	// Provenance is recomputed here rather than returned by the store: the store
	// answers "may they?", this answers "and why?", which only the UI needs.
	sources := map[string][]string{}
	for _, sec := range u.Sections {
		if _, ok := grants[sec]; ok {
			sources[sec] = append(sources[sec], "your account")
		}
	}
	for _, role := range roles {
		for _, rs := range role.Sections {
			if _, ok := grants[rs.Section]; ok {
				sources[rs.Section] = append(sources[rs.Section], role.Name)
			}
		}
	}
	effective := make([]grantOut, 0, len(grants))
	for _, sec := range store.Sections { // stable, menu order
		g, ok := grants[sec]
		if !ok || !g.Granted {
			continue
		}
		hosts := make([]int64, 0, len(g.Hosts))
		for id := range g.Hosts {
			hosts = append(hosts, id)
		}
		sort.Slice(hosts, func(i, j int) bool { return hosts[i] < hosts[j] })
		effective = append(effective, grantOut{
			Section: sec, Write: g.Write, From: sources[sec],
			AllHosts: g.AllHosts, Hosts: hosts,
		})
	}
	out["effective"] = effective
	writeJSON(w, http.StatusOK, out)
}
