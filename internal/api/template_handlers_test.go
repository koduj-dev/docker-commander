package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// withURLParam attaches a chi route param so handlers that read chi.URLParam can
// be exercised without standing up the full router.
func withURLParam(r *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func strconvI(n int64) string { return strconv.FormatInt(n, 10) }

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

func TestPreviewTemplateDoesNotCreateProject(t *testing.T) {
	srv := newTemplatesServer(t)
	w := postJSON(t, srv.handlePreviewTemplate, "/api/project-templates/preview", map[string]any{
		"name":   "Scratch",
		"blocks": []map[string]string{{"id": "redis", "source": "builtin"}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("preview: status %d — %s", w.Code, w.Body.String())
	}
	var res struct {
		Files []struct {
			Path, Content string
		} `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	var compose string
	for _, f := range res.Files {
		if f.Path == "compose.yml" {
			compose = f.Content
		}
	}
	if !strings.Contains(compose, "name: scratch") || !strings.Contains(compose, "redis:") {
		t.Errorf("preview compose unexpected:\n%s", compose)
	}
	// Preview is pure — no project rows were created.
	if ps, _ := srv.store.ListProjects(context.Background()); len(ps) != 0 {
		t.Errorf("preview created %d project(s); want 0", len(ps))
	}
}

func TestServiceBlockCreateGetUpdate(t *testing.T) {
	srv := newTemplatesServer(t)
	cw := postJSON(t, srv.handleCreateServiceBlock, "/api/service-blocks", map[string]any{
		"name": "Worker", "service": "worker", "serviceYaml": "  worker:\n    image: alpine\n", "volumes": []string{"wdata"},
	})
	if cw.Code != http.StatusOK {
		t.Fatalf("create block: status %d — %s", cw.Code, cw.Body.String())
	}
	var created struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(cw.Body.Bytes(), &created)
	id := strconvI(created.ID)

	// GET returns the full body (YAML + volumes).
	gw := httptest.NewRecorder()
	srv.handleGetServiceBlock(gw, withURLParam(httptest.NewRequest("GET", "/api/service-blocks/"+id, nil), "id", id))
	if gw.Code != http.StatusOK {
		t.Fatalf("get block: status %d — %s", gw.Code, gw.Body.String())
	}
	var detail blockDetailJSON
	if err := json.Unmarshal(gw.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail.ServiceYAML, "image: alpine") || detail.Source != "user" || !detail.Deletable {
		t.Errorf("unexpected block detail: %+v", detail)
	}

	// PUT updates the editable fields.
	uw := postJSON(t, func(w http.ResponseWriter, r *http.Request) {
		srv.handleUpdateServiceBlock(w, withURLParam(r, "id", id))
	}, "/api/service-blocks/"+id, map[string]any{
		"name": "Worker", "service": "worker", "serviceYaml": "  worker:\n    image: busybox\n", "volumes": []string{"wdata"},
	})
	if uw.Code != http.StatusOK {
		t.Fatalf("update block: status %d — %s", uw.Code, uw.Body.String())
	}
	b, _ := srv.store.ServiceBlockByID(context.Background(), created.ID)
	if !strings.Contains(b.ServiceYAML, "busybox") {
		t.Errorf("update not persisted: %q", b.ServiceYAML)
	}
}

func TestTemplateFileEditAndMetaUpdate(t *testing.T) {
	srv := newTemplatesServer(t)
	id := createProject(t, srv, map[string]any{
		"name":     "Seed2",
		"template": map[string]string{"id": "nginx-static", "source": "builtin"},
	})
	sw := postJSON(t, srv.handleCreateProjectTemplate, "/api/project-templates", map[string]any{
		"name": "Editable", "fromProjectId": id,
	})
	if sw.Code != http.StatusOK {
		t.Fatalf("save template: status %d — %s", sw.Code, sw.Body.String())
	}
	tpl, _ := srv.store.ProjectTemplateBySlug(context.Background(), "editable")
	tid := strconvI(tpl.ID)

	// Write a new file into the template.
	ww := postJSON(t, func(w http.ResponseWriter, r *http.Request) {
		srv.handleWriteTemplateFile(w, withURLParam(r, "id", tid))
	}, "/api/project-templates/"+tid+"/files", map[string]string{"name": "notes.txt", "content": "hello"})
	if ww.Code != http.StatusOK {
		t.Fatalf("write template file: status %d — %s", ww.Code, ww.Body.String())
	}
	if data, err := os.ReadFile(filepath.Join(srv.templateRoot(tpl.ID), "notes.txt")); err != nil || string(data) != "hello" {
		t.Errorf("template file not written: %v %q", err, data)
	}

	// List reflects it.
	lw := httptest.NewRecorder()
	srv.handleListTemplateFiles(lw, withURLParam(httptest.NewRequest("GET", "/api/project-templates/"+tid+"/files", nil), "id", tid))
	if !strings.Contains(lw.Body.String(), "notes.txt") {
		t.Errorf("list missing notes.txt: %s", lw.Body.String())
	}

	// Metadata update renames the display name (slug stays).
	mw := postJSON(t, func(w http.ResponseWriter, r *http.Request) {
		srv.handleUpdateProjectTemplate(w, withURLParam(r, "id", tid))
	}, "/api/project-templates/"+tid, map[string]string{"name": "Renamed", "description": "now with notes"})
	if mw.Code != http.StatusOK {
		t.Fatalf("update template meta: status %d — %s", mw.Code, mw.Body.String())
	}
	got, _ := srv.store.ProjectTemplateByID(context.Background(), tpl.ID)
	if got.Name != "Renamed" || got.Slug != "editable" {
		t.Errorf("meta update wrong: name=%q slug=%q", got.Name, got.Slug)
	}

	// Built-in presets are read-only: writing a file 404s.
	bw := postJSON(t, func(w http.ResponseWriter, r *http.Request) {
		srv.handleWriteTemplateFile(w, withURLParam(r, "id", "nginx-static"))
	}, "/api/project-templates/nginx-static/files", map[string]string{"name": "x", "content": "y"})
	if bw.Code != http.StatusNotFound {
		t.Errorf("builtin write should 404, got %d", bw.Code)
	}
}
