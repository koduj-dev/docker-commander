package api

import (
	"context"
	"slices"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// A section granted only through a role must appear in the menu. The server
// already let such a user in (checkAccess reads EffectiveGrants), but the menu
// read the account's own section list, so the page had no way to reach it.
func TestMenuShowsSectionsGrantedByARole(t *testing.T) {
	srv, _, u := scopedFixture(t, "images", 7)
	menu := srv.effectiveSections(context.Background(), u)
	if !slices.Contains(menu, "images") {
		t.Fatalf("a section granted by a role is missing from the menu: %v", menu)
	}
}

// The menu and the server must agree in both directions: every section shown
// is one checkAccess lets the user read on the role's host, and every section it
// lets them read is shown. A menu wider than the grants would advertise pages
// that then refuse; narrower is the bug this fixes.
func TestMenuMatchesWhatTheServerAllows(t *testing.T) {
	srv, st, u := scopedFixture(t, "volumes", 7)
	ctx := context.Background()
	// Plus one per-account section, so both sources of a grant are covered.
	if err := st.UpdateUserAccess(ctx, u.ID, u.Role, false, []string{"logs"}); err != nil {
		t.Fatal(err)
	}
	u, err := st.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}

	menu := srv.effectiveSections(ctx, u)
	for _, sec := range store.Sections {
		allowed := srv.checkAccess(ctx, u, sec, false, 7) == nil
		if allowed != slices.Contains(menu, sec) {
			t.Errorf("section %q: server allows=%v, menu shows=%v", sec, allowed, slices.Contains(menu, sec))
		}
	}
}

// PENTEST: a role cannot put a section an admin disabled app-wide back into the
// menu, and an account with no grants gets an empty menu.
func TestPentestMenuDoesNotWidenPastGrants(t *testing.T) {
	srv, st, u := scopedFixture(t, "images", 7)
	ctx := context.Background()
	if err := st.SetDisabledSections(ctx, []string{"images"}); err != nil {
		t.Fatal(err)
	}
	if menu := srv.effectiveSections(ctx, u); slices.Contains(menu, "images") {
		t.Errorf("SECURITY: a disabled section came back through a role: %v", menu)
	}

	nobody, err := st.CreateUser(ctx, &store.User{Username: "nobody", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	nu, err := st.UserByID(ctx, nobody)
	if err != nil {
		t.Fatal(err)
	}
	if menu := srv.effectiveSections(ctx, nu); len(menu) != 0 {
		t.Errorf("SECURITY: an account with no grants sees menu sections: %v", menu)
	}
}

// PENTEST: when the grants can't be loaded, the menu fails closed: empty, never
// every enabled section.
func TestPentestMenuFailsClosedOnAStoreError(t *testing.T) {
	srv, st, u := scopedFixture(t, "images", 7)
	st.Close()
	if menu := srv.effectiveSections(context.Background(), u); len(menu) != 0 {
		t.Fatalf("SECURITY: a store error produced a menu: %v", menu)
	}
}
