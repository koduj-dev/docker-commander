package store

import "testing"

func TestIgnoreDriftAndList(t *testing.T) {
	s, ctx := openStore(t)
	pid, err := s.CreateProject(ctx, &Project{Name: "app", Slug: "app", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.IgnoreDrift(ctx, pid, "web", "env"); err != nil {
		t.Fatal(err)
	}
	if err := s.IgnoreDrift(ctx, pid, "web", "restart"); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListDriftIgnores(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(list), list)
	}
}

// Re-ignoring an already-ignored (service, kind) must not error or duplicate
// the row — the caller's goal ("this drift is accepted") is already true.
func TestIgnoreDrift_Idempotent(t *testing.T) {
	s, ctx := openStore(t)
	pid, err := s.CreateProject(ctx, &Project{Name: "app", Slug: "app", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.IgnoreDrift(ctx, pid, "web", "env"); err != nil {
		t.Fatal(err)
	}
	if err := s.IgnoreDrift(ctx, pid, "web", "env"); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListDriftIgnores(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d entries, want 1 (no duplicate row): %+v", len(list), list)
	}
}

func TestUnignoreDrift(t *testing.T) {
	s, ctx := openStore(t)
	pid, err := s.CreateProject(ctx, &Project{Name: "app", Slug: "app", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.IgnoreDrift(ctx, pid, "web", "env"); err != nil {
		t.Fatal(err)
	}
	if err := s.IgnoreDrift(ctx, pid, "db", "restart"); err != nil {
		t.Fatal(err)
	}
	if err := s.UnignoreDrift(ctx, pid, "web", "env"); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListDriftIgnores(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Service != "db" {
		t.Errorf("list = %+v, want only db:restart left", list)
	}
	// Unignoring a pair never ignored (or already removed) is a no-op.
	if err := s.UnignoreDrift(ctx, pid, "nope", "nope"); err != nil {
		t.Errorf("unignoring an unknown pair should not error: %v", err)
	}
}

// Drift ignores are scoped per project — accepting "web:restart" on one
// project must never suppress the same drift on another.
func TestDriftIgnores_ScopedPerProject(t *testing.T) {
	s, ctx := openStore(t)
	p1, err := s.CreateProject(ctx, &Project{Name: "app1", Slug: "app1", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.CreateProject(ctx, &Project{Name: "app2", Slug: "app2", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.IgnoreDrift(ctx, p1, "web", "env"); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListDriftIgnores(ctx, p2)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("project 2 should have no ignores of its own, got %+v", list)
	}
}

// DeleteProject must clean up drift ignores — otherwise they accumulate
// forever as orphaned rows nothing will ever read again.
func TestDeleteProject_RemovesDriftIgnores(t *testing.T) {
	s, ctx := openStore(t)
	pid, err := s.CreateProject(ctx, &Project{Name: "app", Slug: "app", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.IgnoreDrift(ctx, pid, "web", "env"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(ctx, pid); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_drift_ignores WHERE project_id = ?`, pid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected drift ignores to be removed with the project, found %d", n)
	}
}
