package api

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/config"
	cryptopkg "github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// stepUpFixture returns a server, a request decorator authenticating as the user,
// and the account itself.
func stepUpFixture(t *testing.T, withTOTP bool) (*Server, func(*http.Request), *store.User) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := t.Context()

	key := make([]byte, 32)
	_, _ = rand.Read(key)
	c, _ := cryptopkg.New(key)
	st.SetCipher(c)
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	tokens := auth.NewTokenManager(secret, time.Hour)
	svc := auth.NewService(st, tokens)
	srv := &Server{cfg: config.Config{}, store: st, auth: svc, mw: auth.NewMiddleware(tokens, st)}

	u, err := svc.Setup(ctx, "admin", "correcthorse123")
	if err != nil {
		t.Fatal(err)
	}
	if withTOTP {
		enr, err := svc.BeginTOTPEnrollment(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		code, _ := totpCode(t, enr.Secret)
		if err := svc.ConfirmTOTPEnrollment(ctx, u.ID, code); err != nil {
			t.Fatal(err)
		}
		u, _ = st.UserByID(ctx, u.ID)
	}
	token := issueTestSession(t, tokens, st, u)
	return srv, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
	}, u
}

func setupTOTP(t *testing.T, srv *Server, authenticate func(*http.Request), body string) int {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/auth/totp/setup", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	authenticate(r)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	return w.Code
}

// PENTEST: pairing a new authenticator replaces the second factor, so a session
// alone must not be enough. Otherwise any session takeover — a shared machine, a
// token pasted into a URL — becomes a permanent authenticator takeover: the
// attacker pairs their own device and satisfies 2FA from then on, while the
// owner's app quietly stops working.
func TestPentestTOTPRepair_NeedsThePassword(t *testing.T) {
	srv, authenticate, _ := stepUpFixture(t, true)

	if code := setupTOTP(t, srv, authenticate, `{}`); code != http.StatusForbidden {
		t.Errorf("SECURITY: re-pairing with no password = %d, want 403", code)
	}
	if code := setupTOTP(t, srv, authenticate, `{"password":"wrong-password"}`); code != http.StatusForbidden {
		t.Errorf("SECURITY: re-pairing with a wrong password = %d, want 403", code)
	}
	if code := setupTOTP(t, srv, authenticate, `{"password":"correcthorse123"}`); code != http.StatusOK {
		t.Errorf("the account's own password must be accepted, got %d", code)
	}
}

// An empty body is a missing password, not a malformed request. Older clients
// sent no body at all, and answering 400 would tell them their request was
// wrong when what actually happened is that the step-up failed.
func TestTOTPRepairWithNoBodyIsAStepUpFailure(t *testing.T) {
	srv, authenticate, _ := stepUpFixture(t, true)

	r := httptest.NewRequest("POST", "/api/auth/totp/setup", nil)
	authenticate(r)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("re-pairing with no body = %d, want 403", w.Code)
	}

	// A body that is genuinely malformed is still a client bug.
	if code := setupTOTP(t, srv, authenticate, `{"password":`); code != http.StatusBadRequest {
		t.Errorf("a truncated body = %d, want 400", code)
	}
	// …and so is one naming fields we do not accept, which is how a typo in a
	// client stops being silent.
	if code := setupTOTP(t, srv, authenticate, `{"passwrod":"correcthorse123"}`); code != http.StatusBadRequest {
		t.Errorf("an unknown field = %d, want 400", code)
	}
}

// The counterweight: a FIRST enrolment has no factor to replace, and the
// first-run wizard walks straight into it — so it must not demand a password.
func TestTOTPFirstEnrolmentNeedsNoPassword(t *testing.T) {
	srv, authenticate, u := stepUpFixture(t, false)
	if u.TOTPEnabled {
		t.Fatal("fixture should start without 2FA")
	}
	if code := setupTOTP(t, srv, authenticate, `{}`); code != http.StatusOK {
		t.Errorf("a first enrolment must not require a password, got %d", code)
	}
}

// totpCode generates a code valid right now for the given secret.
func totpCode(t *testing.T, secret string) (string, context.Context) {
	t.Helper()
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return code, t.Context()
}
