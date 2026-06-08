package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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

// newProjectServer builds a minimal Server (store + temp data dir) plus one
// project — enough to exercise the project file handlers without auth/docker.
func newProjectServer(t *testing.T) (*Server, int64) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	id, err := st.CreateProject(context.Background(), &store.Project{Name: "demo", Slug: "demo", CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{cfg: config.Config{DataDir: t.TempDir()}, store: st}
	// The project folder exists in the real flow (created at project creation);
	// mirror that so safeJoin's sandbox resolution has a root to anchor on.
	if err := os.MkdirAll(srv.projectRoot(id), 0o700); err != nil {
		t.Fatal(err)
	}
	return srv, id
}

// projectReq builds a request carrying the chi {id} URL param.
func projectReq(method, target string, id int64, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(id, 10))
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestOverlayProject(t *testing.T) {
	srv, id := newProjectServer(t)
	root := srv.projectRoot(id)
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "app.conf"), []byte("orig\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tmp, err := srv.overlayProject(id, "compose.yml", "services:\n  web:\n    image: nginx\n")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)

	if tmp == root {
		t.Error("overlay must use a separate temp dir, not the project root")
	}
	// The named file carries the unsaved content...
	if got, _ := os.ReadFile(filepath.Join(tmp, "compose.yml")); !strings.Contains(string(got), "nginx") {
		t.Errorf("overlay not applied: %q", got)
	}
	// ...and sibling files are copied verbatim (so relative refs still resolve).
	if got, _ := os.ReadFile(filepath.Join(tmp, "config", "app.conf")); string(got) != "orig\n" {
		t.Errorf("sibling not copied: %q", got)
	}
	// The on-disk project is untouched.
	if got, _ := os.ReadFile(filepath.Join(root, "compose.yml")); string(got) != "services: {}\n" {
		t.Errorf("overlay must not mutate the on-disk file: %q", got)
	}
}

func TestProjectBinaryFileRoundTrip(t *testing.T) {
	srv, id := newProjectServer(t)
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01, 0xff, 0xfe}

	// Upload raw bytes.
	w := httptest.NewRecorder()
	srv.handleUploadProjectFileRaw(w, projectReq("POST", "/api/projects/1/files/raw?path=logo.png", id, bytes.NewReader(png)))
	if w.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body %s", w.Code, w.Body)
	}

	// List should flag it binary and carry no content payload.
	w = httptest.NewRecorder()
	srv.handleListProjectFiles(w, projectReq("GET", "/api/projects/1/files", id, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d", w.Code)
	}
	var files []projectFileJSON
	if err := json.Unmarshal(w.Body.Bytes(), &files); err != nil {
		t.Fatal(err)
	}
	var got *projectFileJSON
	for i := range files {
		if files[i].Name == "logo.png" {
			got = &files[i]
		}
	}
	if got == nil {
		t.Fatal("logo.png not listed")
	}
	if !got.Binary {
		t.Error("logo.png should be flagged binary")
	}
	if got.Content != "" {
		t.Error("binary file should not carry content")
	}

	// Download must return the exact original bytes.
	w = httptest.NewRecorder()
	srv.handleDownloadProjectFile(w, projectReq("GET", "/api/projects/1/files/raw?path=logo.png", id, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("download status = %d", w.Code)
	}
	if !bytes.Equal(w.Body.Bytes(), png) {
		t.Errorf("downloaded bytes differ: got %v want %v", w.Body.Bytes(), png)
	}
}

func TestSafeJoin(t *testing.T) {
	root := t.TempDir()

	ok := []string{"compose.yml", "config/app.conf", "scripts/run.sh", "a/b/c.txt", "a/../b.txt"}
	for _, name := range ok {
		full, err := safeJoin(root, name)
		if err != nil {
			t.Errorf("%q should be allowed: %v", name, err)
			continue
		}
		if full != root && !strings.HasPrefix(full, root+string(os.PathSeparator)) {
			t.Errorf("%q resolved outside root: %s", name, full)
		}
	}

	bad := []string{"", "  ", "../escape", "../../etc/passwd", "/etc/passwd", "a/../../b"}
	for _, name := range bad {
		if _, err := safeJoin(root, name); err == nil {
			t.Errorf("%q should be rejected", name)
		}
	}

	// A symlink target (final component) escaping root is rejected.
	link := filepath.Join(root, "link.yml")
	if err := os.Symlink("/etc/hostname", link); err == nil {
		if _, err := safeJoin(root, "link.yml"); err == nil {
			t.Error("symlink should be rejected")
		}
	}

	// A symlinked *parent* directory escaping root is rejected too (the file
	// itself doesn't exist yet, but writing through it would land outside root).
	if err := os.Symlink("/etc", filepath.Join(root, "escape")); err == nil {
		if _, err := safeJoin(root, "escape/passwd"); err == nil {
			t.Error("a write through a symlinked parent dir should be rejected")
		}
	}
}

func TestLooksBinary(t *testing.T) {
	text := [][]byte{
		[]byte("version: \"3\"\nservices:\n  web:\n    image: nginx\n"),
		[]byte("#!/bin/sh\necho hello\n"),
		[]byte(""),
		[]byte("příliš žluťoučký kůň úpěl ďábelské ódy"), // valid UTF-8 with diacritics
	}
	for _, b := range text {
		if looksBinary(b) {
			t.Errorf("text wrongly flagged as binary: %q", b)
		}
	}

	binary := [][]byte{
		{0x00, 0x01, 0x02, 0x03},             // NUL byte
		{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a}, // PNG header (invalid UTF-8)
		{0xff, 0xd8, 0xff, 0xe0},             // JPEG header
	}
	for _, b := range binary {
		if !looksBinary(b) {
			t.Errorf("binary wrongly flagged as text: %v", b)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"My App":         "my-app",
		"  Web  Stack  ": "web-stack",
		"a__b--c":        "a-b-c",
		"9lives":         "9lives",
		"-leading-":      "leading",
		"!!!":            "project",
		"Foo.Bar":        "foo-bar",
		"Další projekt":  "dalsi-projekt", // diacritics transliterated, not dropped
		"Žluťoučký":      "zlutoucky",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
