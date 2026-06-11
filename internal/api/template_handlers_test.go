package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/store"
)

func newTemplatesServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Server{cfg: config.Config{DataDir: t.TempDir()}, store: st}
}

func postJSON(t *testing.T, h http.HandlerFunc, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", target, bytes.NewReader(b))
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

// createProject drives handleCreateProject and returns the new project id.
func createProject(t *testing.T, srv *Server, body any) int64 {
	t.Helper()
	w := postJSON(t, srv.handleCreateProject, "/api/projects", body)
	if w.Code != http.StatusOK {
		t.Fatalf("create project: status %d — %s", w.Code, w.Body.String())
	}
	var res struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	return res.ID
}

func TestCreateProjectFromBuiltinPreset(t *testing.T) {
	srv := newTemplatesServer(t)
	id := createProject(t, srv, map[string]any{
		"name":     "Demo Web",
		"template": map[string]string{"id": "nginx-static", "source": "builtin"},
	})
	compose, err := os.ReadFile(filepath.Join(srv.projectRoot(id), "compose.yml"))
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	got := string(compose)
	for _, want := range []string{"name: demo-web", "nginx:alpine", "8080:80"} {
		if !strings.Contains(got, want) {
			t.Errorf("preset compose missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "{{") {
		t.Errorf("unrendered template marker:\n%s", got)
	}
	// The preset's sidecar file was seeded too.
	if _, err := os.Stat(filepath.Join(srv.projectRoot(id), "html", "index.html")); err != nil {
		t.Errorf("sidecar not seeded: %v", err)
	}
}

func TestCreateProjectFromBuilderBlocks(t *testing.T) {
	srv := newTemplatesServer(t)
	id := createProject(t, srv, map[string]any{
		"name": "My Stack",
		"blocks": []map[string]string{
			{"id": "redis", "source": "builtin"},
			{"id": "postgres", "source": "builtin"},
		},
	})
	compose, err := os.ReadFile(filepath.Join(srv.projectRoot(id), "compose.yml"))
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	got := string(compose)
	for _, want := range []string{"name: my-stack", "services:", "  redis:", "  db:", "volumes:", "pgdata:"} {
		if !strings.Contains(got, want) {
			t.Errorf("builder compose missing %q:\n%s", want, got)
		}
	}
}

func TestSaveProjectAsTemplateAndList(t *testing.T) {
	srv := newTemplatesServer(t)
	id := createProject(t, srv, map[string]any{
		"name":     "Seed",
		"template": map[string]string{"id": "nginx-static", "source": "builtin"},
	})

	w := postJSON(t, srv.handleCreateProjectTemplate, "/api/project-templates", map[string]any{
		"name": "My Preset", "description": "saved", "fromProjectId": id,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("save as template: status %d — %s", w.Code, w.Body.String())
	}

	// The template row exists and its files were snapshotted to disk.
	tpl, err := srv.store.ProjectTemplateBySlug(context.Background(), "my-preset")
	if err != nil {
		t.Fatalf("template not stored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(srv.templateRoot(tpl.ID), "compose.yml")); err != nil {
		t.Errorf("template files not snapshotted: %v", err)
	}

	// The list endpoint merges builtins + the user preset.
	lw := httptest.NewRecorder()
	srv.handleListProjectTemplates(lw, httptest.NewRequest("GET", "/api/project-templates", nil))
	var list []templateJSON
	if err := json.Unmarshal(lw.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var hasBuiltin, hasUser bool
	for _, it := range list {
		if it.Source == "builtin" && it.ID == "nginx-static" {
			hasBuiltin = true
		}
		if it.Source == "user" && it.Name == "My Preset" && it.Deletable {
			hasUser = true
		}
	}
	if !hasBuiltin || !hasUser {
		t.Errorf("list missing entries: builtin=%v user=%v", hasBuiltin, hasUser)
	}
}
