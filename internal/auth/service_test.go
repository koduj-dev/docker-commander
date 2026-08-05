package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
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

	res, err := svc.Login(ctx, "ip1", "admin", "correcthorse123", false, SessionInfo{})
	if err != nil || res.MFARequired || res.Token == "" {
		t.Fatalf("local login: %+v err=%v", res, err)
	}
	if _, err := svc.Login(ctx, "ip2", "admin", "wrong", false, SessionInfo{}); !errors.Is(err, ErrInvalidCreds) {
		t.Errorf("wrong password → ErrInvalidCreds, got %v", err)
	}
	if _, err := svc.Login(ctx, "ip3", "ghost", "whatever", false, SessionInfo{}); !errors.Is(err, ErrInvalidCreds) {
		t.Errorf("unknown user → ErrInvalidCreds, got %v", err)
	}

	// 5 failures on the same key trip the limiter.
	for i := 0; i < 5; i++ {
		_, _ = svc.Login(ctx, "brute", "admin", "wrong", false, SessionInfo{})
	}
	if _, err := svc.Login(ctx, "brute", "admin", "correcthorse123", false, SessionInfo{}); !errors.Is(err, ErrRateLimited) {
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
	res, err := svc.Login(ctx, "ip", "admin", "correcthorse123", false, SessionInfo{})
	if err != nil || !res.MFARequired {
		t.Fatalf("expected MFA challenge: %+v err=%v", res, err)
	}
	code2, _ := totp.GenerateCode(enr.Secret, time.Now())
	done, err := svc.VerifyMFA(ctx, "ip", res.Token, code2, SessionInfo{})
	if err != nil || done.MFARequired || done.Token == "" {
		t.Fatalf("VerifyMFA: %+v err=%v", done, err)
	}
	if _, err := svc.VerifyMFA(ctx, "ip", res.Token, "000000", SessionInfo{}); err == nil {
		t.Error("bad code should fail VerifyMFA")
	}

	// The localhost exemption skips MFA even though it's enabled.
	ex, err := svc.Login(ctx, "ip", "admin", "correcthorse123", true, SessionInfo{})
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
	if _, err := svc.Login(ctx, "ip", "admin", "anewstrongpassword", false, SessionInfo{}); err != nil {
		t.Errorf("login with new password failed: %v", err)
	}
}

func TestLoginLDAPDisabledUnknownUser(t *testing.T) {
	svc, ctx := newService(t)
	_, _ = svc.Setup(ctx, "admin", "correcthorse123")
	// Unknown user, LDAP not configured → invalid creds (and no panic).
	if _, err := svc.Login(ctx, "ip", "nobody", "whatever", false, SessionInfo{}); !errors.Is(err, ErrInvalidCreds) {
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
	tokIssued, err := tm.Issue(1, "admin", "admin", KindSession, 0)
	if err != nil {
		t.Fatal(err)
	}
	c, err := tm.Parse(tokIssued.Token)
	if err != nil || c.UserID != 1 || c.Kind != KindSession {
		t.Fatalf("parse: %+v err=%v", c, err)
	}
	// A different signing secret must reject the token.
	other := NewTokenManager([]byte("ffffffffffffffffffffffffffffffff"), time.Hour)
	if _, err := other.Parse(tokIssued.Token); err == nil {
		t.Error("token signed with a different secret should be rejected")
	}
	// An already-expired token is rejected.
	expTM := NewTokenManager(secret, -time.Hour)
	expTokIssued, _ := expTM.Issue(1, "admin", "admin", KindSession, 0)
	if _, err := tm.Parse(expTokIssued.Token); err == nil {
		t.Error("expired token should be rejected")
	}
}

// enable2FA sets up an account with TOTP enabled and returns it plus its secret.
func enable2FA(t *testing.T, svc *Service, ctx context.Context) (*store.User, string) {
	t.Helper()
	u, err := svc.Setup(ctx, "admin", "correcthorse123")
	if err != nil {
		t.Fatal(err)
	}
	enr, err := svc.BeginTOTPEnrollment(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := totp.GenerateCode(enr.Secret, time.Now())
	if err := svc.ConfirmTOTPEnrollment(ctx, u.ID, code); err != nil {
		t.Fatal(err)
	}
	return u, enr.Secret
}

// PENTEST: the second factor is the only thing left once an attacker has the
// password, and it is six digits. Unthrottled it is guessable in minutes, so the
// verification step must burn the same budget the password step does.
func TestPentestMFA_BruteForceIsRateLimited(t *testing.T) {
	svc, ctx := newService(t)
	enable2FA(t, svc, ctx)

	// A challenge is good for ONE attempt, so each guess costs a fresh login —
	// which is the point. Five of them exhaust the window.
	for i := 0; i < 5; i++ {
		res, err := svc.Login(ctx, "10.0.0.9", "admin", "correcthorse123", false, SessionInfo{})
		if err != nil || !res.MFARequired {
			t.Fatalf("attempt %d: expected an MFA challenge: %+v err=%v", i+1, res, err)
		}
		if _, err := svc.VerifyMFA(ctx, "10.0.0.9", res.Token, "000000", SessionInfo{}); !errors.Is(err, ErrInvalidMFACode) {
			t.Fatalf("attempt %d: want ErrInvalidMFACode, got %v", i+1, err)
		}
	}
	// The window is full: even the password step is refused now.
	if _, err := svc.Login(ctx, "10.0.0.9", "admin", "correcthorse123", false, SessionInfo{}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("6th attempt: want ErrRateLimited, got %v", err)
	}
}

// PENTEST: rotating the source address must not buy a fresh budget — otherwise
// the limit is a speed bump for anyone with more than one IP.
func TestPentestMFA_RotatingTheClientIPStillHitsTheAccountBucket(t *testing.T) {
	svc, ctx := newService(t)
	enable2FA(t, svc, ctx)

	for i := 0; i < 5; i++ {
		ip := "10.0.0." + strconv.Itoa(100+i) // a different address every time
		res, err := svc.Login(ctx, ip, "admin", "correcthorse123", false, SessionInfo{})
		if err != nil || !res.MFARequired {
			t.Fatalf("attempt %d from %s: expected a challenge: %v", i+1, ip, err)
		}
		if _, err := svc.VerifyMFA(ctx, ip, res.Token, "000000", SessionInfo{}); !errors.Is(err, ErrInvalidMFACode) {
			t.Fatalf("attempt %d from %s: want ErrInvalidMFACode, got %v", i+1, ip, err)
		}
	}
	// A brand-new address: its own bucket is empty, but the account's is not.
	res, err := svc.Login(ctx, "10.0.0.200", "admin", "correcthorse123", false, SessionInfo{})
	if err != nil {
		t.Fatalf("login from a fresh IP: %v", err)
	}
	if _, err := svc.VerifyMFA(ctx, "10.0.0.200", res.Token, "000000", SessionInfo{}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("a fresh IP after 5 failures: want ErrRateLimited, got %v", err)
	}
}

// PENTEST: a correct password must not clear the failure window while the login
// is still unfinished — an attacker who knows the password could otherwise reset
// their own budget by re-authenticating between guesses.
func TestPentestMFA_PasswordSuccessDoesNotResetTheWindow(t *testing.T) {
	svc, ctx := newService(t)
	enable2FA(t, svc, ctx)

	for i := 0; i < 4; i++ {
		if _, err := svc.Login(ctx, "10.0.0.7", "admin", "wrong-password", false, SessionInfo{}); !errors.Is(err, ErrInvalidCreds) {
			t.Fatalf("password attempt %d: want ErrInvalidCreds, got %v", i+1, err)
		}
	}
	res, err := svc.Login(ctx, "10.0.0.7", "admin", "correcthorse123", false, SessionInfo{})
	if err != nil || !res.MFARequired {
		t.Fatalf("correct password should still reach the 2FA step: %+v err=%v", res, err)
	}
	// One more failure fills the window (4 + 1 = 5), so the next call is refused.
	if _, err := svc.VerifyMFA(ctx, "10.0.0.7", res.Token, "000000", SessionInfo{}); !errors.Is(err, ErrInvalidMFACode) {
		t.Fatalf("5th failure: want ErrInvalidMFACode, got %v", err)
	}
	if _, err := svc.VerifyMFA(ctx, "10.0.0.7", res.Token, "000000", SessionInfo{}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want ErrRateLimited once the window is full, got %v", err)
	}
}

// A legitimate user who fumbles a code and then gets it right must not be left
// throttled: success clears both buckets. Guards against "fixing" brute force by
// making the app unusable.
func TestMFA_SuccessClearsTheWindow(t *testing.T) {
	svc, ctx := newService(t)
	_, secret := enable2FA(t, svc, ctx)

	res, _ := svc.Login(ctx, "10.0.0.5", "admin", "correcthorse123", false, SessionInfo{})
	if _, err := svc.VerifyMFA(ctx, "10.0.0.5", res.Token, "000000", SessionInfo{}); !errors.Is(err, ErrInvalidMFACode) {
		t.Fatalf("want ErrInvalidMFACode, got %v", err)
	}
	// The fumble spent that challenge, so getting it right means logging in again.
	retry, _ := svc.Login(ctx, "10.0.0.5", "admin", "correcthorse123", false, SessionInfo{})
	code, _ := totp.GenerateCode(secret, time.Now())
	if _, err := svc.VerifyMFA(ctx, "10.0.0.5", retry.Token, code, SessionInfo{}); err != nil {
		t.Fatalf("correct code after a fumble: %v", err)
	}
	// Both buckets are clear, so a fresh login + 5 wrong codes are available again.
	for i := 0; i < 5; i++ {
		res2, err := svc.Login(ctx, "10.0.0.5", "admin", "correcthorse123", false, SessionInfo{})
		if err != nil {
			t.Fatalf("after a success the window should be clear; login %d gave %v", i+1, err)
		}
		if _, err := svc.VerifyMFA(ctx, "10.0.0.5", res2.Token, "000000", SessionInfo{}); !errors.Is(err, ErrInvalidMFACode) {
			t.Fatalf("after a success the window should be clear; attempt %d gave %v", i+1, err)
		}
	}
}

// ChallengeUsername is what the audit trail is written from, so it must not accept
// anything unsigned.
func TestChallengeUsername(t *testing.T) {
	svc, ctx := newService(t)
	enable2FA(t, svc, ctx)
	res, _ := svc.Login(ctx, "ip", "admin", "correcthorse123", false, SessionInfo{})

	if got := svc.ChallengeUsername(res.Token); got != "admin" {
		t.Errorf("challenge token names %q, want admin", got)
	}
	if got := svc.ChallengeUsername("not.a.token"); got != "" {
		t.Errorf("garbage token → %q, want empty", got)
	}
	// A full session token is not a challenge token and must not be accepted.
	full, _ := svc.Login(ctx, "ip2", "admin", "correcthorse123", true, SessionInfo{})
	if got := svc.ChallengeUsername(full.Token); got != "" {
		t.Errorf("session token accepted as a challenge → %q", got)
	}
}

// PENTEST: a TOTP code is a ONE-time password. Valid for ~90 seconds is not the
// same as usable for ~90 seconds — an observed code (shoulder-surfed, captured by
// a phishing proxy, screenshotted by malware) must be spendable once.
func TestPentestMFA_CodeCannotBeReplayed(t *testing.T) {
	svc, ctx := newService(t)
	_, secret := enable2FA(t, svc, ctx)

	res, _ := svc.Login(ctx, "10.0.0.3", "admin", "correcthorse123", false, SessionInfo{})
	code, _ := totp.GenerateCode(secret, time.Now())
	if _, err := svc.VerifyMFA(ctx, "10.0.0.3", res.Token, code, SessionInfo{}); err != nil {
		t.Fatalf("first use of a valid code: %v", err)
	}

	// Same code, still inside its window, fresh challenge: must be refused, and
	// refused the same way a wrong code is — a distinguishable error would tell an
	// attacker their capture was genuine.
	again, _ := svc.Login(ctx, "10.0.0.3", "admin", "correcthorse123", false, SessionInfo{})
	if _, err := svc.VerifyMFA(ctx, "10.0.0.3", again.Token, code, SessionInfo{}); !errors.Is(err, ErrInvalidMFACode) {
		t.Errorf("SECURITY: replayed code gave %v, want ErrInvalidMFACode", err)
	}
}

// The counterweight: burning a step must not lock the account out of the NEXT
// one. A replay guard that also rejects fresh codes is an outage, not a fix.
func TestMFA_NextCodeStillWorksAfterAReplayIsRefused(t *testing.T) {
	svc, ctx := newService(t)
	_, secret := enable2FA(t, svc, ctx)

	now := time.Now()
	first, _ := totp.GenerateCode(secret, now)
	res, _ := svc.Login(ctx, "ip", "admin", "correcthorse123", false, SessionInfo{})
	if _, err := svc.VerifyMFA(ctx, "ip", res.Token, first, SessionInfo{}); err != nil {
		t.Fatalf("first code: %v", err)
	}

	// The NEXT time step — one period ahead, so it is still inside the skew
	// window the server accepts, but a different one-time password.
	next, _ := totp.GenerateCode(secret, now.Add(30*time.Second))
	if next == first {
		t.Skip("the clock landed such that both steps render the same code")
	}
	res2, _ := svc.Login(ctx, "ip", "admin", "correcthorse123", false, SessionInfo{})
	if _, err := svc.VerifyMFA(ctx, "ip", res2.Token, next, SessionInfo{}); err != nil {
		t.Errorf("a code from the next step must still be accepted: %v", err)
	}
}

// PENTEST: setup creates the FIRST admin. Two requests arriving together must not
// both win — the loser would hold permanent admin on a fresh instance, and a
// fresh instance is exactly the one nobody is watching yet.
func TestPentestSetup_ConcurrentRequestsCreateOnlyOneAdmin(t *testing.T) {
	svc, ctx := newService(t)

	const racers = 8
	var wg sync.WaitGroup
	errs := make([]error, racers)
	start := make(chan struct{})
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // widen the window: everyone counts before anyone inserts
			_, errs[i] = svc.Setup(ctx, "admin"+strconv.Itoa(i), "correcthorse123")
		}()
	}
	close(start)
	wg.Wait()

	won := 0
	for i, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrSetupDone):
		default:
			t.Errorf("racer %d: unexpected error %v", i, err)
		}
	}
	if won != 1 {
		t.Errorf("SECURITY: %d concurrent setups succeeded, want exactly 1", won)
	}
	if n, _ := svc.store.CountUsers(ctx); n != 1 {
		t.Errorf("SECURITY: %d accounts exist after a setup race, want 1", n)
	}
}

// epochStore adapts the real store to the middleware's narrow interface.
type epochStore struct{ svc *Service }

func (e epochStore) SessionEpoch(ctx context.Context, userID int64) (int64, error) {
	return e.svc.store.SessionEpoch(ctx, userID)
}

func (e epochStore) SessionExists(ctx context.Context, id string, userID int64) (bool, error) {
	return e.svc.store.SessionExists(ctx, id, userID)
}

func (e epochStore) TouchSession(ctx context.Context, id string) error {
	return e.svc.store.TouchSession(ctx, id)
}

// PENTEST: a JWT is self-contained, so changing a password reaches nothing that
// is already out there. Without a generation check the attacker whose access
// prompted the reset keeps it for the rest of the token's twelve hours — handed
// to them by the very act meant to take it away.
func TestPentestSession_PasswordChangeInvalidatesIssuedTokens(t *testing.T) {
	svc, ctx := newService(t)
	u, err := svc.Setup(ctx, "admin", "correcthorse123")
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Login(ctx, "ip", "admin", "correcthorse123", false, SessionInfo{})
	if err != nil {
		t.Fatal(err)
	}

	mw := NewMiddleware(svc.tokens, epochStore{svc})
	authorized := func(token string) bool {
		reached := false
		h := mw.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true }))
		r := httptest.NewRequest("GET", "/api/auth/me", nil)
		r.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
		h.ServeHTTP(httptest.NewRecorder(), r)
		return reached
	}

	if !authorized(res.Token) {
		t.Fatal("a fresh session should be accepted")
	}
	if err := svc.SetPassword(ctx, u.ID, "a-different-password"); err != nil {
		t.Fatal(err)
	}
	if authorized(res.Token) {
		t.Error("SECURITY: a session issued before the password change still works")
	}

	// And the new credentials produce a session that does work — a revocation
	// that also breaks logging back in would be an outage, not a fix.
	fresh, err := svc.Login(ctx, "ip", "admin", "a-different-password", false, SessionInfo{})
	if err != nil {
		t.Fatalf("login with the new password: %v", err)
	}
	if !authorized(fresh.Token) {
		t.Error("the session issued after the change must be accepted")
	}
}

// PENTEST: deleting an account must stop its tokens too, including on routes
// that carry no section and therefore never reload the user.
func TestPentestSession_DeletedAccountsTokenIsRefused(t *testing.T) {
	svc, ctx := newService(t)
	u, _ := svc.Setup(ctx, "admin", "correcthorse123")
	res, _ := svc.Login(ctx, "ip", "admin", "correcthorse123", false, SessionInfo{})

	mw := NewMiddleware(svc.tokens, epochStore{svc})
	reached := false
	h := mw.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true }))

	if err := svc.store.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/prefs", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: res.Token})
	h.ServeHTTP(httptest.NewRecorder(), r)
	if reached {
		t.Error("SECURITY: a deleted account's token was accepted")
	}
}

// PENTEST: a challenge token buys ONE attempt. The rate limiter bounds guesses
// per window; this bounds them per password entry — the half an attacker holding
// the password could otherwise repeat as often as they liked, since a challenge
// stayed valid for five minutes.
func TestPentestMFA_ChallengeTokenIsSpentOnFirstUse(t *testing.T) {
	svc, ctx := newService(t)
	_, secret := enable2FA(t, svc, ctx)

	res, err := svc.Login(ctx, "10.0.0.4", "admin", "correcthorse123", false, SessionInfo{})
	if err != nil || !res.MFARequired {
		t.Fatalf("expected an MFA challenge: %v", err)
	}
	if _, err := svc.VerifyMFA(ctx, "10.0.0.4", res.Token, "000000", SessionInfo{}); !errors.Is(err, ErrInvalidMFACode) {
		t.Fatalf("first attempt: want ErrInvalidMFACode, got %v", err)
	}

	// Same token again — even with the RIGHT code, because the token is spent.
	code, _ := totp.GenerateCode(secret, time.Now())
	if _, err := svc.VerifyMFA(ctx, "10.0.0.4", res.Token, code, SessionInfo{}); !errors.Is(err, ErrInvalidCreds) {
		t.Errorf("SECURITY: a spent challenge token was accepted again: %v", err)
	}
}

// And a successful login spends it too, so the token cannot mint a second session.
func TestPentestMFA_ChallengeTokenCannotMintTwoSessions(t *testing.T) {
	svc, ctx := newService(t)
	_, secret := enable2FA(t, svc, ctx)

	res, _ := svc.Login(ctx, "10.0.0.6", "admin", "correcthorse123", false, SessionInfo{})
	code, _ := totp.GenerateCode(secret, time.Now())
	if _, err := svc.VerifyMFA(ctx, "10.0.0.6", res.Token, code, SessionInfo{}); err != nil {
		t.Fatalf("the first use should succeed: %v", err)
	}
	if _, err := svc.VerifyMFA(ctx, "10.0.0.6", res.Token, code, SessionInfo{}); err == nil {
		t.Error("SECURITY: the same challenge token issued a second session")
	}
}

// The counterweight: a fresh challenge always works. One attempt per token must
// not become "one attempt, ever".
func TestMFA_EachLoginGetsAUsableChallenge(t *testing.T) {
	svc, ctx := newService(t)
	_, secret := enable2FA(t, svc, ctx)

	for i := range 3 {
		res, err := svc.Login(ctx, "10.0.0.8", "admin", "correcthorse123", false, SessionInfo{})
		if err != nil {
			t.Fatalf("login %d: %v", i+1, err)
		}
		// A different time step each round, because the replay guard requires a
		// strictly newer counter — and inside the skew window, which spans exactly
		// one step either side of now. (Reaching further is the mistake that made
		// an earlier version of this test fail against correct code.)
		code, _ := totp.GenerateCode(secret, time.Now().Add(time.Duration(i-1)*30*time.Second))
		if _, err := svc.VerifyMFA(ctx, "10.0.0.8", res.Token, code, SessionInfo{}); err != nil {
			t.Fatalf("login %d should complete: %v", i+1, err)
		}
	}
}

// A successful step-up clears the bucket, exactly as a successful login does.
//
// Without that, someone else's wrong guesses from the same address keep the real
// account holder out of their own step-up for the rest of the window even when
// they type the right password — a lockout an attacker can buy with failures,
// against an endpoint that already requires a valid session.
func TestVerifyUserPasswordResetsTheBudgetOnSuccess(t *testing.T) {
	svc, ctx := newService(t)
	u, err := svc.Setup(ctx, "admin", "correcthorse123")
	if err != nil {
		t.Fatal(err)
	}

	// Four wrong guesses: one short of the limit of five.
	for i := 0; i < 4; i++ {
		if svc.VerifyUserPassword(ctx, "10.0.0.7", u, "nope") {
			t.Fatal("a wrong password was accepted")
		}
	}
	if !svc.VerifyUserPassword(ctx, "10.0.0.7", u, "correcthorse123") {
		t.Fatal("the correct password should still be accepted with budget left")
	}

	// If the budget survived that success, the fifth failure below would trip the
	// limiter and the correct password after it would be refused.
	for i := 0; i < 4; i++ {
		svc.VerifyUserPassword(ctx, "10.0.0.7", u, "nope")
	}
	if !svc.VerifyUserPassword(ctx, "10.0.0.7", u, "correcthorse123") {
		t.Error("a successful step-up should have reset the rate-limit budget")
	}
}
