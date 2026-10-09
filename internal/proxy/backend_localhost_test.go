package proxy

import (
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// PENTEST: Manager's hostID<=0 shorthand means "the local host, or — when the
// local row has been deleted — whichever host is first." A local project's
// mapping must never be resolved against that substitute (a remote daemon),
// so with no Kind=="local" host row resolveBackend has to fail closed and say
// why — not merely fail somewhere later for an unrelated reason.
func TestResolveBackendFailsClosedWithoutALocalHostRow(t *testing.T) {
	st, ctx := newTestStore(t) // store.Open only — no EnsureLocalHost, so no local row
	if _, err := st.CreateHost(ctx, &store.Host{Name: "remote-box", Kind: "tcp", Address: "10.0.0.5:2375"}); err != nil {
		t.Fatal(err)
	}
	pid, err := st.CreateProject(ctx, &store.Project{Name: "App", Slug: "app", HostID: 0, CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	p := newTestProxy(t, st)

	_, err = p.resolveBackend(ctx, store.DomainMapping{ProjectID: pid, Domain: "app.example.com", Service: "web", TargetPort: 8080})
	if err == nil {
		t.Fatal("no local host row: resolveBackend must refuse, not resolve against another host")
	}
	if !strings.Contains(err.Error(), "no local host") {
		t.Errorf("refused for the wrong reason: %v (want the explicit no-local-host refusal)", err)
	}
}

// The control for the test above: with the local row present the same call
// gets PAST the local-host check (and fails later, or succeeds, for
// unrelated reasons — there is no matching container here). Without this, a
// resolver that refused everything would satisfy the test above.
func TestResolveBackendPassesTheLocalHostCheckWhenTheRowExists(t *testing.T) {
	st, ctx := newTestStore(t)
	if err := st.EnsureLocalHost(ctx); err != nil {
		t.Fatal(err)
	}
	pid, err := st.CreateProject(ctx, &store.Project{Name: "App", Slug: "app", HostID: 0, CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	p := newTestProxy(t, st)

	_, err = p.resolveBackend(ctx, store.DomainMapping{ProjectID: pid, Domain: "app.example.com", Service: "web", TargetPort: 8080})
	if err != nil && strings.Contains(err.Error(), "no local host") {
		t.Errorf("a local host row exists, yet: %v", err)
	}
}
