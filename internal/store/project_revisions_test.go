package store

import "testing"

func TestCreateRevision_NumbersSequentiallyPerProject(t *testing.T) {
	s, ctx := openStore(t)
	pid, err := s.CreateProject(ctx, &Project{Name: "app", Slug: "app", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}

	r1 := &ProjectRevision{ProjectID: pid, Author: "alice", Valid: true}
	if _, err := s.CreateRevision(ctx, r1); err != nil {
		t.Fatal(err)
	}
	if r1.Revision != 1 {
		t.Errorf("first revision = %d, want 1", r1.Revision)
	}

	r2 := &ProjectRevision{ProjectID: pid, Author: "bob", Valid: true}
	if _, err := s.CreateRevision(ctx, r2); err != nil {
		t.Fatal(err)
	}
	if r2.Revision != 2 {
		t.Errorf("second revision = %d, want 2", r2.Revision)
	}
}

// A second, unrelated project must start its own revision numbering at 1 —
// revision numbers are per-project, not a shared sequence.
func TestCreateRevision_NumberingIsPerProject(t *testing.T) {
	s, ctx := openStore(t)
	p1, err := s.CreateProject(ctx, &Project{Name: "app1", Slug: "app1", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.CreateProject(ctx, &Project{Name: "app2", Slug: "app2", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRevision(ctx, &ProjectRevision{ProjectID: p1, Valid: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRevision(ctx, &ProjectRevision{ProjectID: p1, Valid: true}); err != nil {
		t.Fatal(err)
	}
	r := &ProjectRevision{ProjectID: p2, Valid: true}
	if _, err := s.CreateRevision(ctx, r); err != nil {
		t.Fatal(err)
	}
	if r.Revision != 1 {
		t.Errorf("project 2's first revision = %d, want 1", r.Revision)
	}
}

func TestListRevisions_NewestFirst(t *testing.T) {
	s, ctx := openStore(t)
	pid, err := s.CreateProject(ctx, &Project{Name: "app", Slug: "app", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.CreateRevision(ctx, &ProjectRevision{ProjectID: pid, Valid: true}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListRevisions(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d revisions, want 3", len(list))
	}
	if list[0].Revision != 3 || list[2].Revision != 1 {
		t.Errorf("expected newest-first order, got revisions %d, %d, %d", list[0].Revision, list[1].Revision, list[2].Revision)
	}
}

func TestRevisionByNumberAndLatest(t *testing.T) {
	s, ctx := openStore(t)
	pid, err := s.CreateProject(ctx, &Project{Name: "app", Slug: "app", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	profiles := []string{"extra"}
	images := []RevisionImage{{Service: "web", Image: "nginx:1.25", Digest: "sha256:abc"}}
	rev := &ProjectRevision{
		ProjectID: pid, HostID: 2, Profiles: profiles, Images: images,
		Valid: true, Output: "up and running", Author: "alice", Reason: "initial deploy",
	}
	if _, err := s.CreateRevision(ctx, rev); err != nil {
		t.Fatal(err)
	}

	got, err := s.RevisionByNumber(ctx, pid, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.HostID != 2 || got.Author != "alice" || got.Reason != "initial deploy" || got.Output != "up and running" {
		t.Errorf("got = %+v", got)
	}
	if len(got.Profiles) != 1 || got.Profiles[0] != "extra" {
		t.Errorf("Profiles = %+v", got.Profiles)
	}
	if len(got.Images) != 1 || got.Images[0].Digest != "sha256:abc" {
		t.Errorf("Images = %+v", got.Images)
	}

	if _, err := s.RevisionByNumber(ctx, pid, 99); !isNotFound(err) {
		t.Errorf("unknown revision number should be ErrNotFound, got %v", err)
	}

	latest, err := s.LatestRevision(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Revision != 1 {
		t.Errorf("LatestRevision = %d, want 1", latest.Revision)
	}
}

func TestLatestRevision_NeverDeployedIsNotFound(t *testing.T) {
	s, ctx := openStore(t)
	pid, err := s.CreateProject(ctx, &Project{Name: "app", Slug: "app", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LatestRevision(ctx, pid); !isNotFound(err) {
		t.Errorf("a never-deployed project should report ErrNotFound, got %v", err)
	}
}

// DeleteProject must clean up revision rows too — otherwise they accumulate
// forever as orphans nothing will ever read again.
func TestDeleteProject_RemovesRevisions(t *testing.T) {
	s, ctx := openStore(t)
	pid, err := s.CreateProject(ctx, &Project{Name: "app", Slug: "app", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRevision(ctx, &ProjectRevision{ProjectID: pid, Valid: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(ctx, pid); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_revisions WHERE project_id = ?`, pid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected revisions to be removed with the project, found %d", n)
	}
}

func isNotFound(err error) bool { return err == ErrNotFound }
