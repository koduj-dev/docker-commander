package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// TestRecoveryDomainMappings_RoundTrip: a project's domain mappings must
// survive an export/import round trip — they are persistent project
// configuration, exactly like secrets, not something only the running
// instance knows about.
func TestRecoveryDomainMappings_RoundTrip(t *testing.T) {
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	srv, admin := newRecoveryServer(t)
	ctx := context.Background()
	pid, err := srv.store.CreateProject(ctx, &store.Project{Name: "shop", Slug: "dctest-recovery-domain", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	root := srv.projectRoot(pid)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n"
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.CreateDomainMapping(ctx, pid, "shop.example.com", "web", 8080, "acme", "admin"); err != nil {
		t.Fatal(err)
	}

	w := exportRecoveryRequest(srv, admin, `{}`, "correct horse battery staple")
	if w.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", w.Code, w.Body.String())
	}

	dst, dstAdmin := newRecoveryServer(t)
	iw := importRecoveryRequest(dst, dstAdmin, w.Body.Bytes(), "correct horse battery staple", "")
	if iw.Code != http.StatusOK {
		t.Fatalf("import status = %d: %s", iw.Code, iw.Body.String())
	}
	var resp struct {
		Summary  importSummary `json:"summary"`
		Warnings []string      `json:"warnings"`
	}
	if err := json.Unmarshal(iw.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Summary.ProjectsCreated != 1 {
		t.Fatalf("expected 1 project created, got %+v warnings=%v", resp.Summary, resp.Warnings)
	}
	projects, err := dst.store.ListProjects(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("project not imported: %v %+v", err, projects)
	}
	mappings, err := dst.store.ListDomainMappings(ctx, projects[0].ID)
	if err != nil || len(mappings) != 1 {
		t.Fatalf("domain mapping not restored: %v %+v", err, mappings)
	}
	if mappings[0].Domain != "shop.example.com" || mappings[0].Service != "web" || mappings[0].TargetPort != 8080 {
		t.Errorf("restored mapping wrong: %+v", mappings[0])
	}
}

// TestRecoveryDomainMappings_CollisionWarnsRatherThanFails: a domain already
// claimed on the destination (here: by an earlier project in the SAME
// import) must be skipped with an explicit warning, not silently dropped
// and not allowed to fail the whole import.
func TestRecoveryDomainMappings_CollisionWarnsRatherThanFails(t *testing.T) {
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	srv, admin := newRecoveryServer(t)
	ctx := context.Background()
	pid1, err := srv.store.CreateProject(ctx, &store.Project{Name: "shop1", Slug: "dctest-recovery-domain-c1", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	root := srv.projectRoot(pid1)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n"
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.CreateDomainMapping(ctx, pid1, "shared.example.com", "web", 80, "acme", "admin"); err != nil {
		t.Fatal(err)
	}

	w := exportRecoveryRequest(srv, admin, `{}`, "correct horse battery staple")
	if w.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", w.Code, w.Body.String())
	}

	// The destination already has a DIFFERENT project sitting on the same
	// domain before the import even starts.
	dst, dstAdmin := newRecoveryServer(t)
	pid2, err := dst.store.CreateProject(ctx, &store.Project{Name: "other", Slug: "other", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dst.store.CreateDomainMapping(ctx, pid2, "shared.example.com", "api", 9090, "acme", "admin"); err != nil {
		t.Fatal(err)
	}

	iw := importRecoveryRequest(dst, dstAdmin, w.Body.Bytes(), "correct horse battery staple", "")
	if iw.Code != http.StatusOK {
		t.Fatalf("import status = %d: %s", iw.Code, iw.Body.String())
	}
	var resp struct {
		Summary  importSummary `json:"summary"`
		Warnings []string      `json:"warnings"`
	}
	if err := json.Unmarshal(iw.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// The project itself still imports — only the colliding domain is skipped.
	if resp.Summary.ProjectsCreated != 1 {
		t.Fatalf("expected the project to still be created despite the domain collision, got %+v warnings=%v", resp.Summary, resp.Warnings)
	}
	foundWarning := false
	for _, w := range resp.Warnings {
		if strings.Contains(w, "shared.example.com") && strings.Contains(w, "already mapped") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Errorf("expected a warning about the domain collision, got %v", resp.Warnings)
	}
	// The original mapping (pid2 -> api:9090) must be untouched.
	mappings, err := dst.store.ListDomainMappings(ctx, pid2)
	if err != nil || len(mappings) != 1 || mappings[0].Service != "api" {
		t.Errorf("existing mapping should be untouched by the collision: %v %+v", err, mappings)
	}
}
