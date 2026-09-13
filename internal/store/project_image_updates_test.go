package store

import "testing"

func TestProjectImageUpdateStateCRUD(t *testing.T) {
	s, ctx := newStore(t)
	pid, err := s.CreateProject(ctx, &Project{Name: "App", Slug: "app", CreatedBy: "admin"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	digests, err := s.LastNotifiedImageDigests(ctx, pid)
	if err != nil || len(digests) != 0 {
		t.Fatalf("fresh project should have no notified digests, got %+v (err %v)", digests, err)
	}

	if err := s.SetLastNotifiedImageDigest(ctx, pid, "web", "sha256:aaa"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.SetLastNotifiedImageDigest(ctx, pid, "worker", "sha256:bbb"); err != nil {
		t.Fatalf("set: %v", err)
	}
	digests, err = s.LastNotifiedImageDigests(ctx, pid)
	if err != nil || digests["web"] != "sha256:aaa" || digests["worker"] != "sha256:bbb" {
		t.Fatalf("unexpected digests: %+v (err %v)", digests, err)
	}

	// Overwrites in place — the whole point is dedup, not history.
	if err := s.SetLastNotifiedImageDigest(ctx, pid, "web", "sha256:ccc"); err != nil {
		t.Fatalf("update: %v", err)
	}
	digests, _ = s.LastNotifiedImageDigests(ctx, pid)
	if digests["web"] != "sha256:ccc" || len(digests) != 2 {
		t.Fatalf("update should overwrite in place, got %+v", digests)
	}
}

func TestDeleteProjectCascadesImageUpdateState(t *testing.T) {
	s, ctx := newStore(t)
	pid, err := s.CreateProject(ctx, &Project{Name: "App", Slug: "app", CreatedBy: "admin"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.SetLastNotifiedImageDigest(ctx, pid, "web", "sha256:aaa"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(ctx, pid); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	digests, err := s.LastNotifiedImageDigests(ctx, pid)
	if err != nil || len(digests) != 0 {
		t.Fatalf("expected no image-update state after project delete, got %+v (err %v)", digests, err)
	}
}
