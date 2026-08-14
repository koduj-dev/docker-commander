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
	if err := s.UpdateProjectSettings(ctx, id, "Local renamed", 3, false); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ProjectByID(ctx, id)
	if got.Name != "Local renamed" || got.HostID != 3 {
		t.Errorf("update name+host: %+v", got)
	}
	if got.AllowRemoteHostPaths {
		t.Error("the host-path opt-in must default to off")
	}

	// The opt-in round-trips, and is independent of the name/host.
	if err := s.UpdateProjectSettings(ctx, id, "Local renamed", 3, true); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.ProjectByID(ctx, id); !got.AllowRemoteHostPaths {
		t.Error("the host-path opt-in did not persist")
	}
	// And it survives a listing, not just a by-id read.
	list, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		if p.ID == id && !p.AllowRemoteHostPaths {
			t.Error("the opt-in is missing from ListProjects")
		}
	}
	if err := s.UpdateProjectSettings(ctx, id, "Local renamed", 3, false); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.ProjectByID(ctx, id); got.AllowRemoteHostPaths {
		t.Error("the opt-in could not be turned back off")
	}
}

func TestProjectLastDeployedProfiles(t *testing.T) {
	s, ctx := newStore(t)

	id, err := s.CreateProject(ctx, &Project{Name: "App", Slug: "app"})
	if err != nil {
		t.Fatal(err)
	}

	// Never deployed: nil/empty, not an error, not a placeholder string.
	got, err := s.ProjectByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.LastDeployedProfiles) != 0 {
		t.Errorf("expected no profiles before any deploy, got %v", got.LastDeployedProfiles)
	}

	// A deploy with profiles round-trips through ProjectByID...
	if err := s.SetLastDeployedProfiles(ctx, id, []string{"prod", "cache"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.ProjectByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.LastDeployedProfiles) != 2 || got.LastDeployedProfiles[0] != "prod" || got.LastDeployedProfiles[1] != "cache" {
		t.Errorf("unexpected profiles after set: %v", got.LastDeployedProfiles)
	}

	// ...and through ListProjects, not just a by-id read.
	list, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range list {
		if p.ID == id {
			found = true
			if len(p.LastDeployedProfiles) != 2 {
				t.Errorf("ListProjects lost the profiles: %v", p.LastDeployedProfiles)
			}
		}
	}
	if !found {
		t.Fatal("project missing from ListProjects")
	}

	// A later deploy with no profiles clears the previous set — it must not
	// linger and misrepresent what's actually running.
	if err := s.SetLastDeployedProfiles(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.ProjectByID(ctx, id); len(got.LastDeployedProfiles) != 0 {
		t.Errorf("expected profiles cleared, got %v", got.LastDeployedProfiles)
	}
}
