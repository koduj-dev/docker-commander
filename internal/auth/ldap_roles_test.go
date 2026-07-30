package auth

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Group→role mapping. A login is the one moment the directory gets to decide
// what an account may do, so these tests care less about the happy path than
// about what a login must NOT be able to hand out: admin, a role the user's
// groups don't grant, or a role that no longer exists.

func mapping(dn string, roles ...int64) store.LDAPGroupMapping {
	return store.LDAPGroupMapping{GroupDN: dn, RoleIDs: roles}
}

func ids(in []int64) []int64 {
	out := append([]int64(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func TestRolesForGroups_UnionAcrossGroups(t *testing.T) {
	cfg := store.LDAPConfig{GroupMappings: []store.LDAPGroupMapping{
		mapping("cn=ops,dc=example,dc=org", 1, 2),
		mapping("cn=devs,dc=example,dc=org", 2, 3),
		mapping("cn=other,dc=example,dc=org", 9),
	}}
	got := ids(RolesForGroups(cfg, []string{"cn=ops,dc=example,dc=org", "cn=devs,dc=example,dc=org"}))
	want := []int64{1, 2, 3}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("roles = %v, want %v (union, deduped, nothing from unjoined groups)", got, want)
	}
}

func TestRolesForGroups_CaseAndWhitespaceInsensitive(t *testing.T) {
	cfg := store.LDAPConfig{GroupMappings: []store.LDAPGroupMapping{mapping(" CN=Ops,DC=Example,DC=Org ", 7)}}
	if got := RolesForGroups(cfg, []string{"cn=ops,dc=example,dc=org"}); len(got) != 1 || got[0] != 7 {
		t.Errorf("roles = %v, want [7] — DNs match case-insensitively, like the admin group", got)
	}
}

// PENTEST: membership is exact-DN, not substring — a group whose name merely
// contains a mapped DN must not inherit its roles.
func TestPentestRolesForGroups_NoSubstringMatch(t *testing.T) {
	cfg := store.LDAPConfig{GroupMappings: []store.LDAPGroupMapping{mapping("cn=ops,dc=example,dc=org", 7)}}
	for _, g := range []string{
		"cn=ops,dc=example,dc=org,dc=evil", "cn=notops,dc=example,dc=org", "ops", "",
	} {
		if got := RolesForGroups(cfg, []string{g}); len(got) != 0 {
			t.Errorf("SECURITY: group %q was granted %v", g, got)
		}
	}
}

func TestMapsRoles(t *testing.T) {
	sectionsOnly := store.LDAPConfig{GroupMappings: []store.LDAPGroupMapping{
		{GroupDN: "cn=ops,dc=example,dc=org", Sections: []string{"containers"}},
	}}
	if MapsRoles(sectionsOnly) {
		t.Error("a config that hands out no role must not claim to map roles")
	}
	if MapsRoles(store.LDAPConfig{}) {
		t.Error("an empty config maps nothing")
	}
	if !MapsRoles(store.LDAPConfig{GroupMappings: []store.LDAPGroupMapping{mapping("cn=x", 1)}}) {
		t.Error("a mapping with a role id maps roles")
	}
}

// ldapFixture wires a service whose directory bind is stubbed, so the tests
// exercise the provisioning rules rather than a real LDAP server (that path has
// its own container-backed integration test).
func ldapFixture(t *testing.T, mappings []store.LDAPGroupMapping, groups []string, admin bool) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := store.LDAPConfig{
		Enabled: true, URL: "ldap://stub", UserBaseDN: "dc=example,dc=org",
		UserFilter: "(uid=%s)", GroupMappings: mappings,
	}
	if err := st.SetLDAP(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	svc := NewService(st, NewTokenManager([]byte("0123456789abcdef0123456789abcdef"), 0))
	svc.ldapAuth = func(_ store.LDAPConfig, username, _ string) (*LDAPResult, error) {
		return &LDAPResult{Username: username, IsAdmin: admin, Groups: groups}, nil
	}
	return svc, st
}

func login(t *testing.T, svc *Service, username string) *store.User {
	t.Helper()
	res, err := svc.Login(context.Background(), "k", username, "pw", false)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return res.User
}

func roleNames(t *testing.T, st *store.Store, uid int64) []string {
	t.Helper()
	roles, err := st.RolesForUser(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		out = append(out, r.Name)
	}
	sort.Strings(out)
	return out
}

func newRole(t *testing.T, st *store.Store, name string, sections ...store.RoleSection) int64 {
	t.Helper()
	id, err := st.CreateRole(context.Background(), &store.Role{Name: name, Sections: sections})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLDAPLoginGrantsMappedRolesOnFirstLogin(t *testing.T) {
	svc, st := ldapFixture(t, nil, []string{"cn=ops,dc=example,dc=org"}, false)
	ctx := context.Background()
	deployerID := newRole(t, st, "Deployer", store.RoleSection{Section: "projects", Write: true})
	// Point the mapping at the role now that it has an id.
	cfg, _ := st.GetLDAP(ctx)
	cfg.GroupMappings = []store.LDAPGroupMapping{mapping("cn=ops,dc=example,dc=org", deployerID)}
	if err := st.SetLDAP(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	u := login(t, svc, "alice")
	if got := roleNames(t, st, u.ID); len(got) != 1 || got[0] != "Deployer" {
		t.Errorf("roles = %v, want [Deployer] provisioned from the group", got)
	}
	if u.Role != "user" {
		t.Errorf("account role = %q, want user", u.Role)
	}
}

func TestLDAPLoginResyncsRolesOnEveryLogin(t *testing.T) {
	svc, st := ldapFixture(t, nil, []string{"cn=ops,dc=example,dc=org"}, false)
	ctx := context.Background()
	ops := newRole(t, st, "Ops", store.RoleSection{Section: "containers", Write: true})
	devs := newRole(t, st, "Devs", store.RoleSection{Section: "logs"})

	setMappings := func(m ...store.LDAPGroupMapping) {
		cfg, _ := st.GetLDAP(ctx)
		cfg.GroupMappings = m
		if err := st.SetLDAP(ctx, cfg); err != nil {
			t.Fatal(err)
		}
	}
	setMappings(mapping("cn=ops,dc=example,dc=org", ops), mapping("cn=devs,dc=example,dc=org", devs))

	u := login(t, svc, "alice")
	if got := roleNames(t, st, u.ID); len(got) != 1 || got[0] != "Ops" {
		t.Fatalf("roles = %v, want [Ops]", got)
	}

	// The directory moves alice into devs as well.
	svc.ldapAuth = func(_ store.LDAPConfig, username, _ string) (*LDAPResult, error) {
		return &LDAPResult{Username: username, Groups: []string{"cn=ops,dc=example,dc=org", "cn=devs,dc=example,dc=org"}}, nil
	}
	login(t, svc, "alice")
	if got := roleNames(t, st, u.ID); len(got) != 2 || got[0] != "Devs" || got[1] != "Ops" {
		t.Errorf("roles = %v, want [Devs Ops] after the group was added", got)
	}

	// ...and then out of both. Revocation must be immediate, not on expiry.
	svc.ldapAuth = func(_ store.LDAPConfig, username, _ string) (*LDAPResult, error) {
		return &LDAPResult{Username: username, Groups: []string{"cn=nobody,dc=example,dc=org"}}, nil
	}
	login(t, svc, "alice")
	if got := roleNames(t, st, u.ID); len(got) != 0 {
		t.Errorf("SECURITY: roles = %v, want none once the groups were removed", got)
	}
}

// PENTEST: a config written before group→role mapping existed grants sections
// only. Such a login must not strip roles an admin assigned by hand — silently
// dropping them on the next login would be an invisible outage, and re-granting
// them by hand each time is worse.
func TestPentestSectionOnlyMappingsLeaveHandAssignedRolesAlone(t *testing.T) {
	svc, st := ldapFixture(t,
		[]store.LDAPGroupMapping{{GroupDN: "cn=ops,dc=example,dc=org", Sections: []string{"containers"}}},
		[]string{"cn=ops,dc=example,dc=org"}, false)
	ctx := context.Background()
	roleID := newRole(t, st, "Deployer", store.RoleSection{Section: "projects", Write: true})

	u := login(t, svc, "alice")
	if err := st.SetUserRoles(ctx, u.ID, []int64{roleID}); err != nil {
		t.Fatal(err)
	}
	login(t, svc, "alice")
	if got := roleNames(t, st, u.ID); len(got) != 1 || got[0] != "Deployer" {
		t.Errorf("roles = %v, want the hand-assigned [Deployer] kept", got)
	}
	// Sections stay LDAP-driven, as they were before roles existed.
	if fresh, _ := st.UserByID(ctx, u.ID); len(fresh.Sections) != 1 || fresh.Sections[0] != "containers" {
		t.Errorf("sections = %v, want the mapped [containers]", fresh.Sections)
	}
}

// PENTEST: a role id that no longer names a role must grant nothing — and must
// not fail the login, or deleting a role would lock out every user of the groups
// that referenced it.
func TestPentestStaleRoleIDGrantsNothing(t *testing.T) {
	svc, st := ldapFixture(t, nil, []string{"cn=ops,dc=example,dc=org"}, false)
	ctx := context.Background()
	roleID := newRole(t, st, "Temp", store.RoleSection{Section: "logs"})
	cfg, _ := st.GetLDAP(ctx)
	cfg.GroupMappings = []store.LDAPGroupMapping{mapping("cn=ops,dc=example,dc=org", roleID, 4242)}
	if err := st.SetLDAP(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	u := login(t, svc, "alice")
	if got := roleNames(t, st, u.ID); len(got) != 1 || got[0] != "Temp" {
		t.Fatalf("roles = %v, want just [Temp] — the unknown id grants nothing", got)
	}

	// Now delete the role out from under the mapping.
	if err := st.DeleteRole(ctx, roleID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login(ctx, "k2", "alice", "pw", false); err != nil {
		t.Fatalf("SECURITY: a deleted role in a mapping locked the user out: %v", err)
	}
	if got := roleNames(t, st, u.ID); len(got) != 0 {
		t.Errorf("roles = %v, want none after the role was deleted", got)
	}
}

// PENTEST: group→role mapping hands out roles, never the admin flag. Only the
// configured admin group does that, and a role can't contain __admin.
func TestPentestGroupRoleMappingCannotGrantAdmin(t *testing.T) {
	svc, st := ldapFixture(t, nil, []string{"cn=ops,dc=example,dc=org"}, false)
	ctx := context.Background()
	// The most powerful role an admin could possibly build.
	var all []store.RoleSection
	for _, sec := range store.Sections {
		all = append(all, store.RoleSection{Section: sec, Write: true})
	}
	roleID := newRole(t, st, "Everything", all...)
	cfg, _ := st.GetLDAP(ctx)
	cfg.GroupMappings = []store.LDAPGroupMapping{mapping("cn=ops,dc=example,dc=org", roleID)}
	if err := st.SetLDAP(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	u := login(t, svc, "alice")
	fresh, _ := st.UserByID(ctx, u.ID)
	if fresh.Role == "admin" {
		t.Error("SECURITY: a group→role mapping provisioned an admin account")
	}
	grants, err := st.EffectiveGrants(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := grants["__admin"]; ok {
		t.Error("SECURITY: the admin pseudo-section was granted through a role")
	}
}

// setFallback points the config's fallback role at id.
func setFallback(t *testing.T, st *store.Store, id int64) {
	t.Helper()
	ctx := context.Background()
	cfg, _ := st.GetLDAP(ctx)
	cfg.FallbackRoleID = id
	if err := st.SetLDAP(ctx, cfg); err != nil {
		t.Fatal(err)
	}
}

// A mapped role that no longer exists degrades its members to the fallback role
// rather than to nothing — the point of having one.
func TestFallbackRoleStandsInForADeletedRole(t *testing.T) {
	svc, st := ldapFixture(t, nil, []string{"cn=ops,dc=example,dc=org"}, false)
	ctx := context.Background()
	temp := newRole(t, st, "Temp", store.RoleSection{Section: "containers", Write: true})
	viewer := newRole(t, st, "Baseline", store.RoleSection{Section: "dashboard"})
	cfg, _ := st.GetLDAP(ctx)
	cfg.GroupMappings = []store.LDAPGroupMapping{mapping("cn=ops,dc=example,dc=org", temp)}
	cfg.FallbackRoleID = viewer
	if err := st.SetLDAP(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	u := login(t, svc, "alice")
	if got := roleNames(t, st, u.ID); len(got) != 1 || got[0] != "Temp" {
		t.Fatalf("roles = %v, want [Temp] while the role exists", got)
	}

	// Delete Temp out from under the mapping: the group still matches, the role
	// no longer resolves, so the fallback stands in.
	if err := st.DeleteRole(ctx, temp); err != nil {
		t.Fatal(err)
	}
	login(t, svc, "alice")
	if got := roleNames(t, st, u.ID); len(got) != 1 || got[0] != "Baseline" {
		t.Errorf("roles = %v, want the fallback [Baseline]", got)
	}
}

// PENTEST: the fallback stands in for a BROKEN mapping, never for the ordinary
// "your groups grant you nothing" case. Applying it there would give a role to
// every account in the directory that can authenticate.
func TestPentestFallbackNotGrantedToUnmappedUsers(t *testing.T) {
	svc, st := ldapFixture(t, nil, []string{"cn=nobody,dc=example,dc=org"}, false)
	ctx := context.Background()
	ops := newRole(t, st, "Ops", store.RoleSection{Section: "containers", Write: true})
	baseline := newRole(t, st, "Baseline", store.RoleSection{Section: "dashboard"})
	cfg, _ := st.GetLDAP(ctx)
	cfg.GroupMappings = []store.LDAPGroupMapping{mapping("cn=ops,dc=example,dc=org", ops)}
	cfg.FallbackRoleID = baseline
	if err := st.SetLDAP(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	u := login(t, svc, "stranger")
	if got := roleNames(t, st, u.ID); len(got) != 0 {
		t.Errorf("SECURITY: a user in no mapped group was granted %v via the fallback", got)
	}
}

// The fallback also doesn't fire while every mapped role resolves — it isn't an
// extra grant piled on top of a working mapping.
func TestFallbackNotGrantedWhenMappingResolves(t *testing.T) {
	svc, st := ldapFixture(t, nil, []string{"cn=ops,dc=example,dc=org"}, false)
	ctx := context.Background()
	ops := newRole(t, st, "Ops", store.RoleSection{Section: "containers", Write: true})
	baseline := newRole(t, st, "Baseline", store.RoleSection{Section: "dashboard"})
	cfg, _ := st.GetLDAP(ctx)
	cfg.GroupMappings = []store.LDAPGroupMapping{mapping("cn=ops,dc=example,dc=org", ops)}
	cfg.FallbackRoleID = baseline
	if err := st.SetLDAP(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	u := login(t, svc, "alice")
	if got := roleNames(t, st, u.ID); len(got) != 1 || got[0] != "Ops" {
		t.Errorf("roles = %v, want just [Ops] — the fallback is for broken mappings only", got)
	}
}

// A fallback id that is itself dangling grants nothing and must not fail the
// login: the fallback is a safety net, not another way to lock people out.
func TestFallbackThatIsItselfMissingGrantsNothing(t *testing.T) {
	svc, st := ldapFixture(t, nil, []string{"cn=ops,dc=example,dc=org"}, false)
	ctx := context.Background()
	cfg, _ := st.GetLDAP(ctx)
	cfg.GroupMappings = []store.LDAPGroupMapping{mapping("cn=ops,dc=example,dc=org", 4242)}
	cfg.FallbackRoleID = 7777
	if err := st.SetLDAP(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	u := login(t, svc, "alice")
	if got := roleNames(t, st, u.ID); len(got) != 0 {
		t.Errorf("roles = %v, want none", got)
	}
}

// PENTEST: the fallback must not survive as a dangling id — deleting the role it
// points at is refused, so the hole it exists to close can't reopen one level up.
func TestPentestCannotDeleteTheConfiguredFallbackRole(t *testing.T) {
	_, st := ldapFixture(t, nil, nil, false)
	ctx := context.Background()
	baseline := newRole(t, st, "Baseline", store.RoleSection{Section: "dashboard"})
	setFallback(t, st, baseline)

	if err := st.DeleteRole(ctx, baseline); !errors.Is(err, store.ErrRoleInUseAsFallback) {
		t.Fatalf("deleting the fallback = %v, want ErrRoleInUseAsFallback", err)
	}
	if _, err := st.RoleByID(ctx, baseline); err != nil {
		t.Errorf("the role should still exist: %v", err)
	}
	// Pointing the fallback elsewhere releases it.
	setFallback(t, st, 0)
	if err := st.DeleteRole(ctx, baseline); err != nil {
		t.Errorf("deleting it after clearing the fallback = %v", err)
	}
}

// PENTEST: roles come from the groups the DIRECTORY reports, not from anything
// the user supplies. A login naming a different account must provision that
// account's own name, never inherit another user's mapping by claiming a group.
func TestPentestRolesFollowDirectoryGroupsOnly(t *testing.T) {
	svc, st := ldapFixture(t, nil, nil, false)
	ctx := context.Background()
	roleID := newRole(t, st, "Ops", store.RoleSection{Section: "hosts", Write: true})
	cfg, _ := st.GetLDAP(ctx)
	cfg.GroupMappings = []store.LDAPGroupMapping{mapping("cn=ops,dc=example,dc=org", roleID)}
	if err := st.SetLDAP(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	// The stub reports no groups at all, whatever the username looks like.
	u := login(t, svc, "cn=ops,dc=example,dc=org")
	if got := roleNames(t, st, u.ID); len(got) != 0 {
		t.Errorf("SECURITY: roles = %v for a user the directory placed in no group", got)
	}
}
