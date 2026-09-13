package store

import (
	"errors"
	"testing"
)

func TestProjectSecretsCRUD(t *testing.T) {
	s, ctx := newStore(t)
	pid, err := s.CreateProject(ctx, &Project{Name: "App", Slug: "app", CreatedBy: "admin"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Round trip: create -> resolve gets the real value, list never exposes it.
	if _, err := s.CreateProjectSecret(ctx, pid, "DB_PASSWORD", "hunter2", "admin"); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	env, err := s.ResolveProjectSecretEnv(ctx, pid)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if env["DB_PASSWORD"] != "hunter2" {
		t.Errorf("resolve: got %q want %q", env["DB_PASSWORD"], "hunter2")
	}

	list, err := s.ListProjectSecrets(ctx, pid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Name != "DB_PASSWORD" {
		t.Fatalf("unexpected list: %+v", list)
	}

	// Duplicate name -> ErrDuplicate.
	if _, err := s.CreateProjectSecret(ctx, pid, "DB_PASSWORD", "other", "admin"); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate name should be ErrDuplicate, got %v", err)
	}

	// Update replaces the value in place; name stays the same.
	if err := s.UpdateProjectSecretValue(ctx, pid, "DB_PASSWORD", "hunter3"); err != nil {
		t.Fatalf("update: %v", err)
	}
	env, err = s.ResolveProjectSecretEnv(ctx, pid)
	if err != nil {
		t.Fatalf("resolve after update: %v", err)
	}
	if env["DB_PASSWORD"] != "hunter3" {
		t.Errorf("after update: got %q want %q", env["DB_PASSWORD"], "hunter3")
	}

	// Update/delete of a nonexistent name -> ErrNotFound.
	if err := s.UpdateProjectSecretValue(ctx, pid, "NOPE", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing should be ErrNotFound, got %v", err)
	}
	if err := s.DeleteProjectSecret(ctx, pid, "NOPE"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing should be ErrNotFound, got %v", err)
	}

	// Delete removes it for real.
	if err := s.DeleteProjectSecret(ctx, pid, "DB_PASSWORD"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, err = s.ListProjectSecrets(ctx, pid)
	if err != nil || len(list) != 0 {
		t.Fatalf("expected empty list after delete, got %+v (err %v)", list, err)
	}
}

func TestDeleteProjectCascadesSecrets(t *testing.T) {
	s, ctx := newStore(t)
	pid, err := s.CreateProject(ctx, &Project{Name: "App", Slug: "app", CreatedBy: "admin"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := s.CreateProjectSecret(ctx, pid, "TOKEN", "abc", "admin"); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	if err := s.DeleteProject(ctx, pid); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	list, err := s.ListProjectSecrets(ctx, pid)
	if err != nil || len(list) != 0 {
		t.Fatalf("expected no secrets after project delete, got %+v (err %v)", list, err)
	}
}

func TestMaskSecretValueIsStableAndDistinct(t *testing.T) {
	s, ctx := newStore(t)
	pid, err := s.CreateProject(ctx, &Project{Name: "App", Slug: "app", CreatedBy: "admin"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := s.CreateProjectSecret(ctx, pid, "A", "value-one", "admin"); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	if _, err := s.CreateProjectSecret(ctx, pid, "B", "value-two", "admin"); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	env, err := s.ResolveProjectSecretEnv(ctx, pid)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	maskA1 := s.MaskSecretValue(env["A"])
	maskA2 := s.MaskSecretValue(env["A"])
	maskB := s.MaskSecretValue(env["B"])
	if maskA1 != maskA2 {
		t.Errorf("MaskSecretValue should be stable for the same value: %q != %q", maskA1, maskA2)
	}
	if maskA1 == maskB {
		t.Errorf("MaskSecretValue should differ for different values, both got %q", maskA1)
	}
}
