package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// resetFixture returns a data dir holding one account with a live session and a
// paired second factor.
func resetFixture(t *testing.T) (dir string, st *store.Store, u *store.User) {
	t.Helper()
	dir = t.TempDir()
	st, err := store.Open(filepath.Join(dir, "docker-commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	hash, err := auth.HashPassword("the-old-password")
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateUser(ctx, &store.User{Username: "locked-out", Role: "admin", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateFactor(ctx, &store.AuthFactor{
		UserID: id, Kind: store.FactorKindTOTP, Name: "Phone", Secret: "JBSWY3DPEHPK3PXP",
	}, true); err != nil {
		t.Fatal(err)
	}
	// ExpiresAt matters: ListSessions filters on `expires_at > now`, so a session
	// left at the zero time is already expired and would never be listed — the
	// assertion below would then hold with the deletion removed, which is exactly
	// how it first passed for the wrong reason.
	if err := st.CreateSession(ctx, &store.Session{
		ID: "session-that-must-die", UserID: id, IP: "10.0.0.9", UserAgent: "curl",
		ExpiresAt: time.Now().Add(12 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if live, _ := st.ListSessions(ctx, id); len(live) != 1 {
		t.Fatalf("the fixture has %d live sessions; the test cannot show one being ended", len(live))
	}
	u, err = st.UserByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return dir, st, u
}

// A reset that leaves the old sessions working is not a reset. Whoever reaches for
// this has usually lost control of the account, so the sessions are exactly what
// they need gone — and the online path (auth.Service.SetPassword) already ends
// them, so an offline path that did not would be a quiet way around it.
func TestResetPasswordEndsEverySession(t *testing.T) {
	dir, st, u := resetFixture(t)
	ctx := context.Background()
	before, err := st.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := resetFor(t, dir, "locked-out", "a-brand-new-password"); err != nil {
		t.Fatal(err)
	}

	sessions, err := st.ListSessions(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Errorf("SECURITY: %d session(s) survived the reset", len(sessions))
	}
	after, err := st.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The epoch matters separately: a token whose session row is already gone is
	// still signed and still parses, so only the epoch stops it.
	if after.SessionEpoch <= before.SessionEpoch {
		t.Errorf("SECURITY: the session epoch did not move (%d → %d) — tokens already issued still verify",
			before.SessionEpoch, after.SessionEpoch)
	}
}

// The new password works and the old one does not.
func TestResetPasswordReplacesTheCredential(t *testing.T) {
	dir, st, u := resetFixture(t)
	ctx := context.Background()

	if err := resetFor(t, dir, "locked-out", "a-brand-new-password"); err != nil {
		t.Fatal(err)
	}

	after, err := st.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := auth.VerifyPassword("a-brand-new-password", after.PasswordHash); !ok {
		t.Error("the new password does not verify")
	}
	if ok, _ := auth.VerifyPassword("the-old-password", after.PasswordHash); ok {
		t.Error("SECURITY: the old password still verifies")
	}
}

// The second factor is deliberately untouched. Whoever holds the files can bypass
// it anyway — the signing secret is in this same database — but this command must
// not do it for them, so that "nobody resets another account's second factor"
// stays true and stays easy to state.
func TestResetPasswordLeavesTheSecondFactorAlone(t *testing.T) {
	dir, st, u := resetFixture(t)
	ctx := context.Background()
	before, err := st.ListFactors(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("the fixture holds %d factors, want 1", len(before))
	}

	if err := resetFor(t, dir, "locked-out", "a-brand-new-password"); err != nil {
		t.Fatal(err)
	}

	after, err := st.ListFactors(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Fatalf("the account holds %d factors, want the one it started with", len(after))
	}
	// Identity, not the count. Counting alone passes a SWAP — delete the owner's
	// factor, insert your own — which is the single worst thing this command could
	// do and exactly what "nobody resets another account's second factor" promises
	// it does not.
	if after[0].ID != before[0].ID || after[0].Secret != before[0].Secret {
		t.Errorf("SECURITY: the second factor was replaced (id %d→%d)", before[0].ID, after[0].ID)
	}
	u2, _ := st.UserByID(ctx, u.ID)
	if !u2.MFAEnabled {
		t.Error("the reset disabled the second factor")
	}
}

// The floor applies where the write happens, not only at the prompt — a caller
// that skips the prompt must not be able to write a one-character password.
func TestResetPasswordEnforcesTheMinimumLength(t *testing.T) {
	dir, st, u := resetFixture(t)
	ctx := context.Background()
	before, _ := st.UserByID(ctx, u.ID)

	short := strings.Repeat("x", auth.MinPasswordLength-1)
	if err := resetFor(t, dir, "locked-out", short); err == nil {
		t.Error("SECURITY: a password below the minimum was accepted")
	}
	after, _ := st.UserByID(ctx, u.ID)
	if after.PasswordHash != before.PasswordHash {
		t.Error("SECURITY: the credential changed despite the refusal")
	}

	// …and exactly the minimum is fine: the guard is a floor, not an off-by-one.
	if err := resetFor(t, dir, "locked-out", strings.Repeat("y", auth.MinPasswordLength)); err != nil {
		t.Errorf("a password at the minimum should be accepted: %v", err)
	}
}

// It must never create a database. On a packaged install the data dir comes from a
// config file this path does not read, so a wrong guess would otherwise answer "no
// account called admin" from a directory that was empty a moment ago.
func TestResetPasswordRefusesToCreateADatabase(t *testing.T) {
	dir := t.TempDir()

	err := resetFor(t, dir, "admin", "a-brand-new-password")
	if err == nil {
		t.Fatal("an empty directory should be refused")
	}
	if !strings.Contains(err.Error(), "--data-dir") {
		t.Errorf("the error should point at --data-dir, got %q", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "docker-commander.db")); statErr == nil {
		t.Error("a database was created in a directory that had none")
	}
}

// It is written down, like every other privileged action.
func TestResetPasswordIsAudited(t *testing.T) {
	dir, st, _ := resetFixture(t)

	if err := resetFor(t, dir, "locked-out", "a-brand-new-password"); err != nil {
		t.Fatal(err)
	}

	entries, err := st.RecentAudit(context.Background(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "auth.password.reset" && e.Username == "locked-out" {
			found = true
		}
	}
	if !found {
		t.Error("the reset left no audit entry")
	}
}

// An LDAP account's password lives in the directory; writing a local one would
// store a credential the login path never consults.
func TestResetPasswordRefusesADirectoryAccount(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "docker-commander.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.CreateUser(context.Background(), &store.User{
		Username: "directory", Role: "user", AuthSource: "ldap", PasswordHash: "x",
	}); err != nil {
		t.Fatal(err)
	}

	err = resetFor(t, dir, "directory", "a-brand-new-password")
	if err == nil {
		t.Fatal("an LDAP account should be refused")
	}
	if got := err.Error(); got == "" {
		t.Error("the refusal should say why")
	}
}

// …and any other directory too. The rule is "accounts whose password this app
// owns", not "not LDAP" — a denylist would accept the next auth source added.
func TestResetPasswordRefusesAnyDirectoryAccount(t *testing.T) {
	for _, source := range []string{"ldap", "oidc", "saml"} {
		dir := t.TempDir()
		st, err := store.Open(filepath.Join(dir, "docker-commander.db"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateUser(context.Background(), &store.User{
			Username: "federated", Role: "user", AuthSource: source, PasswordHash: "x",
		}); err != nil {
			t.Fatal(err)
		}
		st.Close()

		if err := resetFor(t, dir, "federated", "a-brand-new-password"); err == nil {
			t.Errorf("SECURITY: a %q account was reset locally", source)
		}
	}
}

// A local account is not refused by that rule — the guard is about which authority
// owns the password, not a ban.
func TestResetPasswordAllowsBothLocalSpellings(t *testing.T) {
	for _, source := range []string{"", "local"} {
		dir := t.TempDir()
		st, err := store.Open(filepath.Join(dir, "docker-commander.db"))
		if err != nil {
			t.Fatal(err)
		}
		hash, _ := auth.HashPassword("the-old-password")
		if _, err := st.CreateUser(context.Background(), &store.User{
			Username: "local-user", Role: "admin", AuthSource: source, PasswordHash: hash,
		}); err != nil {
			t.Fatal(err)
		}
		st.Close()

		if err := resetFor(t, dir, "local-user", "a-brand-new-password"); err != nil {
			t.Errorf("auth_source %q should be resettable: %v", source, err)
		}
	}
}

// …and an unknown name says so rather than failing obscurely.
func TestResetPasswordUnknownAccount(t *testing.T) {
	dir, _, _ := resetFixture(t)
	if err := resetFor(t, dir, "nobody", "a-brand-new-password"); err == nil {
		t.Error("an unknown account should be refused")
	}
}

// The username comes from the argument after the flag.
func TestResetPasswordUserParsing(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
		on   bool
	}{
		{[]string{"dockercmd", "--reset-password", "alice"}, "alice", true},
		{[]string{"dockercmd", "-reset-password", "alice"}, "alice", true},
		{[]string{"dockercmd", "--reset-password"}, "", true},
		// A following flag is not a username.
		{[]string{"dockercmd", "--reset-password", "--data-dir", "/tmp"}, "", true},
		{[]string{"dockercmd", "--backup", "x.tar"}, "", false},
		// After a bare --, nothing is ours.
		{[]string{"dockercmd", "--", "--reset-password", "alice"}, "", false},
	} {
		old := os.Args
		os.Args = tc.args
		gotOn, gotUser := wantsResetPassword(), resetPasswordUser()
		os.Args = old
		if gotOn != tc.on || gotUser != tc.want {
			t.Errorf("%v: got (%v, %q), want (%v, %q)", tc.args, gotOn, gotUser, tc.on, tc.want)
		}
	}
}

// resetFor runs the reset the way the command does, minus the terminal prompt.
func resetFor(t *testing.T, dir, username, pw string) error {
	t.Helper()
	u, st, err := openForReset(dir, username)
	if err != nil {
		return err
	}
	defer st.Close()
	return applyResetTo(st, u, pw)
}
