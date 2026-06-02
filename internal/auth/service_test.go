package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/store"
)

func newService(t *testing.T) (*Service, context.Context) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	c, _ := crypto.New(key)
	st.SetCipher(c)
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	svc := NewService(st, NewTokenManager(secret, time.Hour))
	return svc, context.Background()
}

func TestSetupFlow(t *testing.T) {
	svc, ctx := newService(t)
	if needs, _ := svc.NeedsSetup(ctx); !needs {
		t.Error("fresh store needs setup")
	}
	if _, err := svc.Setup(ctx, "admin", "correcthorse123"); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if needs, _ := svc.NeedsSetup(ctx); needs {
		t.Error("setup done → NeedsSetup false")
	}
	if _, err := svc.Setup(ctx, "other", "correcthorse123"); !errors.Is(err, ErrSetupDone) {
		t.Errorf("second setup should be ErrSetupDone, got %v", err)
	}
}

func TestCreateAccountValidation(t *testing.T) {
	svc, ctx := newService(t)
	if _, err := svc.CreateAccount(ctx, "ab", "correcthorse123", "user", false, nil); !errors.Is(err, ErrInvalidUsername) {
		t.Errorf("short username → ErrInvalidUsername, got %v", err)
	}
	if _, err := svc.CreateAccount(ctx, "alice", "short", "user", false, nil); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("weak password → ErrWeakPassword, got %v", err)
	}
	u, err := svc.CreateAccount(ctx, "alice", "correcthorse123", "user", true, []string{"containers"})
	if err != nil || u.Role != "user" || !u.ReadOnly {
		t.Fatalf("CreateAccount: %+v err=%v", u, err)
	}
	if _, err := svc.CreateAccount(ctx, "alice", "correcthorse123", "user", false, nil); err == nil {
		t.Error("duplicate username should fail")
	}
}

func TestLoginLocalAndRateLimit(t *testing.T) {
	svc, ctx := newService(t)
	_, _ = svc.Setup(ctx, "admin", "correcthorse123")

	res, err := svc.Login(ctx, "ip1", "admin", "correcthorse123", false)
	if err != nil || res.MFARequired || res.Token == "" {
		t.Fatalf("local login: %+v err=%v", res, err)
	}
	if _, err := svc.Login(ctx, "ip2", "admin", "wrong", false); !errors.Is(err, ErrInvalidCreds) {
		t.Errorf("wrong password → ErrInvalidCreds, got %v", err)
	}
	if _, err := svc.Login(ctx, "ip3", "ghost", "whatever", false); !errors.Is(err, ErrInvalidCreds) {
		t.Errorf("unknown user → ErrInvalidCreds, got %v", err)
	}

	// 5 failures on the same key trip the limiter.
	for i := 0; i < 5; i++ {
		_, _ = svc.Login(ctx, "brute", "admin", "wrong", false)
	}
	if _, err := svc.Login(ctx, "brute", "admin", "correcthorse123", false); !errors.Is(err, ErrRateLimited) {
		t.Errorf("after 5 fails → ErrRateLimited, got %v", err)
	}
}

func TestLogin2FAFlow(t *testing.T) {
	svc, ctx := newService(t)
	u, _ := svc.Setup(ctx, "admin", "correcthorse123")

	enr, err := svc.BeginTOTPEnrollment(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := totp.GenerateCode(enr.Secret, time.Now())
	if err := svc.ConfirmTOTPEnrollment(ctx, u.ID, code); err != nil {
		t.Fatalf("ConfirmTOTPEnrollment: %v", err)
	}

	// With 2FA enabled and no exemption, login returns an MFA challenge.
	res, err := svc.Login(ctx, "ip", "admin", "correcthorse123", false)
	if err != nil || !res.MFARequired {
		t.Fatalf("expected MFA challenge: %+v err=%v", res, err)
	}
	code2, _ := totp.GenerateCode(enr.Secret, time.Now())
	done, err := svc.VerifyMFA(ctx, res.Token, code2)
	if err != nil || done.MFARequired || done.Token == "" {
		t.Fatalf("VerifyMFA: %+v err=%v", done, err)
	}
	if _, err := svc.VerifyMFA(ctx, res.Token, "000000"); err == nil {
		t.Error("bad code should fail VerifyMFA")
	}

	// The localhost exemption skips MFA even though it's enabled.
	ex, err := svc.Login(ctx, "ip", "admin", "correcthorse123", true)
	if err != nil || ex.MFARequired {
		t.Errorf("exemptMFA should issue a session directly: %+v err=%v", ex, err)
	}
}

func TestSetPassword(t *testing.T) {
	svc, ctx := newService(t)
	u, _ := svc.Setup(ctx, "admin", "correcthorse123")
	if err := svc.SetPassword(ctx, u.ID, "short"); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("weak → ErrWeakPassword, got %v", err)
	}
	if err := svc.SetPassword(ctx, u.ID, "anewstrongpassword"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login(ctx, "ip", "admin", "anewstrongpassword", false); err != nil {
		t.Errorf("login with new password failed: %v", err)
	}
}

func TestLoginLDAPDisabledUnknownUser(t *testing.T) {
	svc, ctx := newService(t)
	_, _ = svc.Setup(ctx, "admin", "correcthorse123")
	// Unknown user, LDAP not configured → invalid creds (and no panic).
	if _, err := svc.Login(ctx, "ip", "nobody", "whatever", false); !errors.Is(err, ErrInvalidCreds) {
		t.Errorf("unknown user without LDAP → ErrInvalidCreds, got %v", err)
	}
}

func TestRateLimiterUnit(t *testing.T) {
	l := NewLoginLimiter(3, time.Hour)
	if !l.Allow("k") {
		t.Fatal("fresh key allowed")
	}
	l.Fail("k")
	l.Fail("k")
	if !l.Allow("k") {
		t.Error("under max still allowed")
	}
	l.Fail("k")
	if l.Allow("k") {
		t.Error("at max should be blocked")
	}
	l.Reset("k")
	if !l.Allow("k") {
		t.Error("reset should clear the window")
	}
}

func TestTokenExpiryAndTamper(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	tm := NewTokenManager(secret, time.Hour)
	tok, _, err := tm.Issue(1, "admin", "admin", KindSession)
	if err != nil {
		t.Fatal(err)
	}
	c, err := tm.Parse(tok)
	if err != nil || c.UserID != 1 || c.Kind != KindSession {
		t.Fatalf("parse: %+v err=%v", c, err)
	}
	// A different signing secret must reject the token.
	other := NewTokenManager([]byte("ffffffffffffffffffffffffffffffff"), time.Hour)
	if _, err := other.Parse(tok); err == nil {
		t.Error("token signed with a different secret should be rejected")
	}
	// An already-expired token is rejected.
	expTM := NewTokenManager(secret, -time.Hour)
	expTok, _, _ := expTM.Issue(1, "admin", "admin", KindSession)
	if _, err := tm.Parse(expTok); err == nil {
		t.Error("expired token should be rejected")
	}
}
