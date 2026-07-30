package store

import (
	"context"
	"errors"
	"sort"
	"testing"
)

func openStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, context.Background()
}

func grantsOf(t *testing.T, s *Store, u *User) map[string]Grant {
	t.Helper()
	g, err := s.EffectiveGrants(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestBuiltinRolesSeeded(t *testing.T) {
	s, ctx := openStore(t)
	roles, err := s.ListRoles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Role{}
	for _, r := range roles {
		byName[r.Name] = r
	}
	viewer, ok := byName[BuiltinViewer]
	if !ok {
		t.Fatalf("%s was not seeded; got %v", BuiltinViewer, byName)
	}
	if !viewer.Builtin {
		t.Error("Viewer should be flagged built-in")
	}
	if len(viewer.Sections) != len(Sections) {
		t.Errorf("Viewer should cover all %d sections, got %d", len(Sections), len(viewer.Sections))
	}
	for _, rs := range viewer.Sections {
		if rs.Write {
			t.Errorf("Viewer must be read-only, but %q is writable", rs.Section)
		}
	}

	op, ok := byName[BuiltinOperator]
	if !ok {
		t.Fatalf("%s was not seeded", BuiltinOperator)
	}
	if !op.Builtin {
		t.Error("Operator should be flagged built-in")
	}
	granted := map[string]bool{}
	for _, rs := range op.Sections {
		granted[rs.Section] = true
		if !rs.Write {
			t.Errorf("Operator's %q should be writable", rs.Section)
		}
	}
	// The authority sections are deliberately excluded.
	for _, sec := range []string{"hosts", "registries", "audit"} {
		if granted[sec] {
			t.Errorf("Operator must not grant %q by default", sec)
		}
	}
	for _, sec := range []string{"containers", "projects", "images", "alerts"} {
		if !granted[sec] {
			t.Errorf("Operator should grant %q", sec)
		}
	}
	// Built-ins sort ahead of user roles.
	if len(roles) < 2 || !roles[0].Builtin {
		t.Error("built-in roles should be listed first")
	}
}

// Seeding runs on every open; it must not reset an operator's edits or duplicate
// the rows.
func TestBuiltinRolesSeedingIsIdempotent(t *testing.T) {
	s, ctx := openStore(t)
	before, err := s.ListRoles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.seedBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.seedBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := s.ListRoles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("re-seeding duplicated roles: %d → %d", len(before), len(after))
	}
}

func TestRoleCRUD(t *testing.T) {
	s, ctx := openStore(t)

	id, err := s.CreateRole(ctx, &Role{
		Name: "Deployer", Description: "projects only",
		Sections: []RoleSection{{Section: "projects", Write: true}, {Section: "containers"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.RoleByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Builtin {
		t.Error("a created role must not be flagged built-in")
	}
	if len(r.Sections) != 2 {
		t.Fatalf("expected 2 grants, got %+v", r.Sections)
	}
	// Sections come back sorted, so containers precedes projects.
	if r.Sections[0].Section != "containers" || r.Sections[0].Write {
		t.Errorf("containers should be read-only: %+v", r.Sections[0])
	}
	if r.Sections[1].Section != "projects" || !r.Sections[1].Write {
		t.Errorf("projects should be writable: %+v", r.Sections[1])
	}

	if err := s.UpdateRole(ctx, id, "Deployer2", "renamed", []RoleSection{{Section: "images", Write: true}}); err != nil {
		t.Fatal(err)
	}
	r, _ = s.RoleByID(ctx, id)
	if r.Name != "Deployer2" || r.Description != "renamed" {
		t.Errorf("rename did not apply: %+v", r)
	}
	if len(r.Sections) != 1 || r.Sections[0].Section != "images" {
		t.Errorf("update should REPLACE grants, got %+v", r.Sections)
	}

	if err := s.DeleteRole(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RoleByID(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted role should be gone, got %v", err)
	}
}

func TestCreateRole_DuplicateName(t *testing.T) {
	s, ctx := openStore(t)
	if _, err := s.CreateRole(ctx, &Role{Name: "Dup"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRole(ctx, &Role{Name: "Dup"}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate role name should be ErrDuplicate, got %v", err)
	}
	if _, err := s.CreateRole(ctx, &Role{Name: "  "}); err == nil {
		t.Error("a blank role name should be refused")
	}
}

// A payload naming a section that doesn't exist must not create a grant — that
// would be an unenforceable permission sitting in the database.
func TestRoleSections_UnknownSectionsDropped(t *testing.T) {
	s, ctx := openStore(t)
	id, err := s.CreateRole(ctx, &Role{Name: "Odd", Sections: []RoleSection{
		{Section: "containers", Write: true},
		{Section: "not-a-section", Write: true},
		{Section: "", Write: true},
		{Section: "containers", Write: false}, // duplicate: first wins
	}})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := s.RoleByID(ctx, id)
	if len(r.Sections) != 1 || r.Sections[0].Section != "containers" || !r.Sections[0].Write {
		t.Errorf("expected only the valid containers grant, got %+v", r.Sections)
	}
}

// PENTEST: built-in roles are the known-good baseline. Editing or deleting them
// through the store must be refused, so a compromised or buggy caller can't
// quietly turn Viewer into a writable role for everyone holding it.
func TestPentestBuiltinRolesImmutable(t *testing.T) {
	s, ctx := openStore(t)
	roles, _ := s.ListRoles(ctx)
	var viewer Role
	for _, r := range roles {
		if r.Name == BuiltinViewer {
			viewer = r
		}
	}
	if viewer.ID == 0 {
		t.Fatal("Viewer not found")
	}

	err := s.UpdateRole(ctx, viewer.ID, "Pwned", "", []RoleSection{{Section: "containers", Write: true}})
	if !errors.Is(err, ErrBuiltinRole) {
		t.Errorf("SECURITY: editing a built-in role returned %v, want ErrBuiltinRole", err)
	}
	if err := s.DeleteRole(ctx, viewer.ID); !errors.Is(err, ErrBuiltinRole) {
		t.Errorf("SECURITY: deleting a built-in role returned %v, want ErrBuiltinRole", err)
	}
	// Unchanged afterwards.
	again, _ := s.RoleByID(ctx, viewer.ID)
	if again.Name != BuiltinViewer {
		t.Errorf("SECURITY: the built-in role was modified: %+v", again)
	}
	for _, rs := range again.Sections {
		if rs.Write {
			t.Errorf("SECURITY: Viewer gained write on %q", rs.Section)
		}
	}
}

func TestSetUserRoles(t *testing.T) {
	s, ctx := openStore(t)
	uid, err := s.CreateUser(ctx, &User{Username: "u", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.CreateRole(ctx, &Role{Name: "A", Sections: []RoleSection{{Section: "containers", Write: true}}})
	b, _ := s.CreateRole(ctx, &Role{Name: "B", Sections: []RoleSection{{Section: "images"}}})

	// Duplicates and unknown ids are dropped, not errors.
	if err := s.SetUserRoles(ctx, uid, []int64{a, b, a, 9999, 0, -1}); err != nil {
		t.Fatal(err)
	}
	ids, _ := s.RoleIDsForUser(ctx, uid)
	if len(ids) != 2 {
		t.Fatalf("expected 2 assignments, got %v", ids)
	}
	// Replacement, not accumulation.
	if err := s.SetUserRoles(ctx, uid, []int64{b}); err != nil {
		t.Fatal(err)
	}
	if ids, _ = s.RoleIDsForUser(ctx, uid); len(ids) != 1 || ids[0] != b {
		t.Errorf("SetUserRoles should replace, got %v", ids)
	}
	// Deleting a role drops its assignments too, so no dangling grants remain.
	if err := s.DeleteRole(ctx, b); err != nil {
		t.Fatal(err)
	}
	if ids, _ = s.RoleIDsForUser(ctx, uid); len(ids) != 0 {
		t.Errorf("deleting a role should remove its assignments, got %v", ids)
	}
}

func TestEffectiveGrants_UnionOfRolesAndUserSections(t *testing.T) {
	s, ctx := openStore(t)
	uid, _ := s.CreateUser(ctx, &User{Username: "u", Role: "user", Sections: []string{"logs"}})
	roleID, _ := s.CreateRole(ctx, &Role{Name: "Mixed", Sections: []RoleSection{
		{Section: "containers", Write: true},
		{Section: "images", Write: false},
	}})
	if err := s.SetUserRoles(ctx, uid, []int64{roleID}); err != nil {
		t.Fatal(err)
	}
	u, _ := s.UserByID(ctx, uid)

	g := grantsOf(t, s, u)
	if !g["logs"].Granted || !g["logs"].Write {
		t.Errorf("a per-user section should grant write for a non-read-only account: %+v", g["logs"])
	}
	if !g["containers"].Granted || !g["containers"].Write {
		t.Errorf("the role's writable section should grant write: %+v", g["containers"])
	}
	if !g["images"].Granted || g["images"].Write {
		t.Errorf("the role's read-only section should not grant write: %+v", g["images"])
	}
	if g["hosts"].Granted {
		t.Error("hosts was never granted and must not appear")
	}

	secs, err := s.EffectiveSections(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(secs)
	if len(secs) != 3 {
		t.Errorf("expected 3 effective sections, got %v", secs)
	}
}

// Two roles granting the same section with different write bits: the union wins,
// so the more permissive role decides.
func TestEffectiveGrants_WriteIsUnionAcrossRoles(t *testing.T) {
	s, ctx := openStore(t)
	uid, _ := s.CreateUser(ctx, &User{Username: "u", Role: "user"})
	ro, _ := s.CreateRole(ctx, &Role{Name: "RO", Sections: []RoleSection{{Section: "containers"}}})
	rw, _ := s.CreateRole(ctx, &Role{Name: "RW", Sections: []RoleSection{{Section: "containers", Write: true}}})
	_ = s.SetUserRoles(ctx, uid, []int64{ro, rw})
	u, _ := s.UserByID(ctx, uid)

	if g := grantsOf(t, s, u)["containers"]; !g.Granted || !g.Write {
		t.Errorf("the writable role should win the union: %+v", g)
	}
}

// PENTEST: the account-level read-only flag is the hard cap. A writable role must
// not restore write access to a read-only account — that's the whole point of the
// flag, and D2's migration mapping depends on it.
func TestPentestEffectiveGrants_ReadOnlyAccountCapsRoles(t *testing.T) {
	s, ctx := openStore(t)
	uid, _ := s.CreateUser(ctx, &User{
		Username: "ro", Role: "user", ReadOnly: true, Sections: []string{"projects"},
	})
	roleID, _ := s.CreateRole(ctx, &Role{Name: "Writer", Sections: []RoleSection{
		{Section: "containers", Write: true}, {Section: "images", Write: true},
	}})
	_ = s.SetUserRoles(ctx, uid, []int64{roleID})
	u, _ := s.UserByID(ctx, uid)

	for sec, g := range grantsOf(t, s, u) {
		if g.Write {
			t.Errorf("SECURITY: read-only account has write on %q via a role", sec)
		}
	}
	// It still *reads* everything it was granted.
	g := grantsOf(t, s, u)
	for _, sec := range []string{"projects", "containers", "images"} {
		if !g[sec].Granted {
			t.Errorf("read-only account should still be granted %q", sec)
		}
	}
}

// PENTEST: an app-wide disabled section is off for everyone, so a role must not
// be a way around a feature flag.
func TestPentestEffectiveGrants_DisabledSectionBeatsRole(t *testing.T) {
	s, ctx := openStore(t)
	uid, _ := s.CreateUser(ctx, &User{Username: "u", Role: "user", Sections: []string{"images"}})
	roleID, _ := s.CreateRole(ctx, &Role{Name: "All", Sections: []RoleSection{
		{Section: "containers", Write: true}, {Section: "images", Write: true},
	}})
	_ = s.SetUserRoles(ctx, uid, []int64{roleID})
	if err := s.SetDisabledSections(ctx, []string{"containers", "images"}); err != nil {
		t.Fatal(err)
	}
	u, _ := s.UserByID(ctx, uid)

	g := grantsOf(t, s, u)
	if g["containers"].Granted {
		t.Error("SECURITY: a disabled section was granted through a role")
	}
	if g["images"].Granted {
		t.Error("SECURITY: a disabled section was granted through the per-user list")
	}
}

// A user with no roles must behave exactly as before roles existed — this is the
// backwards-compatibility guarantee the migration rests on.
func TestEffectiveGrants_NoRolesMatchesLegacyBehaviour(t *testing.T) {
	s, ctx := openStore(t)
	uid, _ := s.CreateUser(ctx, &User{
		Username: "legacy", Role: "user", Sections: []string{"containers", "logs"},
	})
	u, _ := s.UserByID(ctx, uid)

	g := grantsOf(t, s, u)
	if len(g) != 2 {
		t.Fatalf("expected exactly the two per-user sections, got %v", g)
	}
	for _, sec := range []string{"containers", "logs"} {
		if !g[sec].Granted || !g[sec].Write {
			t.Errorf("%q should be granted and writable: %+v", sec, g[sec])
		}
	}
}

func TestEffectiveGrants_NilUser(t *testing.T) {
	s, _ := openStore(t)
	if g := grantsOf(t, s, nil); len(g) != 0 {
		t.Errorf("a nil user should have no grants, got %v", g)
	}
}
