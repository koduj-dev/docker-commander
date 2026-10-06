package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

// While one operation holds a project, every other operation that changes its
// files or what is deployed from them is refused with 409, and changes nothing.
// The race this closes: a restore snapshots the folder, swaps it and deletes
// the old one, so an editor save made in between answered 200 and was lost.
func TestProjectOperationsRefuseWhileTheProjectIsBusy(t *testing.T) {
	srv, _, pid, admin := deployTestServer(t, "oplock", "services:\n  web:\n    image: alpine\n")
	root := srv.projectRoot(pid)
	id := strconv.FormatInt(pid, 10)

	call := func(method, target string, h http.HandlerFunc, body string, params map[string]string) int {
		r := httptest.NewRequest(method, target, strings.NewReader(body)).WithContext(ctxAs(admin, "admin"))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		for k, v := range params {
			rctx.URLParams.Add(k, v)
		}
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code
	}
	ops := []struct {
		name   string
		method string
		target string
		h      http.HandlerFunc
		body   string
		params map[string]string
	}{
		{"save a file", "PUT", "/x", srv.handleWriteProjectFile, `{"name":"compose.yml","content":"services: {}\n"}`, nil},
		{"upload", "POST", "/x?path=new.txt", srv.handleUploadProjectFileRaw, "data", nil},
		{"delete a file", "DELETE", "/x?path=compose.yml", srv.handleDeleteProjectFile, "", nil},
		{"new folder", "POST", "/x", srv.handleMakeProjectDir, `{"name":"sub"}`, nil},
		{"settings", "PATCH", "/x", srv.handleRenameProject, `{"name":"renamed"}`, nil},
		{"deploy", "POST", "/x", srv.handleDeployProject, `{}`, nil},
		{"down", "POST", "/x", srv.handleDownProject, "", nil},
		{"restart", "POST", "/x", srv.handleRestartProject, "", nil},
		{"restore", "POST", "/x", srv.handleRestoreRevision, `{}`, map[string]string{"rev": "1"}},
		{"delete the project", "DELETE", "/x", srv.handleDeleteProject, "", nil},
	}

	before, err := os.ReadFile(filepath.Join(root, "compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	release, busy := beginProjectOp(pid, "a restore")
	if release == nil {
		t.Fatalf("project already busy: %s", busy)
	}
	for _, op := range ops {
		if code := call(op.method, op.target, op.h, op.body, op.params); code != http.StatusConflict {
			t.Errorf("SECURITY: %s while a restore runs → %d, want 409", op.name, code)
		}
	}
	after, err := os.ReadFile(filepath.Join(root, "compose.yml"))
	if err != nil || string(after) != string(before) {
		t.Errorf("a refused operation changed compose.yml: %q, err %v", after, err)
	}
	for _, p := range []string{"new.txt", "sub"} {
		if _, err := os.Stat(filepath.Join(root, p)); !os.IsNotExist(err) {
			t.Errorf("a refused operation created %s", p)
		}
	}
	release()

	// Once the project is free, the same request goes through.
	if code := call("PUT", "/x", srv.handleWriteProjectFile, `{"name":"notes.txt","content":"hi"}`, nil); code != http.StatusOK {
		t.Errorf("save after the restore finished → %d, want 200", code)
	}
}

// The MCP deploy and down are the same operations and take the same lock.
func TestMCPProjectOperationsRefuseWhileTheProjectIsBusy(t *testing.T) {
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("the MCP project tools check for the compose CLI first")
	}
	srv, _, pid, _ := deployTestServer(t, "oplock-mcp", "services:\n  web:\n    image: alpine\n")
	release, _ := beginProjectOp(pid, "a deploy")
	defer release()
	if _, err := srv.mcpDeployProject(context.Background(), pid, nil, false); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Errorf("MCP deploy while busy: %v, want a busy refusal", err)
	}
	if _, err := srv.mcpDownProject(context.Background(), pid); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Errorf("MCP down while busy: %v, want a busy refusal", err)
	}
}

func TestBeginProjectOpIsPerProject(t *testing.T) {
	a, _ := beginProjectOp(9001, "a deploy")
	if a == nil {
		t.Fatal("free project refused")
	}
	if r, busy := beginProjectOp(9001, "a save"); r != nil || busy != "a deploy" {
		t.Errorf("second op on the same project: release=%v busy=%q", r != nil, busy)
	}
	b, _ := beginProjectOp(9002, "a save")
	if b == nil {
		t.Error("a different project was refused")
	} else {
		b()
	}
	a()
	if r, _ := beginProjectOp(9001, "a save"); r == nil {
		t.Error("released project still refused")
	} else {
		r()
	}
}

// A stack's start/stop/restart/remove act on the containers of the project that
// deploys it, so while that project is busy they are refused too. A stack no
// project owns is not held up.
func TestStackActionsWaitForTheProjectThatOwnsTheStack(t *testing.T) {
	srv, st, pid, admin := deployTestServer(t, "oplock-stack", "services:\n  web:\n    image: alpine\n")
	if err := st.EnsureLocalHost(context.Background()); err != nil { // as every real install has
		t.Fatal(err)
	}
	release, _ := beginProjectOp(pid, "a restore")
	defer release()

	stackAction := func(stack string) (int, string) {
		r := httptest.NewRequest("POST", "/x", nil).WithContext(ctxAs(admin, "admin"))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("project", stack)
		rctx.URLParams.Add("action", "restart")
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		srv.handleStackAction(w, r)
		return w.Code, w.Body.String()
	}
	if code, body := stackAction("oplock-stack"); code != http.StatusConflict || !strings.Contains(body, "busy") {
		t.Errorf("SECURITY: restarting a busy project's stack → %d %s, want 409 busy", code, body)
	}
	if code, body := stackAction("someone-elses-stack"); code == http.StatusConflict {
		t.Errorf("a stack no project owns was held up: %s", body)
	}

	// The same claim is what the MCP stack tools get through Deps.BeginStackOp.
	if _, err := srv.beginStackOp(context.Background(), 0, "oplock-stack", "a stack stop"); !errors.Is(err, errBusy) {
		t.Errorf("beginStackOp on a busy project's stack: %v, want busy", err)
	}
}
