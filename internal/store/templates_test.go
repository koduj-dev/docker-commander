package store

import (
	"errors"
	"testing"
)

func TestProjectTemplatesCRUD(t *testing.T) {
	s, ctx := newStore(t)

	id, err := s.CreateProjectTemplate(ctx, &ProjectTemplate{Name: "My Stack", Slug: "my-stack", Description: "d", CreatedBy: "admin"})
	if err != nil || id == 0 {
		t.Fatalf("create: id=%d err=%v", id, err)
	}
	if _, err := s.CreateProjectTemplate(ctx, &ProjectTemplate{Name: "Other", Slug: "my-stack"}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate slug should be ErrDuplicate, got %v", err)
	}
	got, err := s.ProjectTemplateBySlug(ctx, "my-stack")
	if err != nil || got.Name != "My Stack" {
		t.Fatalf("by slug: %+v err=%v", got, err)
	}
	if list, _ := s.ListProjectTemplates(ctx); len(list) != 1 {
		t.Errorf("expected 1 template, got %d", len(list))
	}
	if err := s.DeleteProjectTemplate(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.ProjectTemplateByID(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestServiceBlocksCRUD(t *testing.T) {
	s, ctx := newStore(t)

	id, err := s.CreateServiceBlock(ctx, &ServiceBlock{
		Name: "My Cache", Slug: "my-cache", Service: "cache",
		ServiceYAML: "  cache:\n    image: redis:7-alpine\n", Volumes: []string{"cachedata"}, CreatedBy: "admin",
	})
	if err != nil || id == 0 {
		t.Fatalf("create: id=%d err=%v", id, err)
	}
	got, err := s.ServiceBlockBySlug(ctx, "my-cache")
	if err != nil {
		t.Fatalf("by slug: %v", err)
	}
	if got.Service != "cache" || len(got.Volumes) != 1 || got.Volumes[0] != "cachedata" {
		t.Errorf("unexpected block: %+v", got)
	}
	if list, _ := s.ListServiceBlocks(ctx); len(list) != 1 {
		t.Errorf("expected 1 block, got %d", len(list))
	}
	if err := s.DeleteServiceBlock(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if list, _ := s.ListServiceBlocks(ctx); len(list) != 0 {
		t.Errorf("expected 0 blocks after delete, got %d", len(list))
	}
}
