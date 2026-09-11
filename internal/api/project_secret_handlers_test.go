package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/store"
)

func jsonBody(v any) *bytes.Reader {
	if v == nil {
		return bytes.NewReader(nil)
	}
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

// callSecretHandler dispatches to the right handler by method, mirroring the
// server's own route table (see server.go) rather than going through chi.
func callSecretHandler(srv *Server, method string, pid int64, name string, uid int64, role string, body any) *httptest.ResponseRecorder {
	sid := strconv.FormatInt(pid, 10)
	url := "/api/projects/" + sid + "/secrets"
	if name != "" {
		url += "/" + name
	}
	req := httptest.NewRequest(method, url, jsonBody(body)).WithContext(ctxAs(uid, role))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sid)
	if name != "" {
		rctx.URLParams.Add("name", name)
	}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	switch method {
	case "GET":
		srv.handleListProjectSecrets(w, req)
	case "POST":
		srv.handleCreateProjectSecret(w, req)
	case "PUT":
		srv.handleUpdateProjectSecret(w, req)
	case "DELETE":
		srv.handleDeleteProjectSecret(w, req)
	}
	return w
}

func TestProjectSecretsHandlers_CRUD(t *testing.T) {
	srv, pid := newProjectServer(t)

	// Create.
	w := callSecretHandler(srv, "POST", pid, "", 1, "admin", map[string]string{"name": "DB_PASSWORD", "value": "hunter2"})
	if w.Code != 200 {
		t.Fatalf("create status = %d: %s", w.Code, w.Body.String())
	}

	// List — name only, no value field anywhere in the body.
	w = callSecretHandler(srv, "GET", pid, "", 1, "admin", nil)
	if w.Code != 200 {
		t.Fatalf("list status = %d: %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("DB_PASSWORD")) {
		t.Error("list should include the secret's name")
	}
	if bytes.Contains(w.Body.Bytes(), []byte("hunter2")) {
		t.Fatal("SECURITY: list echoed the plaintext value")
	}

	// Duplicate create -> 409.
	w = callSecretHandler(srv, "POST", pid, "", 1, "admin", map[string]string{"name": "DB_PASSWORD", "value": "other"})
	if w.Code != 409 {
		t.Errorf("duplicate create status = %d, want 409", w.Code)
	}

	// Update.
	w = callSecretHandler(srv, "PUT", pid, "DB_PASSWORD", 1, "admin", map[string]string{"value": "hunter3"})
	if w.Code != 200 {
		t.Fatalf("update status = %d: %s", w.Code, w.Body.String())
	}
	env, err := srv.store.ResolveProjectSecretEnv(context.Background(), pid)
	if err != nil || env["DB_PASSWORD"] != "hunter3" {
		t.Errorf("update did not take effect: env=%v err=%v", env, err)
	}

	// Update of a missing name -> 404.
	w = callSecretHandler(srv, "PUT", pid, "NOPE", 1, "admin", map[string]string{"value": "x"})
	if w.Code != 404 {
		t.Errorf("update missing status = %d, want 404", w.Code)
	}

	// Delete.
	w = callSecretHandler(srv, "DELETE", pid, "DB_PASSWORD", 1, "admin", nil)
	if w.Code != 200 {
		t.Fatalf("delete status = %d: %s", w.Code, w.Body.String())
	}
	w = callSecretHandler(srv, "DELETE", pid, "DB_PASSWORD", 1, "admin", nil)
	if w.Code != 404 {
		t.Errorf("delete of already-deleted secret status = %d, want 404", w.Code)
	}
}

func TestProjectSecretsHandlers_NameValidation(t *testing.T) {
	srv, pid := newProjectServer(t)
	for _, name := range []string{"123FOO", "FOO-BAR", "", "FOO BAR"} {
		w := callSecretHandler(srv, "POST", pid, "", 1, "admin", map[string]string{"name": name, "value": "x"})
		if w.Code != 400 {
			t.Errorf("name %q: status = %d, want 400", name, w.Code)
		}
	}
}

func TestProjectSecretsHandlers_RBAC(t *testing.T) {
	srv, pid := newProjectServer(t)
	ctx := context.Background()

	readOnly, err := srv.store.CreateUser(ctx, &store.User{
		Username: "viewer", Role: "user", Sections: []string{"projects"}, ReadOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	noGrant, err := srv.store.CreateUser(ctx, &store.User{Username: "outsider", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}

	// A read-only projects grant can list...
	w := callSecretHandler(srv, "GET", pid, "", readOnly, "user", nil)
	if w.Code != 200 {
		t.Errorf("read-only list status = %d, want 200: %s", w.Code, w.Body.String())
	}
	// ...but not create/update/delete.
	w = callSecretHandler(srv, "POST", pid, "", readOnly, "user", map[string]string{"name": "X", "value": "v"})
	if w.Code != 403 {
		t.Errorf("read-only create status = %d, want 403", w.Code)
	}

	// No grant at all: project must look like it doesn't exist (404, not 403) —
	// matches loadProject's convention for every other /projects/{id}/... route.
	w = callSecretHandler(srv, "GET", pid, "", noGrant, "user", nil)
	if w.Code != 404 {
		t.Errorf("no-grant list status = %d, want 404", w.Code)
	}
}
