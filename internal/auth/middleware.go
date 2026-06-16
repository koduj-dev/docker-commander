package auth

import (
	"context"
	"net/http"
	"strings"
)

// SessionCookie is the name of the httpOnly cookie carrying the session JWT.
const SessionCookie = "dc_session"

type ctxKey int

const claimsKey ctxKey = 0

// Middleware enforces a valid, fully-authenticated session token. It reads the
// token from the session cookie first, then falls back to an Authorization
// Bearer header (useful for API clients and tooling).
type Middleware struct {
	tokens *TokenManager
}

// NewMiddleware builds auth middleware backed by the given token manager.
func NewMiddleware(tokens *TokenManager) *Middleware { return &Middleware{tokens: tokens} }

// RequireSession wraps next, rejecting requests without a valid session token.
func (m *Middleware) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := m.extract(r)
		if err != nil || claims.Kind != KindSession {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), claimsKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// extract pulls and validates the token from the request.
func (m *Middleware) extract(r *http.Request) (*Claims, error) {
	var raw string
	if c, err := r.Cookie(SessionCookie); err == nil {
		raw = c.Value
	} else if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		raw = strings.TrimPrefix(h, "Bearer ")
	} else if q := r.URL.Query().Get("token"); q != "" {
		// Allowed only for WebSocket upgrades where headers are awkward to set.
		raw = q
	}
	if raw == "" {
		return nil, http.ErrNoCookie
	}
	return m.tokens.Parse(raw)
}

// ClaimsFrom returns the authenticated claims stored in the request context.
func ClaimsFrom(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(claimsKey).(*Claims)
	return c, ok
}

// WithClaims returns a context carrying the given claims, the counterpart to
// ClaimsFrom. RequireSession uses the same key after verifying a token; this is
// exposed for composing authenticated contexts (and tests).
func WithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, claimsKey, c)
}

// ParseSessionToken validates a raw token and ensures it is a session token.
// Used by the WebSocket handler which authenticates before upgrading.
func (m *Middleware) ParseSessionToken(raw string) (*Claims, error) {
	c, err := m.tokens.Parse(raw)
	if err != nil {
		return nil, err
	}
	if c.Kind != KindSession {
		return nil, http.ErrNoCookie
	}
	return c, nil
}
