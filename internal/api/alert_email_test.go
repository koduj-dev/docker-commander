package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// Alert e-mail recipients. Two things matter here: the address a user sets on
// their own account can only ever affect their own alerts (it must not be a way
// to read or change anyone else's), and a rule's recipient list must never
// silently become "nobody" or "everybody".

func TestValidEmail(t *testing.T) {
	for _, ok := range []string{
		"a@b.co", "first.last@example.com", "ops+alerts@example.co.uk", "x@y.z.dev",
	} {
		if !validEmail(ok) {
			t.Errorf("%q should be accepted", ok)
		}
	}
	for _, bad := range []string{
		"", "plain", "@example.com", "user@", "user@host", // no dot in the domain
		"a b@example.com", "a@b.com, c@d.com", // a list, not an address
		"a@@example.com", "a@example.com.", "<a@example.com>",
	} {
		if validEmail(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestCleanEmails(t *testing.T) {
	got := cleanEmails([]string{" ops@example.com ", "", "nonsense", "dev@example.com", "  "})
	if len(got) != 2 || got[0] != "ops@example.com" || got[1] != "dev@example.com" {
		t.Errorf("cleanEmails = %v, want the two valid addresses trimmed", got)
	}
	if got := cleanEmails(nil); len(got) != 0 {
		t.Errorf("nil should stay empty, got %v", got)
	}
}

func setMyEmail(t *testing.T, srv *Server, uid int64, role, email string) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"email": email})
	r := httptest.NewRequest("PUT", "/api/auth/me/email", strings.NewReader(string(raw))).
		WithContext(ctxAs(uid, role))
	w := httptest.NewRecorder()
	srv.handleSetMyEmail(w, r)
	return w
}

// PENTEST: the endpoint writes the address of the CALLER, taken from their session
// claims — a body naming another account must not be able to redirect that
// account's alerts, and no id is accepted from the request at all.
func TestPentestSetMyEmail_OnlyAffectsTheCaller(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	srv := &Server{cfg: config.Config{}, store: st}

	victim, _ := st.CreateUser(ctx, &store.User{Username: "victim", Role: "user", Email: "victim@example.com"})
	attacker, _ := st.CreateUser(ctx, &store.User{Username: "attacker", Role: "user"})

	// A body carrying someone else's id is simply not part of the contract; the
	// caller's own claims decide. Send one anyway and check nothing moves.
	raw := `{"email":"attacker@evil.test","id":` + itoa(victim) + `,"userId":` + itoa(victim) + `}`
	r := httptest.NewRequest("PUT", "/api/auth/me/email", strings.NewReader(raw)).
		WithContext(ctxAs(attacker, "user"))
	w := httptest.NewRecorder()
	srv.handleSetMyEmail(w, r)
	// decodeJSON uses DisallowUnknownFields, so the extra keys are rejected outright.
	if w.Code == 200 {
		t.Errorf("a body with unknown fields should be refused, got 200")
	}

	v, _ := st.UserByID(ctx, victim)
	if v.Email != "victim@example.com" {
		t.Errorf("SECURITY: the victim's address changed to %q", v.Email)
	}

	// The legitimate call sets only the caller's own address.
	if code := setMyEmail(t, srv, attacker, "user", "attacker@example.com").Code; code != 200 {
		t.Fatalf("setting one's own address = %d", code)
	}
	a, _ := st.UserByID(ctx, attacker)
	if a.Email != "attacker@example.com" {
		t.Errorf("own address = %q, want it saved", a.Email)
	}
	if v, _ = st.UserByID(ctx, victim); v.Email != "victim@example.com" {
		t.Errorf("SECURITY: the victim's address changed to %q", v.Email)
	}
}

func TestSetMyEmail_ValidationAndClearing(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	srv := &Server{cfg: config.Config{}, store: st}
	uid, _ := st.CreateUser(ctx, &store.User{Username: "u", Role: "user"})

	if code := setMyEmail(t, srv, uid, "user", "not-an-address").Code; code != 400 {
		t.Errorf("a malformed address should be 400, got %d", code)
	}
	if u, _ := st.UserByID(ctx, uid); u.Email != "" {
		t.Errorf("nothing should have been stored, got %q", u.Email)
	}

	if code := setMyEmail(t, srv, uid, "user", "  ops@example.com  ").Code; code != 200 {
		t.Fatal("a valid address should be accepted")
	}
	if u, _ := st.UserByID(ctx, uid); u.Email != "ops@example.com" {
		t.Errorf("address = %q, want it trimmed", u.Email)
	}

	// Empty clears it — that's how a user opts out of prefilled recipients.
	if code := setMyEmail(t, srv, uid, "user", "").Code; code != 200 {
		t.Fatal("clearing should be allowed")
	}
	if u, _ := st.UserByID(ctx, uid); u.Email != "" {
		t.Errorf("address should be cleared, got %q", u.Email)
	}
}

// A read-only account may still set its own address: it changes nothing about
// what the account can do, only where its own notifications land.
func TestSetMyEmail_ReadOnlyAccountAllowed(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	srv := &Server{cfg: config.Config{}, store: st}
	uid, _ := st.CreateUser(ctx, &store.User{Username: "ro", Role: "user", ReadOnly: true})

	if code := setMyEmail(t, srv, uid, "user", "ro@example.com").Code; code != 200 {
		t.Errorf("a read-only account should be able to set its own address, got %d", code)
	}
}

// PENTEST: the access overview must expose only the caller's own roles and
// grants. It is ungated, so if it could be steered at another account it would
// leak the whole permission map of the installation.
func TestPentestMyAccess_OnlyOwnData(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	srv := &Server{cfg: config.Config{}, store: st}

	privileged, _ := st.CreateUser(ctx, &store.User{Username: "ops", Role: "user"})
	plain, _ := st.CreateUser(ctx, &store.User{Username: "dev", Role: "user", Sections: []string{"logs"}})
	secretRole, err := st.CreateRole(ctx, &store.Role{
		Name: "HostsAdmin", Sections: []store.RoleSection{{Section: "hosts", Write: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserRoles(ctx, privileged, []int64{secretRole}); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/api/auth/me/access", nil).WithContext(ctxAs(plain, "user"))
	w := httptest.NewRecorder()
	srv.handleMyAccess(w, r)
	if w.Code != 200 {
		t.Fatalf("own access = %d (%s)", w.Code, w.Body.String())
	}
	// The other user's ROLE NAME must not appear anywhere. The "hosts" section is
	// checked structurally below rather than by substring: since host scoping
	// shipped, the payload legitimately contains a "hosts" key of its own, and a
	// substring match on it would fail for the wrong reason.
	if body := w.Body.String(); strings.Contains(body, "HostsAdmin") {
		t.Errorf("SECURITY: another user's role leaked into the response: %s", body)
	}

	var got struct {
		Admin     bool `json:"admin"`
		Effective []struct {
			Section string   `json:"section"`
			Write   bool     `json:"write"`
			From    []string `json:"from"`
		} `json:"effective"`
		Roles []map[string]any `json:"roles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Admin {
		t.Error("a plain user must not be reported as admin")
	}
	if len(got.Roles) != 0 {
		t.Errorf("this user holds no roles, got %v", got.Roles)
	}
	if len(got.Effective) != 1 || got.Effective[0].Section != "logs" {
		t.Fatalf("effective = %+v, want just logs — the other user's hosts grant must not appear", got.Effective)
	}
	// Provenance is the point of the endpoint: say where the grant came from.
	if len(got.Effective[0].From) != 1 || got.Effective[0].From[0] != "your account" {
		t.Errorf("provenance = %v, want [\"your account\"]", got.Effective[0].From)
	}
}

// A grant reaching the user through a role names that role, so the profile page
// can explain it.
func TestMyAccess_NamesTheRoleAGrantCameFrom(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	srv := &Server{cfg: config.Config{}, store: st}

	uid, _ := st.CreateUser(ctx, &store.User{Username: "u", Role: "user"})
	roleID, _ := st.CreateRole(ctx, &store.Role{
		Name: "Deployer", Sections: []store.RoleSection{{Section: "projects", Write: true}},
	})
	_ = st.SetUserRoles(ctx, uid, []int64{roleID})

	r := httptest.NewRequest("GET", "/api/auth/me/access", nil).WithContext(ctxAs(uid, "user"))
	w := httptest.NewRecorder()
	srv.handleMyAccess(w, r)

	var got struct {
		Effective []struct {
			Section string   `json:"section"`
			Write   bool     `json:"write"`
			From    []string `json:"from"`
		} `json:"effective"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Effective) != 1 || got.Effective[0].Section != "projects" || !got.Effective[0].Write {
		t.Fatalf("effective = %+v", got.Effective)
	}
	if len(got.Effective[0].From) != 1 || got.Effective[0].From[0] != "Deployer" {
		t.Errorf("provenance = %v, want the role name", got.Effective[0].From)
	}
}
