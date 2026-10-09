package proxy

import (
	"testing"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// TestResolveBackendRefusesARemoteLocalDaemon is the regression for a P1 a
// code review caught before merge: a store.Host row of Kind "local"
// (HostID == 0) does not guarantee "the same machine, or network
// namespace, this process runs in" — internal/docker's buildClient honors
// DOCKER_HOST for a "local" host, so an operator can point the app's own
// "local" daemon at a remote tcp:// endpoint while HostID stays 0.
// bindDialAddr's loopback/reported-IP translation is only meaningful when
// the daemon really is reachable from this process's own network
// namespace — dialing a bind IP reported by a REMOTE daemon would target
// this process's own loopback instead, never the actual workload.
// resolveBackend must detect a non-local (non unix/npipe socket) "local"
// daemon and refuse rather than guess.
//
// No real Docker daemon is needed here: constructing a *client.Client is
// lazy (no dial until first API call), so this runs as a fast unit test,
// not a -short-skipped integration one — it never gets far enough to touch
// the network.
func TestResolveBackendRefusesARemoteLocalDaemon(t *testing.T) {
	st, ctx := newTestStore(t)
	// A "local" host row (Kind == "local", so defaultHostID picks it for
	// hostID <= 0) whose Address is a remote TCP endpoint — the exact shape
	// DOCKER_HOST-via-FromEnv or an explicit Address can produce.
	if _, err := st.CreateHost(ctx, &store.Host{Name: "local", Kind: "local", Address: "tcp://192.0.2.1:2375"}); err != nil {
		t.Fatal(err)
	}
	pid, err := st.CreateProject(ctx, &store.Project{Name: "App", Slug: "app", HostID: 0, CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	dm := docker.NewManager(st)
	t.Cleanup(dm.Close)
	p := New(st, dm, Config{CacheDir: t.TempDir()})

	mapping := store.DomainMapping{ProjectID: pid, Domain: "app.test", Service: "web", TargetPort: 8080}
	if _, err := p.resolveBackend(ctx, mapping); err == nil {
		t.Error("a 'local' daemon that is actually a remote tcp:// endpoint must be refused, not dialed as if it were loopback")
	}
}
