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

// composeRequiringSecret writes a compose file that fails to resolve unless
// TOKEN is a non-empty env var (`${TOKEN:?...}`) — the exact shape a
// PENTEST/regression case needs to prove the import validate step doesn't
// spuriously reject a project just because its secrets haven't been
// restored yet.
func composeRequiringSecret(t *testing.T, root string) {
	t.Helper()
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n    environment:\n      TOKEN: ${TOKEN:?TOKEN must be set}\n"
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRecoveryProjectSecrets_IncludedRoundTrip: exporting with
// includeSecrets=true must restore the project's secret with its real
// value on import, and the project itself must not be rejected as
// "no longer validates" just because ${TOKEN:?...} needs a real value.
func TestRecoveryProjectSecrets_IncludedRoundTrip(t *testing.T) {
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	srv, admin := newRecoveryServer(t)
	ctx := context.Background()
	pid, err := srv.store.CreateProject(ctx, &store.Project{Name: "shop", Slug: "dctest-recovery-secret-inc", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	root := srv.projectRoot(pid)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	composeRequiringSecret(t, root)
	if _, err := srv.store.CreateProjectSecret(ctx, pid, "TOKEN", "hunter2", "admin"); err != nil {
		t.Fatal(err)
	}

	w := exportRecoveryRequest(srv, admin, `{"includeSecrets":true}`, "correct horse battery staple")
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
		t.Fatalf("expected 1 project created (validation must not reject it for a missing-secret reason), got %+v warnings=%v", resp.Summary, resp.Warnings)
	}
	projects, err := dst.store.ListProjects(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("project not imported: %v %+v", err, projects)
	}
	env, err := dst.store.ResolveProjectSecretEnv(ctx, projects[0].ID)
	if err != nil || env["TOKEN"] != "hunter2" {
		t.Errorf("secret not restored with its real value: env=%v err=%v", env, err)
	}
}

// TestRecoveryProjectSecrets_ExcludedStillImportsProject: exporting with
// includeSecrets=false must still import the project (not reject it as
// invalid just because the secret it needs wasn't restored), but must NOT
// fabricate a secret value — and must warn that one is still needed.
func TestRecoveryProjectSecrets_ExcludedStillImportsProject(t *testing.T) {
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	srv, admin := newRecoveryServer(t)
	ctx := context.Background()
	pid, err := srv.store.CreateProject(ctx, &store.Project{Name: "shop", Slug: "dctest-recovery-secret-exc", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	root := srv.projectRoot(pid)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	composeRequiringSecret(t, root)
	if _, err := srv.store.CreateProjectSecret(ctx, pid, "TOKEN", "hunter2", "admin"); err != nil {
		t.Fatal(err)
	}

	// includeSecrets omitted -> false: the export must still name the
	// secret (so import can warn about it) but never carry its value.
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
		t.Fatalf("expected 1 project created even without its secret, got %+v warnings=%v", resp.Summary, resp.Warnings)
	}
	foundWarning := false
	for _, w := range resp.Warnings {
		if strings.Contains(w, "TOKEN") && strings.Contains(w, "not included") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Errorf("expected a warning about the missing TOKEN secret, got %v", resp.Warnings)
	}
	projects, err := dst.store.ListProjects(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("project not imported: %v %+v", err, projects)
	}
	env, err := dst.store.ResolveProjectSecretEnv(ctx, projects[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := env["TOKEN"]; ok {
		t.Error("SECURITY: a secret excluded from the export must not be fabricated on import")
	}
}
