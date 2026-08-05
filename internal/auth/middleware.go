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
	// epochs answers "is this account's session generation still the one this
	// token was minted with". Nil disables the check, which only the narrowest
	// unit tests do — production always wires the store.
	epochs SessionEpochSource
}

// SessionEpochSource reports an account's current session generation, and
// ErrNotFound (or any error) if the account is gone. It also answers whether a
// particular session is still recorded, which is what makes revoking one from
// the profile take effect against a self-contained token.
type SessionEpochSource interface {
	SessionEpoch(ctx context.Context, userID int64) (int64, error)
	SessionExists(ctx context.Context, id string, userID int64) (bool, error)
	TouchSession(ctx context.Context, id string) error
}

// NewMiddleware builds auth middleware backed by the given token manager and the
// source of session generations.
func NewMiddleware(tokens *TokenManager, epochs SessionEpochSource) *Middleware {
	return &Middleware{tokens: tokens, epochs: epochs}
}

// RequireSession wraps next, rejecting requests without a valid session token.
func (m *Middleware) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := m.extract(r)
		if err != nil || claims.Kind != KindSession {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// A JWT is self-contained, so a password change (or a deleted account)
		// reaches nothing that is already out there. Comparing the token's epoch
		// with the account's current one is what makes those take effect now
		// rather than whenever the token happens to expire.
		//
		// It costs one indexed read per request. The alternative — trusting the
		// token until its TTL runs out — is what lets a stolen session survive the
		// reset performed precisely to end it.
		if m.epochs != nil {
			epoch, err := m.epochs.SessionEpoch(r.Context(), claims.UserID)
			if err != nil || epoch != claims.Epoch {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			// And the session itself must still exist. Revoking one from the
			// profile deletes its row, which is what stops the token — there is
			// nothing else to stop, since the token is self-contained.
			//
			// A token minted before sessions were recorded has no row and is
			// refused: everyone signs in once after the upgrade, which is the
			// honest cost of being able to say what is signed in.
			live, err := m.epochs.SessionExists(r.Context(), claims.ID, claims.UserID)
			if err != nil || !live {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			// Best-effort: the store writes only when the value is a minute stale,
			// and a failure here must not cost the user their request.
			_ = m.epochs.TouchSession(r.Context(), claims.ID)
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
