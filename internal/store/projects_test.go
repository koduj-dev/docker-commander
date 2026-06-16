package store

import (
	"errors"
	"testing"
)

func TestProjectsCRUD(t *testing.T) {
	s, ctx := newStore(t)

	id, err := s.CreateProject(ctx, &Project{Name: "My App", Slug: "my-app", CreatedBy: "admin"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id == 0 {
		t.Fatal("expected a non-zero id")
	}

	// Slug is UNIQUE → a second insert with the same slug is ErrDuplicate.
	if _, err := s.CreateProject(ctx, &Project{Name: "Other", Slug: "my-app"}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate slug should be ErrDuplicate, got %v", err)
	}

	// ComposeFile defaults to compose.yml when blank.
	got, err := s.ProjectByID(ctx, id)
	if err != nil {
		t.Fatalf("by id: %v", err)
	}
	if got.Name != "My App" || got.Slug != "my-app" || got.ComposeFile != "compose.yml" {
		t.Errorf("unexpected row: %+v", got)
	}

	if _, err := s.ProjectByID(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing project should be ErrNotFound, got %v", err)
	}

	list, err := s.ListProjects(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: err=%v len=%d", err, len(list))
	}

	if err := s.DeleteProject(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if list, _ := s.ListProjects(ctx); len(list) != 0 {
		t.Errorf("expected no projects after delete, got %d", len(list))
	}
}

func TestProjectHostIDRoundTrip(t *testing.T) {
	s, ctx := newStore(t)

	// Defaults to local (0).
	id, err := s.CreateProject(ctx, &Project{Name: "Local", Slug: "local-app"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ProjectByID(ctx, id); got.HostID != 0 {
		t.Errorf("default host should be 0 (local), got %d", got.HostID)
	}

	// Created with an explicit host.
	id2, err := s.CreateProject(ctx, &Project{Name: "Remote", Slug: "remote-app", HostID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ProjectByID(ctx, id2); got.HostID != 7 {
		t.Errorf("host id not persisted on create: %d", got.HostID)
	}

	// Update retargets the host.
	if err := s.UpdateProjectName(ctx, id, "Local renamed", 3); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ProjectByID(ctx, id)
	if got.Name != "Local renamed" || got.HostID != 3 {
		t.Errorf("update name+host: %+v", got)
	}
}
