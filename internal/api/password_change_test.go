package api

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
)

// Changing one's own password, over the real router.
//
// The change is only worth something if it takes access away from whoever had the
// old password: every other session must end, including a copy of the very token
// that asked. These tests use a read-only account on purpose; changing one's own
// password is self-service and must not need any grant.

func changePassword(a *apiClient, current, password string) (int, map[string]any) {
	return a.do("PUT", "/api/auth/me/password", map[string]string{"current": current, "password": password})
}

func canLogin(t *testing.T, from *apiClient, user, pass string) bool {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &apiClient{t: t, c: &http.Client{Jar: jar}, url: from.url, dm: from.dm, st: from.st}
	code, _ := c.do("POST", "/api/auth/login", map[string]string{"username": user, "password": pass})
	return code == http.StatusOK
}

func auditHas(t *testing.T, a *apiClient, action string) bool {
	t.Helper()
	entries, err := a.st.RecentAudit(context.Background(), 200, 0, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == action {
			return true
		}
	}
	return false
}

func TestChangeOwnPassword(t *testing.T) {
	admin := rbacFixture(t)
	me := loginWithRoles(t, admin, "changer", true, nil, nil)
	otherDevice := loginAgain(t, me, "changer", "restricted123")
	stolenCopy := cloneClient(t, me) // the same token as `me`, held by someone else

	if code, resp := changePassword(me, "restricted123", "brand-new-pass"); code != http.StatusOK {
		t.Fatalf("change: %d %v", code, resp)
	}

	if code, _ := me.do("GET", "/api/auth/me", nil); code != http.StatusOK {
		t.Errorf("the session that changed the password was signed out (me → %d)", code)
	}
	if code, _ := otherDevice.do("GET", "/api/auth/me", nil); code != http.StatusUnauthorized {
		t.Errorf("SECURITY: another session survived the password change (me → %d)", code)
	}
	if code, _ := stolenCopy.do("GET", "/api/auth/me", nil); code != http.StatusUnauthorized {
		t.Errorf("SECURITY: the old token of the asking session still works (me → %d)", code)
	}
	if n := len(listSessions(t, me)); n != 1 {
		t.Errorf("%d sessions after the change, want 1", n)
	}
	if canLogin(t, me, "changer", "restricted123") {
		t.Error("SECURITY: the old password still signs in")
	}
	if !canLogin(t, me, "changer", "brand-new-pass") {
		t.Error("the new password does not sign in")
	}
	if !auditHas(t, admin, "auth.password.change") {
		t.Error("the change was not audited")
	}
}

func TestChangeOwnPassword_WrongCurrentChangesNothing(t *testing.T) {
	admin := rbacFixture(t)
	me := loginWithRoles(t, admin, "changer", true, nil, nil)
	otherDevice := loginAgain(t, me, "changer", "restricted123")

	if code, _ := changePassword(me, "not-my-password", "brand-new-pass"); code != http.StatusForbidden {
		t.Fatalf("wrong current password: %d, want 403", code)
	}
	if !canLogin(t, me, "changer", "restricted123") {
		t.Error("SECURITY: a wrong current password still changed the password")
	}
	if code, _ := otherDevice.do("GET", "/api/auth/me", nil); code != http.StatusOK {
		t.Errorf("a refused change ended another session (me → %d)", code)
	}
	if !auditHas(t, admin, "auth.password.change.denied") {
		t.Error("the refusal was not audited")
	}
}

func TestChangeOwnPassword_WeakNewPasswordRefused(t *testing.T) {
	admin := rbacFixture(t)
	me := loginWithRoles(t, admin, "changer", true, nil, nil)

	code, resp := changePassword(me, "restricted123", "short")
	if code != http.StatusBadRequest || !strings.Contains(resp["error"].(string), "at least") {
		t.Fatalf("weak password: %d %v, want 400 naming the minimum", code, resp)
	}
	if !canLogin(t, me, "changer", "restricted123") {
		t.Error("a refused change replaced the password")
	}
	// Checked before the current password, so a typo in the new one spends none of
	// the guessing budget: a wrong current password with it is still a 400.
	if code, _ := changePassword(me, "not-my-password", "short"); code != http.StatusBadRequest {
		t.Errorf("weak password with a wrong current one: %d, want 400 before the current one is checked", code)
	}
}

func TestChangeOwnPassword_DirectoryAccountRefused(t *testing.T) {
	admin := rbacFixture(t)
	me := loginWithRoles(t, admin, "changer", true, nil, nil)
	u, err := admin.st.UserByUsername(context.Background(), "changer")
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.st.SetAuthSource(context.Background(), u.ID, "ldap"); err != nil {
		t.Fatal(err)
	}

	code, resp := changePassword(me, "restricted123", "brand-new-pass")
	if code != http.StatusBadRequest || !strings.Contains(resp["error"].(string), "directory") {
		t.Fatalf("directory account: %d %v, want 400 pointing at the directory", code, resp)
	}
	after, err := admin.st.UserByID(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PasswordHash != u.PasswordHash {
		t.Error("a directory account's stored hash was replaced")
	}
}

// PENTEST: the current-password check is not an unlimited oracle for whoever
// holds a session.
func TestPentestChangeOwnPassword_GuessingIsRateLimited(t *testing.T) {
	admin := rbacFixture(t)
	me := loginWithRoles(t, admin, "changer", true, nil, nil)

	limited := false
	for i := 0; i < 20; i++ {
		code, _ := changePassword(me, "guess-number-x", "brand-new-pass")
		if code == http.StatusTooManyRequests {
			limited = true
			break
		}
		if code != http.StatusForbidden {
			t.Fatalf("guess %d: %d, want 403 or 429", i, code)
		}
	}
	if !limited {
		t.Fatal("SECURITY: 20 wrong current passwords and no rate limit")
	}
	// Spent per session, so the right password from this session is refused too…
	if code, _ := changePassword(me, "restricted123", "brand-new-pass"); code != http.StatusTooManyRequests {
		t.Errorf("right password after the budget was spent: %d, want 429", code)
	}
	// …while signing in, which has its own budget, still works.
	if !canLogin(t, me, "changer", "restricted123") {
		t.Error("guessing from a session locked the account out of signing in")
	}
}
