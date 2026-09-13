package api

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/monitor"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// imageUpdatesToNotify is the only decision in the poller worth unit
// testing in isolation: everything around it (real containers, a real
// registry) needs a live Docker daemon, but the dedup logic — notify once
// per newly-observed digest, never again for the same one, again if it
// changes — is pure and deserves its own mutation-provable coverage.
func TestImageUpdatesToNotify(t *testing.T) {
	digest := func(service, to string) docker.ServiceChange {
		return docker.ServiceChange{Service: service, Kind: "digest", To: to}
	}

	t.Run("a brand-new digest drift is notified", func(t *testing.T) {
		changes := []docker.ServiceChange{digest("web", "sha256:new")}
		got := imageUpdatesToNotify(changes, map[string]string{})
		if len(got) != 1 || got[0].Service != "web" {
			t.Errorf("got %+v, want the web digest change", got)
		}
	})

	t.Run("the same digest already notified is not renotified", func(t *testing.T) {
		changes := []docker.ServiceChange{digest("web", "sha256:new")}
		got := imageUpdatesToNotify(changes, map[string]string{"web": "sha256:new"})
		if len(got) != 0 {
			t.Errorf("expected no notification for an already-notified digest, got %+v", got)
		}
	})

	t.Run("a digest that moved again since the last notification is notified", func(t *testing.T) {
		changes := []docker.ServiceChange{digest("web", "sha256:newer")}
		got := imageUpdatesToNotify(changes, map[string]string{"web": "sha256:old-notified"})
		if len(got) != 1 || got[0].To != "sha256:newer" {
			t.Errorf("got %+v, want a fresh notification for the newer digest", got)
		}
	})

	t.Run("non-digest changes (image/env/ports/...) are never notified", func(t *testing.T) {
		changes := []docker.ServiceChange{
			{Service: "web", Kind: "image", To: "app:2.0"},
			{Service: "web", Kind: "env", To: "changed"},
		}
		got := imageUpdatesToNotify(changes, map[string]string{})
		if len(got) != 0 {
			t.Errorf("only digest-kind changes should ever notify, got %+v", got)
		}
	})

	t.Run("multiple services are handled independently", func(t *testing.T) {
		changes := []docker.ServiceChange{digest("web", "sha256:a"), digest("worker", "sha256:b")}
		got := imageUpdatesToNotify(changes, map[string]string{"web": "sha256:a"}) // web already notified, worker is not
		want := []docker.ServiceChange{digest("worker", "sha256:b")}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
}

// hostForProject/projectsToPoll: a project's HostID==0 ("local", the
// project model's own convention) must resolve against whichever host row
// is Kind=="local" — store.ListHosts never returns a row with id 0 itself
// (EnsureLocalHost gives it a real, auto-incremented id).
func TestHostForProject(t *testing.T) {
	hosts := []store.Host{
		{ID: 1, Name: "local-host", Kind: "local"},
		{ID: 2, Name: "prod", Kind: "tcp", Disabled: true},
	}
	if h := hostForProject(0, hosts); h == nil || h.Name != "local-host" {
		t.Errorf("HostID 0 should resolve to the local-kind host, got %+v", h)
	}
	if h := hostForProject(2, hosts); h == nil || h.Name != "prod" {
		t.Errorf("a real host id should resolve directly, got %+v", h)
	}
	if h := hostForProject(999, hosts); h != nil {
		t.Errorf("an unknown host id should resolve to nil, got %+v", h)
	}
}

func TestProjectsToPoll(t *testing.T) {
	hosts := []store.Host{
		{ID: 1, Name: "local-host", Kind: "local", Disabled: false},
		{ID: 2, Name: "prod", Kind: "tcp", Disabled: true},
	}
	projects := []store.Project{
		{ID: 10, Name: "on-local", HostID: 0},       // local alias — must be polled (local isn't disabled)
		{ID: 11, Name: "on-prod", HostID: 2},        // disabled host — must be skipped
		{ID: 12, Name: "unknown-host", HostID: 999}, // unresolvable host — polled (fails open, not silently dropped)
	}
	got := projectsToPoll(projects, hosts)
	var names []string
	for _, p := range got {
		names = append(names, p.Name)
	}
	want := []string{"on-local", "unknown-host"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("projectsToPoll = %v, want %v", names, want)
	}
}

// The exact bug this guards against: a project on HostID 0 ("local") must be
// skipped when the LOCAL host itself is disabled — not just when a remote
// host is. Getting the 0-is-local alias wrong here means a disabled local
// host is never skipped at all, since no project ever has HostID matching
// the local host's real row id.
func TestProjectsToPoll_DisabledLocalHostIsSkipped(t *testing.T) {
	hosts := []store.Host{{ID: 1, Name: "local-host", Kind: "local", Disabled: true}}
	projects := []store.Project{{ID: 10, Name: "on-local", HostID: 0}}
	got := projectsToPoll(projects, hosts)
	if len(got) != 0 {
		t.Errorf("a project on a disabled local host must be skipped, got %+v", got)
	}
}

// newImageUpdateTestServer builds a Server backed by a real store and a real
// Monitor (so NotifySystem's insert + maintenance-window check actually
// run), but no real Docker manager — pollImageUpdatesForProject's own Docker/
// compose/registry work is supplied by an injected fake buildPreview instead.
func newImageUpdateTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Server{store: st, monitor: monitor.New(st, nil, nil)}, st
}

func fakeBuildPreview(prev docker.DeployPreview, err error) func(context.Context, *store.Project) (docker.DeployPreview, map[string]bool, error) {
	return fakeBuildPreviewChecked(prev, nil, err)
}

// fakeBuildPreviewChecked lets a test control exactly which services were
// "confirmed unchanged" by the digest check, independent of what's in
// prev.Changes — needed to simulate a registry/inspect lookup failure (a
// service that is neither digest-changed nor confirmed unchanged).
func fakeBuildPreviewChecked(prev docker.DeployPreview, confirmedUnchanged map[string]bool, err error) func(context.Context, *store.Project) (docker.DeployPreview, map[string]bool, error) {
	return func(context.Context, *store.Project) (docker.DeployPreview, map[string]bool, error) {
		return prev, confirmedUnchanged, err
	}
}

func TestPollImageUpdatesForProject_NotifiesAndPersistsOnFirstDrift(t *testing.T) {
	srv, st := newImageUpdateTestServer(t)
	p := &store.Project{ID: 1, Name: "shop", Slug: "shop"}
	prev := docker.DeployPreview{
		Running: []docker.ServiceSpec{{Name: "web", Image: "app:1"}},
		Changes: []docker.ServiceChange{{Service: "web", Kind: "digest", To: "sha256:new", Detail: "d1 -> d2"}},
	}
	srv.pollImageUpdatesForProject(context.Background(), p, nil, fakeBuildPreview(prev, nil))

	digests, err := st.LastNotifiedImageDigests(context.Background(), p.ID)
	if err != nil || digests["web"] != "sha256:new" {
		t.Fatalf("expected the new digest to be persisted, got %+v (err %v)", digests, err)
	}
	events, _, err := st.ListAlertEvents(context.Background(), store.AlertQuery{})
	if err != nil || len(events) != 1 || events[0].Type != "image_update" || events[0].RuleName != "Image update" {
		t.Fatalf("expected one image_update event with a rule label, got %+v (err %v)", events, err)
	}
}

func TestPollImageUpdatesForProject_SameDriftIsNotRenotified(t *testing.T) {
	srv, st := newImageUpdateTestServer(t)
	p := &store.Project{ID: 1, Name: "shop", Slug: "shop"}
	prev := docker.DeployPreview{
		Running: []docker.ServiceSpec{{Name: "web", Image: "app:1"}},
		Changes: []docker.ServiceChange{{Service: "web", Kind: "digest", To: "sha256:new"}},
	}
	build := fakeBuildPreview(prev, nil)
	ctx := context.Background()
	srv.pollImageUpdatesForProject(ctx, p, nil, build)
	srv.pollImageUpdatesForProject(ctx, p, nil, build) // same drift again

	events, _, err := st.ListAlertEvents(ctx, store.AlertQuery{})
	if err != nil || len(events) != 1 {
		t.Fatalf("expected exactly one event across two polls of the same drift, got %d (err %v)", len(events), err)
	}
}

// A drift that resolves (the service is running with no digest change) must
// clear the stale dedup row — otherwise a LATER drift back to that exact
// same digest is wrongly suppressed as "already notified".
func TestPollImageUpdatesForProject_ResolvedDriftClearsStateForFutureRenotify(t *testing.T) {
	srv, st := newImageUpdateTestServer(t)
	p := &store.Project{ID: 1, Name: "shop", Slug: "shop"}
	ctx := context.Background()

	// Tick 1: drift to B, notified.
	srv.pollImageUpdatesForProject(ctx, p, nil, fakeBuildPreview(docker.DeployPreview{
		Running: []docker.ServiceSpec{{Name: "web", Image: "app:1"}},
		Changes: []docker.ServiceChange{{Service: "web", Kind: "digest", To: "sha256:B"}},
	}, nil))

	// Tick 2: redeployed to B, so no more drift, AND positively confirmed
	// unchanged (registry + running digest both resolved and matched) —
	// must clear the dedup row.
	srv.pollImageUpdatesForProject(ctx, p, nil, fakeBuildPreviewChecked(docker.DeployPreview{
		Running: []docker.ServiceSpec{{Name: "web", Image: "app:1"}},
		Changes: nil,
	}, map[string]bool{"web": true}, nil))
	// Cleared means "no longer equal to any real digest a future drift could
	// report" — an empty stored value satisfies that (imageUpdatesToNotify
	// only ever suppresses an exact match, and a real digest is never "").
	digests, _ := st.LastNotifiedImageDigests(ctx, p.ID)
	if digests["web"] != "" {
		t.Fatalf("resolved drift should clear the dedup row, got %+v", digests)
	}

	// Tick 3: drifts to B again — must notify again, not be suppressed by
	// stale state from tick 1.
	srv.pollImageUpdatesForProject(ctx, p, nil, fakeBuildPreview(docker.DeployPreview{
		Running: []docker.ServiceSpec{{Name: "web", Image: "app:1"}},
		Changes: []docker.ServiceChange{{Service: "web", Kind: "digest", To: "sha256:B"}},
	}, nil))

	events, _, err := st.ListAlertEvents(ctx, store.AlertQuery{})
	if err != nil || len(events) != 2 {
		t.Fatalf("expected 2 notifications (tick 1 and tick 3), got %d (err %v)", len(events), err)
	}
}

// A registry/inspect lookup failure for a service must NOT be treated as
// "confirmed unchanged": the digest check for a service can fail (registry
// timeout, auth error, docker inspect error) without producing a "digest"
// change, which used to be indistinguishable out here from a genuine
// resolve — and used to wrongly clear that service's dedup row, causing the
// SAME already-notified drift to renotify once the registry recovered.
func TestPollImageUpdatesForProject_UnconfirmedLookupDoesNotClearDedupState(t *testing.T) {
	srv, st := newImageUpdateTestServer(t)
	p := &store.Project{ID: 1, Name: "shop", Slug: "shop"}
	ctx := context.Background()

	// Tick 1: drift to B, notified.
	srv.pollImageUpdatesForProject(ctx, p, nil, fakeBuildPreview(docker.DeployPreview{
		Running: []docker.ServiceSpec{{Name: "web", Image: "app:1"}},
		Changes: []docker.ServiceChange{{Service: "web", Kind: "digest", To: "sha256:B"}},
	}, nil))

	// Tick 2: the registry/inspect lookup failed this time — no digest
	// change reported, AND not in the confirmed-unchanged set. Must NOT
	// clear the dedup row.
	srv.pollImageUpdatesForProject(ctx, p, nil, fakeBuildPreviewChecked(docker.DeployPreview{
		Running: []docker.ServiceSpec{{Name: "web", Image: "app:1"}},
		Changes: nil,
	}, map[string]bool{}, nil))

	digests, _ := st.LastNotifiedImageDigests(ctx, p.ID)
	if digests["web"] != "sha256:B" {
		t.Fatalf("dedup state must survive an unconfirmed lookup, got %+v", digests)
	}

	// Tick 3: the same still-unresolved drift is seen again — must NOT
	// renotify, since it was never actually confirmed resolved.
	srv.pollImageUpdatesForProject(ctx, p, nil, fakeBuildPreview(docker.DeployPreview{
		Running: []docker.ServiceSpec{{Name: "web", Image: "app:1"}},
		Changes: []docker.ServiceChange{{Service: "web", Kind: "digest", To: "sha256:B"}},
	}, nil))

	events, _, err := st.ListAlertEvents(ctx, store.AlertQuery{})
	if err != nil || len(events) != 1 {
		t.Fatalf("expected exactly one notification across all three ticks, got %d (err %v)", len(events), err)
	}
}

// If NotifySystem never actually records the event (its own store is
// unusable — simulated here by pointing the Monitor at a separate, already-
// closed store, so pollImageUpdatesForProject's OWN store, checked below,
// stays perfectly healthy), the dedup state must not be persisted either —
// otherwise a notification nobody will ever see in the feed would still be
// treated as delivered, forever.
func TestPollImageUpdatesForProject_NotifyFailureDoesNotPersistDedupState(t *testing.T) {
	srv, st := newImageUpdateTestServer(t)
	failingStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	failingStore.Close() // closed immediately -> InsertAlertEvent always fails
	srv.monitor = monitor.New(failingStore, nil, nil)

	p := &store.Project{ID: 1, Name: "shop", Slug: "shop"}
	prev := docker.DeployPreview{
		Running: []docker.ServiceSpec{{Name: "web", Image: "app:1"}},
		Changes: []docker.ServiceChange{{Service: "web", Kind: "digest", To: "sha256:new"}},
	}
	srv.pollImageUpdatesForProject(context.Background(), p, nil, fakeBuildPreview(prev, nil))

	digests, err := st.LastNotifiedImageDigests(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := digests["web"]; ok {
		t.Errorf("dedup state must not be persisted when the notification was never recorded, got %+v", digests)
	}
}

// buildProjectImagePreviewChecked needs a real Docker daemon + compose CLI
// (see docker.ComposeAvailable), same gating other compose-integration tests
// in this package already use.
func TestBuildProjectImagePreviewChecked_NoStackReturnsNotDeployed(t *testing.T) {
	if testing.Short() {
		t.Skip("needs the docker compose CLI; skipped under -short")
	}
	if !docker.ComposeAvailable(context.Background()) {
		t.Skip("docker compose CLI not available")
	}
	srv, st := newImageUpdateTestServer(t)
	if err := st.EnsureLocalHost(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv.docker = docker.NewManager(srv.store)
	p := &store.Project{ID: 1, Name: "shop", Slug: "dctest-no-such-project", HostID: 0}
	if _, _, err := srv.buildProjectImagePreviewChecked(context.Background(), p); !errors.Is(err, errProjectNotDeployed) {
		t.Errorf("expected errProjectNotDeployed for a project with no running stack, got %v", err)
	}
}
