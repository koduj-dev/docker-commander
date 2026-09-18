package api

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// Boundary cases a single naive regex got wrong: an unbounded final (TLD)
// label, and no cap on the complete name — DNS/IDNA and ACME both impose a
// 63-octet-per-label and a 253-octet-total limit (RFC 1035).
func TestValidFQDN_LengthBoundaries(t *testing.T) {
	label63 := strings.Repeat("a", 63)
	label64 := strings.Repeat("a", 64)
	tld63 := strings.Repeat("a", 63)
	tld64 := strings.Repeat("a", 64)

	cases := []struct {
		name   string
		domain string
		want   bool
	}{
		{"63-octet label accepted", label63 + ".example.com", true},
		{"64-octet label rejected", label64 + ".example.com", false},
		{"63-octet TLD accepted", "app." + tld63, true},
		{"64-octet TLD rejected", "app." + tld64, false},
		{"ordinary domain accepted", "app.example.com", true},
		{"numeric TLD rejected (IP-shaped)", "192.168.1.1", false},
		{"punycode TLD accepted (real, ACME-usable IDN)", "app.xn--p1ai", true},
		{"punycode label + punycode TLD accepted", "xn--e1afmkfd.xn--p1ai", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := validFQDN(c.domain); got != c.want {
				t.Errorf("validFQDN(%d-char domain) = %v, want %v", len(c.domain), got, c.want)
			}
		})
	}

	// Total name length: build a domain out of many max-length labels until
	// it crosses 253 octets, and confirm the boundary is enforced even when
	// every individual label is itself valid.
	var labels []string
	for total := 0; total < 253; {
		labels = append(labels, label63)
		total += 64 // label + dot
	}
	labels = append(labels, "com")
	tooLong := strings.Join(labels, ".")
	if len(tooLong) <= 253 {
		t.Fatalf("test setup bug: constructed domain is only %d octets, want >253", len(tooLong))
	}
	if validFQDN(tooLong) {
		t.Errorf("a %d-octet domain name should be rejected (DNS max is 253)", len(tooLong))
	}
}

// A project with no mappings must return the literal JSON "[]", never
// "null" — the frontend modal treats a null/undefined body as "still
// loading" and would otherwise never show "No domains yet.".
func TestDomainMappingHandlers_EmptyListIsJSONArrayNotNull(t *testing.T) {
	srv, pid := newProjectServer(t)
	w := callDomainHandler(srv, "GET", pid, "", 1, "admin", nil)
	if w.Code != 200 {
		t.Fatalf("list status = %d: %s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "[]\n" && got != "[]" {
		t.Errorf("empty list body = %q, want []", got)
	}
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

	// A PUT trying to change the domain itself must be REJECTED, not
	// silently ignored with a misleading 200 — the domain is immutable.
	w = callDomainHandler(srv, "PUT", pid, id, 1, "admin", map[string]any{
		"domain": "different.example.com", "service": "web", "targetPort": 9090, "tlsMode": "acme",
	})
	if w.Code != 400 {
		t.Errorf("attempting to change the domain via PUT should be 400, got %d: %s", w.Code, w.Body.String())
	}
	list, _ = srv.store.ListDomainMappings(context.Background(), pid)
	if list[0].Domain != "app.example.com" {
		t.Errorf("the domain must not have changed: %+v", list[0])
	}
	// The SAME domain echoed back (a client re-sending what it read) is not
	// an attempted change and must still succeed.
	w = callDomainHandler(srv, "PUT", pid, id, 1, "admin", map[string]any{
		"domain": "app.example.com", "service": "api2", "targetPort": 9091, "tlsMode": "acme",
	})
	if w.Code != 200 {
		t.Errorf("PUT echoing the unchanged domain should succeed, got %d: %s", w.Code, w.Body.String())
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

// A service gated behind a Compose profile must still be mappable: a plain
// `compose config` (no profiles active) silently omits it, so validation
// must resolve the catalog with every declared profile enabled instead of
// rejecting a perfectly valid service.
func TestDomainMappingHandlers_ProfileGatedServiceIsAccepted(t *testing.T) {
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
	pid, err := st.CreateProject(ctx, &store.Project{Name: "app", Slug: "dctest-domains-profile", ComposeFile: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	root := srv.projectRoot(pid)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  web:\n    image: " + deployTestImage + "\n" +
		"  admin:\n    image: " + deployTestImage + "\n    profiles: [\"admin\"]\n"
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	w := callDomainHandler(srv, "POST", pid, "", 1, "admin", map[string]any{
		"domain": "admin.example.com", "service": "admin", "targetPort": 9000, "tlsMode": "acme",
	})
	if w.Code != 200 {
		t.Fatalf("a profile-gated but real service should be accepted: status = %d: %s", w.Code, w.Body.String())
	}

	// The service picker endpoint must offer it too, not just accept it once typed.
	sw := httptest.NewRequest("GET", "/api/projects/"+strconv.FormatInt(pid, 10)+"/domains/services", nil).WithContext(ctxAs(1, "admin"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(pid, 10))
	sw = sw.WithContext(context.WithValue(sw.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleListDomainMappingServices(rec, sw)
	if !bytes.Contains(rec.Body.Bytes(), []byte("admin")) {
		t.Errorf("service picker should include the profile-gated service, got %s", rec.Body.String())
	}
}
