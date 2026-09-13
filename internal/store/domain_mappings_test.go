package store

import (
	"encoding/json"
	"errors"
	"testing"
)

// A project with no mappings must get "[]" back, never "null" — the
// frontend's modal uses a nil vs. non-nil array specifically to distinguish
// "still loading" from "loaded, no mappings yet", so a nil slice here would
// leave the UI stuck on its loading spinner forever.
func TestListDomainMappingsEmptyIsNeverNil(t *testing.T) {
	s, ctx := newStore(t)
	pid, err := s.CreateProject(ctx, &Project{Name: "App", Slug: "app", CreatedBy: "admin"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	list, err := s.ListDomainMappings(ctx, pid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list == nil {
		t.Fatal("ListDomainMappings must return a non-nil empty slice, got nil")
	}
	b, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "[]" {
		t.Errorf("JSON encoding of an empty list = %q, want []", b)
	}
}

func TestDomainMappingsCRUD(t *testing.T) {
	s, ctx := newStore(t)
	pid, err := s.CreateProject(ctx, &Project{Name: "App", Slug: "app", CreatedBy: "admin"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	id, err := s.CreateDomainMapping(ctx, pid, "app.example.com", "web", 8080, "acme", "admin")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	list, err := s.ListDomainMappings(ctx, pid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Domain != "app.example.com" || list[0].Service != "web" || list[0].TargetPort != 8080 {
		t.Fatalf("unexpected list: %+v", list)
	}

	got, err := s.DomainMappingByDomain(ctx, "app.example.com")
	if err != nil || got.ProjectID != pid || got.ID != id {
		t.Fatalf("DomainMappingByDomain: %+v err=%v", got, err)
	}
	if _, err := s.DomainMappingByDomain(ctx, "nope.example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown domain should be ErrNotFound, got %v", err)
	}

	// Same domain again (even under a different project) -> ErrDuplicate: a
	// public hostname can only ever point at one place.
	pid2, err := s.CreateProject(ctx, &Project{Name: "App2", Slug: "app2", CreatedBy: "admin"})
	if err != nil {
		t.Fatalf("create project 2: %v", err)
	}
	if _, err := s.CreateDomainMapping(ctx, pid2, "app.example.com", "web", 8080, "acme", "admin"); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate domain (cross-project) should be ErrDuplicate, got %v", err)
	}

	// Update changes service/port/TLS mode, never the domain itself.
	if err := s.UpdateDomainMapping(ctx, pid, id, "api", 9090, "acme"); err != nil {
		t.Fatalf("update: %v", err)
	}
	list, _ = s.ListDomainMappings(ctx, pid)
	if list[0].Service != "api" || list[0].TargetPort != 9090 {
		t.Errorf("update not applied: %+v", list[0])
	}

	// Update/delete under the WRONG project id must not touch another
	// project's mapping — scoped exactly like project secrets.
	if err := s.UpdateDomainMapping(ctx, pid2, id, "x", 1, "acme"); !errors.Is(err, ErrNotFound) {
		t.Errorf("update via wrong project should be ErrNotFound, got %v", err)
	}
	if err := s.DeleteDomainMapping(ctx, pid2, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete via wrong project should be ErrNotFound, got %v", err)
	}

	if err := s.DeleteDomainMapping(ctx, pid, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, err = s.ListDomainMappings(ctx, pid)
	if err != nil || len(list) != 0 {
		t.Fatalf("expected empty list after delete, got %+v (err %v)", list, err)
	}
}

func TestDeleteProjectCascadesDomainMappings(t *testing.T) {
	s, ctx := newStore(t)
	pid, err := s.CreateProject(ctx, &Project{Name: "App", Slug: "app", CreatedBy: "admin"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := s.CreateDomainMapping(ctx, pid, "app.example.com", "web", 8080, "acme", "admin"); err != nil {
		t.Fatalf("create mapping: %v", err)
	}
	if err := s.DeleteProject(ctx, pid); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	if _, err := s.DomainMappingByDomain(ctx, "app.example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("mapping should be gone after project delete, got %v", err)
	}
}
