package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/mcp"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// This file implements a minimal, spec-compliant OAuth 2.1 Authorization Server
// for the remote MCP endpoint: RFC 8414 metadata, RFC 7591 dynamic client
// registration, and the authorization-code + PKCE (S256) + refresh-token
// grants with RFC 8707 audience binding. It is mounted only when MCP is enabled
// AND a public URL is configured (see mountMCP). User authentication and consent
// reuse the existing GUI session (dc_session) — we never handle passwords here.

const (
	oauthCodeTTL    = 60 * time.Second
	oauthRefreshTTL = 30 * 24 * time.Hour
	scopeFull       = mcp.ScopeFull     // "mcp"
	scopeReadOnly   = mcp.ScopeReadOnly // "mcp:read"
	clientIDPrefix  = "dcmcp_"
	maxRedirectURIs = 5
)

// ---- discovery: RFC 8414 authorization server metadata ----

func (s *Server) handleOAuthASMetadata(w http.ResponseWriter, r *http.Request) {
	base := s.mcpBase()
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         base,
		"authorization_endpoint":                         base + "/oauth/authorize",
		"token_endpoint":                                 base + "/oauth/token",
		"registration_endpoint":                          base + "/oauth/register",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"scopes_supported":                               []string{scopeFull, scopeReadOnly},
		"authorization_response_iss_parameter_supported": true,
	})
}

// ---- RFC 7591 dynamic client registration ----

func (s *Server) handleOAuthRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RedirectURIs            []string `json:"redirect_uris"`
		ClientName              string   `json:"client_name"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	}
	if err := decodeJSON(r, &body); err != nil {
		oauthErr(w, http.StatusBadRequest, "invalid_client_metadata", "malformed JSON")
		return
	}
	if len(body.RedirectURIs) == 0 || len(body.RedirectURIs) > maxRedirectURIs {
		oauthErr(w, http.StatusBadRequest, "invalid_redirect_uri", "1..5 redirect_uris required")
		return
	}
	// This AS only issues public clients (PKCE, no secret). Reject a request that
	// explicitly asks for a confidential method rather than silently downgrading.
	if m := body.TokenEndpointAuthMethod; m != "" && m != "none" {
		oauthErr(w, http.StatusBadRequest, "invalid_client_metadata", "only token_endpoint_auth_method=none is supported")
		return
	}
	for _, u := range body.RedirectURIs {
		if !validRedirectURI(u) {
			oauthErr(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris must be https or loopback")
			return
		}
	}
	id := clientIDPrefix + randToken(16)
	c := &store.OAuthClient{ID: id, Name: body.ClientName, RedirectURIs: body.RedirectURIs}
	if err := s.store.CreateOAuthClient(r.Context(), c); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not register client")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  id,
		"client_id_issued_at":        time.Now().Unix(),
		"client_name":                body.ClientName,
		"redirect_uris":              body.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
}

// ---- authorization endpoint ----

type authzParams struct {
	clientID      string
	redirectURI   string
	state         string
	codeChallenge string
	method        string
	scope         string
	resource      string
}

func parseAuthzParams(q url.Values) authzParams {
	return authzParams{
		clientID:      q.Get("client_id"),
		redirectURI:   q.Get("redirect_uri"),
		state:         q.Get("state"),
		codeChallenge: q.Get("code_challenge"),
		method:        q.Get("code_challenge_method"),
		scope:         q.Get("scope"),
		resource:      q.Get("resource"),
	}
}

// handleOAuthAuthorize (GET) validates the request, confirms the browser holds a
// valid GUI session, and renders the consent screen. Validation errors that
// concern the client/redirect are shown as a page (never redirected, to avoid
// open-redirect); the rest is enforced here too before any code is minted.
func (s *Server) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	p := parseAuthzParams(r.URL.Query())
	client, ok := s.validateClientRedirect(w, r, p)
	if !ok {
		return
	}
	if r.URL.Query().Get("response_type") != "code" {
		s.authzRedirectError(w, r, p, "unsupported_response_type")
		return
	}
	if p.codeChallenge == "" || p.method != "S256" {
		s.authzRedirectError(w, r, p, "invalid_request")
		return
	}
	if !s.resourceOK(p.resource) {
		s.authzRedirectError(w, r, p, "invalid_target")
		return
	}

	claims := s.sessionClaims(r)
	if claims == nil {
		renderOAuthPage(w, loginNeededTmpl, map[string]any{"Base": s.mcpBase()})
		return
	}

	renderOAuthPage(w, consentTmpl, map[string]any{
		"User":          claims.Username,
		"ClientName":    clientDisplay(client),
		"CSRF":          s.oauthCSRF(r, p.clientID, p.redirectURI),
		"ClientID":      p.clientID,
		"RedirectURI":   p.redirectURI,
		"State":         p.state,
		"CodeChallenge": p.codeChallenge,
		"Method":        p.method,
		"Resource":      p.resource,
	})
}

// handleOAuthAuthorizeDecision (POST) processes the consent form: re-validates
// everything, checks the session-bound CSRF token, and either issues a code
// (redirect with ?code) or denies (redirect with ?error=access_denied).
func (s *Server) handleOAuthAuthorizeDecision(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeErr(w, http.StatusBadRequest, "bad form")
		return
	}
	p := parseAuthzParams(r.PostForm)
	client, ok := s.validateClientRedirect(w, r, p)
	if !ok {
		return
	}
	claims := s.sessionClaims(r)
	if claims == nil {
		renderOAuthPage(w, loginNeededTmpl, map[string]any{"Base": s.mcpBase()})
		return
	}
	// CSRF: the token is bound to this browser's session; an attacker cannot
	// read it (httpOnly cookie) nor compute it (HMAC under the MCP key).
	if !s.oauthCSRFValid(r, r.PostForm.Get("csrf"), p.clientID, p.redirectURI) {
		writeErr(w, http.StatusForbidden, "invalid CSRF token")
		return
	}
	if p.codeChallenge == "" || p.method != "S256" || !s.resourceOK(p.resource) {
		s.authzRedirectError(w, r, p, "invalid_request")
		return
	}

	if r.PostForm.Get("decision") != "allow" {
		s.authzRedirectError(w, r, p, "access_denied")
		return
	}

	scope := scopeFull
	if r.PostForm.Get("readonly") == "1" {
		scope = scopeReadOnly
	}
	code := randToken(32)
	err := s.store.CreateOAuthCode(r.Context(), hashToken(code), &store.OAuthCode{
		ClientID:      client.ID,
		UserID:        claims.UserID,
		RedirectURI:   p.redirectURI,
		CodeChallenge: p.codeChallenge,
		Resource:      s.mcpResource(),
		Scope:         scope,
		ExpiresAt:     time.Now().Add(oauthCodeTTL),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not issue code")
		return
	}
	s.audit(r, "mcp.oauth.authorize", claims.Username, "client="+client.ID+" scope="+scope)

	u, _ := url.Parse(p.redirectURI)
	q := u.Query()
	q.Set("code", code)
	q.Set("iss", s.mcpBase()) // RFC 9207 issuer identification (mix-up defense)
	if p.state != "" {
		q.Set("state", p.state)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// ---- token endpoint ----

func (s *Server) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthErr(w, http.StatusBadRequest, "invalid_request", "bad form")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.tokenFromCode(w, r)
	case "refresh_token":
		s.tokenFromRefresh(w, r)
	default:
		oauthErr(w, http.StatusBadRequest, "unsupported_grant_type", "")
	}
}

func (s *Server) tokenFromCode(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	rec, err := s.store.ConsumeOAuthCode(r.Context(), hashToken(f.Get("code")))
	if err != nil {
		oauthErr(w, http.StatusBadRequest, "invalid_grant", "unknown or used code")
		return
	}
	if time.Now().After(rec.ExpiresAt) {
		oauthErr(w, http.StatusBadRequest, "invalid_grant", "code expired")
		return
	}
	if f.Get("client_id") != rec.ClientID || f.Get("redirect_uri") != rec.RedirectURI {
		oauthErr(w, http.StatusBadRequest, "invalid_grant", "client/redirect mismatch")
		return
	}
	if rec.Resource != s.mcpResource() {
		oauthErr(w, http.StatusBadRequest, "invalid_grant", "resource mismatch")
		return
	}
	// PKCE S256: BASE64URL(SHA256(verifier)) must equal the stored challenge.
	if !verifyPKCE(f.Get("code_verifier"), rec.CodeChallenge) {
		oauthErr(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	s.issueTokens(w, r, rec.ClientID, rec.UserID, rec.Scope, rec.Resource)
}

func (s *Server) tokenFromRefresh(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	rec, err := s.store.ConsumeRefreshToken(r.Context(), hashToken(f.Get("refresh_token")))
	if err != nil {
		oauthErr(w, http.StatusBadRequest, "invalid_grant", "unknown refresh token")
		return
	}
	if !rec.ExpiresAt.IsZero() && time.Now().After(rec.ExpiresAt) {
		oauthErr(w, http.StatusBadRequest, "invalid_grant", "refresh token expired")
		return
	}
	if f.Get("client_id") != rec.ClientID {
		oauthErr(w, http.StatusBadRequest, "invalid_grant", "client mismatch")
		return
	}
	if rec.Resource != s.mcpResource() {
		oauthErr(w, http.StatusBadRequest, "invalid_grant", "resource mismatch")
		return
	}
	s.issueTokens(w, r, rec.ClientID, rec.UserID, rec.Scope, rec.Resource)
}

// issueTokens mints an access token (audience-bound to resource) and a rotated
// refresh token. resource is taken from the consumed grant and re-asserted by
// the callers against the current MCP resource, so the binding is enforced.
func (s *Server) issueTokens(w http.ResponseWriter, r *http.Request, clientID string, userID int64, scope, resource string) {
	readOnly := scope == scopeReadOnly
	access, _, err := mcp.MintAccessToken(s.mcpSigningKey, s.mcpBase(), resource, userID, readOnly, mcp.AccessTokenTTL)
	if err != nil {
		oauthErr(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	refresh := randToken(32)
	if err := s.store.CreateRefreshToken(r.Context(), hashToken(refresh), &store.OAuthRefreshToken{
		ClientID: clientID, UserID: userID, Scope: scope, Resource: resource,
		ExpiresAt: time.Now().Add(oauthRefreshTTL),
	}); err != nil {
		oauthErr(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(mcp.AccessTokenTTL.Seconds()),
		"refresh_token": refresh,
		"scope":         scope,
	})
}

// ---- helpers ----

func (s *Server) mcpBase() string     { return strings.TrimRight(s.cfg.MCPPublicURL, "/") }
func (s *Server) mcpResource() string { return s.mcpBase() + "/mcp" }

func (s *Server) resourceOK(resource string) bool {
	return resource == "" || resource == s.mcpResource()
}

func (s *Server) sessionClaims(r *http.Request) *auth.Claims {
	c, err := r.Cookie(auth.SessionCookie)
	if err != nil {
		return nil
	}
	claims, err := s.mw.ParseSessionToken(c.Value)
	if err != nil {
		return nil
	}
	return claims
}

// validateClientRedirect loads the client and verifies the redirect_uri is one
// it registered (exact match). On failure it renders an error PAGE — never a
// redirect — so an unvalidated redirect_uri can't be used for open redirection.
func (s *Server) validateClientRedirect(w http.ResponseWriter, r *http.Request, p authzParams) (*store.OAuthClient, bool) {
	client, err := s.store.OAuthClientByID(r.Context(), p.clientID)
	if err != nil {
		renderOAuthPage(w, errorTmpl, map[string]any{"Msg": "Unknown client."})
		return nil, false
	}
	for _, u := range client.RedirectURIs {
		if u == p.redirectURI {
			return client, true
		}
	}
	renderOAuthPage(w, errorTmpl, map[string]any{"Msg": "redirect_uri does not match a registered URI."})
	return nil, false
}

// authzRedirectError redirects the OAuth error back to the (already-validated)
// redirect_uri, per the spec, preserving state.
func (s *Server) authzRedirectError(w http.ResponseWriter, r *http.Request, p authzParams, code string) {
	u, err := url.Parse(p.redirectURI)
	if err != nil {
		renderOAuthPage(w, errorTmpl, map[string]any{"Msg": code})
		return
	}
	q := u.Query()
	q.Set("error", code)
	q.Set("iss", s.mcpBase())
	if p.state != "" {
		q.Set("state", p.state)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// oauthCSRF derives a CSRF token bound to the current session cookie AND the
// specific authorize request (client + redirect). An attacker can't read the
// httpOnly session cookie nor compute the HMAC, and a token minted for one
// client/redirect can't be replayed against another.
func (s *Server) oauthCSRF(r *http.Request, clientID, redirectURI string) string {
	c, err := r.Cookie(auth.SessionCookie)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, s.mcpSigningKey)
	mac.Write([]byte("csrf:" + c.Value + "\x00" + clientID + "\x00" + redirectURI))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) oauthCSRFValid(r *http.Request, got, clientID, redirectURI string) bool {
	want := s.oauthCSRF(r, clientID, redirectURI)
	if want == "" || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

func clientDisplay(c *store.OAuthClient) string {
	if c.Name != "" {
		return c.Name
	}
	return c.ID
}

func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		return host == "localhost" || host == "127.0.0.1" || host == "::1"
	}
	return false // custom-scheme app callbacks not supported in this MVP
}

func verifyPKCE(verifier, challenge string) bool {
	if verifier == "" || challenge == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(want), []byte(challenge)) == 1
}

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand must never fail; emitting a predictable token would be a
		// critical auth flaw, so fail closed (Recoverer turns this into a 500).
		panic("crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

func oauthErr(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func renderOAuthPage(w http.ResponseWriter, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = t.Execute(w, data)
}

// mcpSecret loads (or creates) the dedicated HMAC key used to sign MCP OAuth
// access tokens — separate from the app session secret.
func (s *Server) mcpSecret(ctx context.Context) []byte {
	const key = "mcp_oauth_secret"
	if v, _ := s.store.Setting(ctx, key); v != "" {
		if b, err := base64.StdEncoding.DecodeString(v); err == nil {
			return b
		}
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed generating MCP signing key: " + err.Error())
	}
	_ = s.store.SetSetting(ctx, key, base64.StdEncoding.EncodeToString(b))
	return b
}

var (
	pageHead = `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Docker Commander</title><style>
body{background:#0f1623;color:#e5e9f0;font-family:system-ui,sans-serif;display:grid;place-items:center;min-height:100vh;margin:0}
.card{background:#1a2233;border:1px solid #243047;border-radius:12px;padding:28px;max-width:420px;width:90%}
h1{font-size:18px;margin:0 0 8px}p{color:#8b97ad;font-size:14px;line-height:1.5}
.who{color:#e5e9f0;font-weight:600}button{font:inherit;border:0;border-radius:8px;padding:10px 14px;cursor:pointer;margin-top:8px}
.allow{background:#2496ed;color:#fff}.ro{background:#243047;color:#e5e9f0}.deny{background:transparent;color:#8b97ad}
.row{display:flex;gap:8px;flex-wrap:wrap;margin-top:12px}a{color:#2496ed}
</style>`

	consentTmpl = template.Must(template.New("consent").Parse(pageHead + `
<div class="card"><h1>Authorize access</h1>
<p><span class="who">{{.ClientName}}</span> is requesting access to Docker Commander as <span class="who">{{.User}}</span>.</p>
<p>Granting access lets this tool read and control Docker through your permissions. Read-only restricts it to viewing.</p>
<form method="post" action="/oauth/authorize">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<input type="hidden" name="client_id" value="{{.ClientID}}">
<input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
<input type="hidden" name="state" value="{{.State}}">
<input type="hidden" name="code_challenge" value="{{.CodeChallenge}}">
<input type="hidden" name="code_challenge_method" value="{{.Method}}">
<input type="hidden" name="resource" value="{{.Resource}}">
<div class="row">
<button class="allow" name="decision" value="allow">Allow full access</button>
<button class="ro" name="decision" value="allow" formnovalidate onclick="this.form.readonly.value='1'">Allow read-only</button>
<button class="deny" name="decision" value="deny">Deny</button>
</div>
<input type="hidden" name="readonly" value="0">
</form></div>`))

	loginNeededTmpl = template.Must(template.New("login").Parse(pageHead + `
<div class="card"><h1>Sign in first</h1>
<p>To authorize this tool you need to be signed in to Docker Commander in this browser.</p>
<p><a href="{{.Base}}/">Open Docker Commander</a>, sign in, then retry the connection from your tool.</p></div>`))

	errorTmpl = template.Must(template.New("err").Parse(pageHead + `
<div class="card"><h1>Authorization error</h1><p>{{.Msg}}</p></div>`))
)
