package api

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/config"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// callDomainHandler dispatches to the right handler by method, mirroring the
// server's own route table (see server.go) rather than going through chi.
func callDomainHandler(srv *Server, method string, pid int64, domainID string, uid int64, role string, body any) *httptest.ResponseRecorder {
	sid := strconv.FormatInt(pid, 10)
	url := "/api/projects/" + sid + "/domains"
	if domainID != "" {
		url += "/" + domainID
	}
	req := httptest.NewRequest(method, url, jsonBody(body)).WithContext(ctxAs(uid, role))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sid)
	if domainID != "" {
		rctx.URLParams.Add("domainID", domainID)
	}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	switch method {
	case "GET":
		srv.handleListDomainMappings(w, req)
	case "POST":
		srv.handleCreateDomainMapping(w, req)
	case "PUT":
		srv.handleUpdateDomainMapping(w, req)
	case "DELETE":
		srv.handleDeleteDomainMapping(w, req)
	}
	return w
}

func validDomainBody() map[string]any {
	return map[string]any{"domain": "app.example.com", "service": "web", "targetPort": 8080, "tlsMode": "acme"}
}

func TestDomainMappingHandlers_CRUD(t *testing.T) {
	srv, pid := newProjectServer(t)

	w := callDomainHandler(srv, "POST", pid, "", 1, "admin", validDomainBody())
	if w.Code != 200 {
		t.Fatalf("create status = %d: %s", w.Code, w.Body.String())
	}

	w = callDomainHandler(srv, "GET", pid, "", 1, "admin", nil)
	if w.Code != 200 {
		t.Fatalf("list status = %d: %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("app.example.com")) {
		t.Error("list should include the domain")
	}

	list, err := srv.store.ListDomainMappings(context.Background(), pid)
	if err != nil || len(list) != 1 {
		t.Fatalf("expected 1 mapping in store, got %+v (err %v)", list, err)
	}
	id := strconv.FormatInt(list[0].ID, 10)

	// Duplicate domain -> 409.
	w = callDomainHandler(srv, "POST", pid, "", 1, "admin", validDomainBody())
	if w.Code != 409 {
		t.Errorf("duplicate create status = %d, want 409", w.Code)
	}

	// Update.
	w = callDomainHandler(srv, "PUT", pid, id, 1, "admin", map[string]any{"service": "web", "targetPort": 9090, "tlsMode": "acme"})
	if w.Code != 200 {
		t.Fatalf("update status = %d: %s", w.Code, w.Body.String())
	}
	list, _ = srv.store.ListDomainMappings(context.Background(), pid)
	if list[0].TargetPort != 9090 {
		t.Errorf("update did not take effect: %+v", list[0])
	}

	// Update of a missing id -> 404.
	w = callDomainHandler(srv, "PUT", pid, "999999", 1, "admin", map[string]any{"service": "web", "targetPort": 1, "tlsMode": "acme"})
	if w.Code != 404 {
		t.Errorf("update missing status = %d, want 404", w.Code)
	}

	// Delete.
	w = callDomainHandler(srv, "DELETE", pid, id, 1, "admin", nil)
	if w.Code != 200 {
		t.Fatalf("delete status = %d: %s", w.Code, w.Body.String())
	}
	w = callDomainHandler(srv, "DELETE", pid, id, 1, "admin", nil)
	if w.Code != 404 {
		t.Errorf("delete of already-deleted mapping status = %d, want 404", w.Code)
	}
}

func TestDomainMappingHandlers_Validation(t *testing.T) {
	srv, pid := newProjectServer(t)
	cases := []struct {
		name string
		body map[string]any
	}{
		{"bare hostname, no dot", map[string]any{"domain": "localhost", "service": "web", "targetPort": 80, "tlsMode": "acme"}},
		{"IP literal", map[string]any{"domain": "192.168.1.1", "service": "web", "targetPort": 80, "tlsMode": "acme"}},
		{"wildcard", map[string]any{"domain": "*.example.com", "service": "web", "targetPort": 80, "tlsMode": "acme"}},
		{"empty service", map[string]any{"domain": "app.example.com", "service": "", "targetPort": 80, "tlsMode": "acme"}},
		{"port zero", map[string]any{"domain": "app.example.com", "service": "web", "targetPort": 0, "tlsMode": "acme"}},
		{"port too big", map[string]any{"domain": "app.example.com", "service": "web", "targetPort": 70000, "tlsMode": "acme"}},
		{"unknown tls mode", map[string]any{"domain": "app.example.com", "service": "web", "targetPort": 80, "tlsMode": "self-signed"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := callDomainHandler(srv, "POST", pid, "", 1, "admin", c.body)
			if w.Code != 400 {
				t.Errorf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
		})
	}
}

// A domain equal to Docker Commander's OWN configured admin hostname must be
// rejected — the (future) SNI dispatcher could never resolve the collision.
func TestDomainMappingHandlers_RejectsOwnACMEDomain(t *testing.T) {
	srv, pid := newProjectServer(t)
	srv.cfg.ACMEDomains = []string{"admin.example.com"}
	body := validDomainBody()
	body["domain"] = "Admin.Example.Com" // case-insensitive match
	w := callDomainHandler(srv, "POST", pid, "", 1, "admin", body)
	if w.Code != 400 {
		t.Errorf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

func TestDomainMappingHandlers_RBAC(t *testing.T) {
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

	w := callDomainHandler(srv, "GET", pid, "", readOnly, "user", nil)
	if w.Code != 200 {
		t.Errorf("read-only list status = %d, want 200: %s", w.Code, w.Body.String())
	}
	w = callDomainHandler(srv, "POST", pid, "", readOnly, "user", validDomainBody())
	if w.Code != 403 {
		t.Errorf("read-only create status = %d, want 403", w.Code)
	}

	w = callDomainHandler(srv, "GET", pid, "", noGrant, "user", nil)
	if w.Code != 404 {
		t.Errorf("no-grant list status = %d, want 404", w.Code)
	}
}

// Live compose-integration: the service-exists check must actually consult
// the project's real compose config, not just accept anything. Needs the
// docker compose CLI (see docker.ComposeAvailable), same gating other
// compose-integration tests in this package already use.
func TestDomainMappingHandlers_ServiceMustExistInCompose(t *testing.T) {
	if testing.Short() {
		t.Skip("needs the docker compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	dir := t.TempDir()
	srv := &Server{cfg: config.Config{DataDir: dir}, store: st, docker: docker.NewManager(st)}

	if _, err := st.CreateUser(ctx, &store.User{Username: "root", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	pid, err := st.CreateProject(ctx, &store.Project{Name: "app", Slug: "dctest-domains", ComposeFile: "compose.yml"})
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

	// "web" is a real service -> accepted.
	w := callDomainHandler(srv, "POST", pid, "", 1, "admin", map[string]any{
		"domain": "app.example.com", "service": "web", "targetPort": 8080, "tlsMode": "acme",
	})
	if w.Code != 200 {
		t.Fatalf("real service should be accepted: status = %d: %s", w.Code, w.Body.String())
	}

	// "ghost" is not defined anywhere in the compose file -> rejected.
	w = callDomainHandler(srv, "POST", pid, "", 1, "admin", map[string]any{
		"domain": "ghost.example.com", "service": "ghost", "targetPort": 8080, "tlsMode": "acme",
	})
	if w.Code != 400 {
		t.Errorf("nonexistent service should be rejected: status = %d, want 400: %s", w.Code, w.Body.String())
	}
}
