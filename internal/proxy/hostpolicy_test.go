package proxy

import (
	"context"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

func newTestStore(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, context.Background()
}

func newTestProxy(t *testing.T, st *store.Store) *Proxy {
	t.Helper()
	// docker.NewManager is safe to construct without a daemon (it only
	// dials lazily, on first real use) — needed so resolveBackend's
	// p.docker.ListStacks call has a non-nil receiver even in tests that
	// never expect it to succeed (no daemon in the test sandbox). No real
	// ACME cache/network is touched either, since these tests call
	// p.HostPolicy/CombinedHandler directly, never p.mgr.GetCertificate.
	return New(st, docker.NewManager(st), Config{CacheDir: t.TempDir()})
}

// TestHostPolicyAcceptsALocalMapping is the positive case every rejection
// test below is contrasted against.
func TestHostPolicyAcceptsALocalMapping(t *testing.T) {
	st, ctx := newTestStore(t)
	p := newTestProxy(t, st)

	pid, err := st.CreateProject(ctx, &store.Project{Name: "App", Slug: "app", HostID: 0, CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateDomainMapping(ctx, pid, "app.example.com", "web", 8080, "acme", "admin"); err != nil {
		t.Fatal(err)
	}

	if err := p.HostPolicy(ctx, "app.example.com"); err != nil {
		t.Errorf("a local project's own mapping must be accepted, got %v", err)
	}
}

func TestHostPolicyRejectsAnUnmappedDomain(t *testing.T) {
	st, ctx := newTestStore(t)
	p := newTestProxy(t, st)

	if err := p.HostPolicy(ctx, "nowhere.example.com"); err == nil {
		t.Error("a domain with no mapping at all must be refused")
	}
}

// TestHostPolicyRejectsARemoteProjectsMapping is the core scope boundary for
// this phase: local-host projects only, no remote-host reachability.
func TestHostPolicyRejectsARemoteProjectsMapping(t *testing.T) {
	st, ctx := newTestStore(t)
	p := newTestProxy(t, st)

	hostID, err := st.CreateHost(ctx, &store.Host{Name: "remote-box", Kind: "ssh", Address: "10.0.0.5"})
	if err != nil {
		t.Fatal(err)
	}
	pid, err := st.CreateProject(ctx, &store.Project{Name: "Remote App", Slug: "remoteapp", HostID: hostID, CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateDomainMapping(ctx, pid, "remote.example.com", "web", 8080, "acme", "admin"); err != nil {
		t.Fatal(err)
	}

	if err := p.HostPolicy(ctx, "remote.example.com"); err == nil {
		t.Error("a remote-host project's mapping must be refused — this phase is local-host only")
	}
}

// TestHostPolicyRejectsAnOrphanedMapping proves the policy itself — not the
// app-level project-delete cascade that normally removes a project's
// mappings alongside it — is the real safety boundary here. domain_mappings
// has no declared foreign key on project_id (matching this schema's existing
// convention elsewhere), so a row pointing at a project id that was never
// real is constructed directly, standing in for "the cascade didn't run" —
// via a bug, a direct DB edit, or (as here) a row that was simply never
// backed by a real project in the first place — rather than exercising
// DeleteProject, which WOULD clean this up and so wouldn't test the policy
// at all.
func TestHostPolicyRejectsAnOrphanedMapping(t *testing.T) {
	st, ctx := newTestStore(t)
	p := newTestProxy(t, st)

	const neverARealProjectID = 999999
	if _, err := st.CreateDomainMapping(ctx, neverARealProjectID, "orphan.example.com", "web", 8080, "acme", "admin"); err != nil {
		t.Fatal(err)
	}

	if err := p.HostPolicy(ctx, "orphan.example.com"); err == nil {
		t.Error("a mapping whose project doesn't exist must be refused, not just left to the cascade")
	}
}
