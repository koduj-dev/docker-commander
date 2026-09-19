package proxy

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// TestResolveBackendCachedCoalescesConcurrentMisses is the regression for a
// P2 a code review caught before merge: the cache released its lock BEFORE
// calling resolveBackend, so every request arriving concurrently for a
// cold/expired domain observed the same miss and issued its OWN Docker
// lookup — a burst of ordinary traffic (or a deliberate one, every TTL
// window) fanned out into many full container-list calls instead of the
// one the cache exists to guarantee.
func TestResolveBackendCachedCoalescesConcurrentMisses(t *testing.T) {
	p := &Proxy{cache: newBackendCache(5 * time.Second)}
	var calls atomic.Int32
	release := make(chan struct{})
	p.resolveFn = func(ctx context.Context, m store.DomainMapping) (backend, error) {
		calls.Add(1)
		<-release // hold every concurrent caller here until they've all arrived
		return backend{addr: "127.0.0.1:9"}, nil
	}
	mapping := store.DomainMapping{Domain: "app.example.com"}

	const n = 20
	var wg sync.WaitGroup
	results := make([]backend, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			b, err := p.resolveBackendCached(context.Background(), mapping)
			if err != nil {
				t.Errorf("resolveBackendCached: %v", err)
			}
			results[i] = b
		}(i)
	}
	// Give every goroutine a chance to reach the (blocked) resolveFn before
	// releasing them, so this actually exercises the "all arrived while
	// cold" race, not just a lucky ordering.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Errorf("resolveFn was called %d times for %d concurrent requests on a cold cache, want exactly 1", got, n)
	}
	for i, b := range results {
		if b.addr != "127.0.0.1:9" {
			t.Errorf("result[%d].addr = %q, want the coalesced resolution's result", i, b.addr)
		}
	}
}

// TestResolveBackendCachedExpiresFromCompletionNotFromTheMiss is the other
// half of the same P2: expires used to be computed from the moment the miss
// was first observed, not from when resolution actually finished. A lookup
// taking close to (or longer than) the TTL stored an entry that was already
// expired the instant it landed, forcing the very next request to redo the
// same work.
func TestResolveBackendCachedExpiresFromCompletionNotFromTheMiss(t *testing.T) {
	const ttl = 100 * time.Millisecond
	p := &Proxy{cache: newBackendCache(ttl)}
	var calls atomic.Int32
	p.resolveFn = func(ctx context.Context, m store.DomainMapping) (backend, error) {
		calls.Add(1)
		time.Sleep(ttl) // resolution itself takes a full TTL window
		return backend{addr: "127.0.0.1:9"}, nil
	}
	mapping := store.DomainMapping{Domain: "app.example.com"}

	if _, err := p.resolveBackendCached(context.Background(), mapping); err != nil {
		t.Fatal(err)
	}
	// Immediately after the slow resolution completes, the cached entry
	// must still be fresh (its TTL window starts at completion, not at the
	// original miss ttl ago) — a second call right away must hit the cache.
	if _, err := p.resolveBackendCached(context.Background(), mapping); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("resolveFn was called %d times, want exactly 1 — the entry expired before it was even stored", got)
	}
}

// TestResolveBackendCachedSurvivesTheFirstCallersCancellation is the
// regression for a P2 a code review caught before merge: the coalesced
// flight used to run under the FIRST caller's own request context. If that
// caller disconnected mid-flight, net/http canceled its context, the
// lookup failed with context.Canceled, and every OTHER request sharing the
// flight — including ones that never disconnected — got that same
// cancellation error cached as the domain's result for a full TTL window.
// One impatient/disconnecting client must never turn into an outage for
// everyone else asking about the same domain.
func TestResolveBackendCachedSurvivesTheFirstCallersCancellation(t *testing.T) {
	p := &Proxy{cache: newBackendCache(5 * time.Second)}
	entered := make(chan struct{})
	release := make(chan struct{})
	p.resolveFn = func(ctx context.Context, m store.DomainMapping) (backend, error) {
		close(entered)
		<-release
		return backend{addr: "127.0.0.1:9"}, nil
	}
	mapping := store.DomainMapping{Domain: "app.example.com"}

	// Caller 1 starts the flight, then its OWN context is canceled while
	// resolveFn is still blocked inside it (simulating a disconnect).
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := p.resolveBackendCached(leaderCtx, mapping)
		leaderDone <- err
	}()
	<-entered // the flight has genuinely started
	cancelLeader()

	// Caller 2 joins the SAME flight with its own, healthy context. It must
	// get the real result once resolveFn finishes — not the leader's
	// cancellation.
	waiterDone := make(chan backend, 1)
	go func() {
		b, err := p.resolveBackendCached(context.Background(), mapping)
		if err != nil {
			t.Errorf("waiter: resolveBackendCached: %v", err)
		}
		waiterDone <- b
	}()
	time.Sleep(20 * time.Millisecond) // let caller 2 actually join the flight
	close(release)

	if err := <-leaderDone; err == nil {
		t.Error("the canceled leader's OWN call should report its own context error")
	}
	select {
	case b := <-waiterDone:
		if b.addr != "127.0.0.1:9" {
			t.Errorf("waiter got %+v, want the real resolved backend", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter never got a result — the leader's cancellation must not have killed the shared flight")
	}
}

// TestResolveBackendCachedWaiterCancellationDoesNotAffectOthers is the other
// half: a waiter canceling ITS OWN context while a flight is in progress
// must return promptly with that context's error, without canceling the
// flight for anyone else (or itself, if it called again with a fresh
// context — proven here via a second, healthy waiter).
func TestResolveBackendCachedWaiterCancellationDoesNotAffectOthers(t *testing.T) {
	p := &Proxy{cache: newBackendCache(5 * time.Second)}
	entered := make(chan struct{})
	release := make(chan struct{})
	p.resolveFn = func(ctx context.Context, m store.DomainMapping) (backend, error) {
		close(entered)
		<-release
		return backend{addr: "127.0.0.1:9"}, nil
	}
	mapping := store.DomainMapping{Domain: "app.example.com"}

	cancelableCtx, cancel := context.WithCancel(context.Background())
	canceledDone := make(chan error, 1)
	go func() {
		_, err := p.resolveBackendCached(cancelableCtx, mapping)
		canceledDone <- err
	}()
	<-entered

	start := time.Now()
	cancel()
	select {
	case err := <-canceledDone:
		if err == nil {
			t.Error("a caller whose own context was canceled must get that error back")
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("canceling the waiter's own context took %s to return — it must not wait for the flight", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a canceled waiter never returned")
	}

	// The flight itself must still be alive and correct for everyone else.
	healthyDone := make(chan backend, 1)
	go func() {
		b, err := p.resolveBackendCached(context.Background(), mapping)
		if err != nil {
			t.Errorf("healthy waiter: resolveBackendCached: %v", err)
		}
		healthyDone <- b
	}()
	close(release)
	select {
	case b := <-healthyDone:
		if b.addr != "127.0.0.1:9" {
			t.Errorf("healthy waiter got %+v, want the real resolved backend", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the flight never completed for the healthy waiter")
	}
}

// TestResolveBackendCachedIsKeyedByMappingIdentityNotDomain is the regression
// for a P1 the third code review caught: the cache (and its singleflight
// group) was keyed by domain alone, so deleting a mapping and recreating the
// same globally-unique domain for ANOTHER project inside one TTL window handed
// the new mapping the previous project's cached backend — public traffic for
// project B reaching project A's container under B's valid hostname and cert.
func TestResolveBackendCachedIsKeyedByMappingIdentityNotDomain(t *testing.T) {
	p := &Proxy{cache: newBackendCache(time.Minute)}
	var calls atomic.Int32
	p.resolveFn = func(ctx context.Context, m store.DomainMapping) (backend, error) {
		calls.Add(1)
		return backend{addr: "project-" + string(rune('0'+m.ProjectID))}, nil
	}
	a := store.DomainMapping{ID: 1, ProjectID: 1, Domain: "app.example.com", Service: "web", TargetPort: 80}
	b := store.DomainMapping{ID: 2, ProjectID: 2, Domain: "app.example.com", Service: "web", TargetPort: 80}

	got, _ := p.resolveBackendCached(context.Background(), a)
	if got.addr != "project-1" {
		t.Fatalf("mapping A resolved to %q", got.addr)
	}
	got, _ = p.resolveBackendCached(context.Background(), b)
	if got.addr != "project-2" {
		t.Errorf("mapping B (same domain, different project) got %q — the previous project's cached backend leaked across mappings", got.addr)
	}
	// Same mapping again is still a hit: the fix must not disable the cache.
	if _, _ = p.resolveBackendCached(context.Background(), a); calls.Load() != 2 {
		t.Errorf("resolveFn ran %d times, want 2 (A once, B once; the repeat of A is a cache hit)", calls.Load())
	}

	// Repointing the SAME row (same ID/project, new service or port; every
	// edit bumps UpdatedAt) must miss as well.
	repointed := a
	repointed.TargetPort = 8080
	repointed.UpdatedAt = time.Now()
	p.resolveBackendCached(context.Background(), repointed)
	if calls.Load() != 3 {
		t.Errorf("a repointed mapping reused the stale entry (resolveFn calls = %d, want 3)", calls.Load())
	}
}

// The singleflight key must carry the same identity: an in-flight lookup for
// mapping A must not be joined by a request for mapping B on the same domain.
func TestResolveBackendCachedDoesNotJoinAnotherMappingsFlight(t *testing.T) {
	p := &Proxy{cache: newBackendCache(time.Minute)}
	entered := make(chan struct{})
	release := make(chan struct{})
	p.resolveFn = func(ctx context.Context, m store.DomainMapping) (backend, error) {
		if m.ProjectID == 1 {
			close(entered)
			<-release
		}
		return backend{addr: "project-" + string(rune('0'+m.ProjectID))}, nil
	}
	a := store.DomainMapping{ID: 1, ProjectID: 1, Domain: "app.example.com", Service: "web", TargetPort: 80}
	b := store.DomainMapping{ID: 2, ProjectID: 2, Domain: "app.example.com", Service: "web", TargetPort: 80}

	aDone := make(chan backend, 1)
	go func() {
		got, _ := p.resolveBackendCached(context.Background(), a)
		aDone <- got
	}()
	<-entered // A's flight is genuinely in progress

	bDone := make(chan backend, 1)
	go func() {
		got, _ := p.resolveBackendCached(context.Background(), b)
		bDone <- got
	}()
	select {
	case got := <-bDone:
		if got.addr != "project-2" {
			t.Errorf("mapping B got %q while A's flight was in progress, want its own result", got.addr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mapping B blocked on (or joined) mapping A's flight")
	}
	close(release)
	if got := <-aDone; got.addr != "project-1" {
		t.Errorf("mapping A got %q", got.addr)
	}
}

// Expired entries must actually be removed (P3): with identity-scoped keys a
// superseded mapping's entry is never looked up again, so without a sweep the
// map grows by one key per distinct mapping that ever received a request.
func TestBackendCacheDropsExpiredEntries(t *testing.T) {
	p := &Proxy{cache: newBackendCache(20 * time.Millisecond)}
	p.resolveFn = func(ctx context.Context, m store.DomainMapping) (backend, error) {
		return backend{addr: "127.0.0.1:9"}, nil
	}
	for i := int64(1); i <= 50; i++ {
		p.resolveBackendCached(context.Background(), store.DomainMapping{ID: i, ProjectID: i, Domain: "churn.example.com", Service: "web", TargetPort: 80})
	}
	time.Sleep(60 * time.Millisecond) // everything above has expired
	p.resolveBackendCached(context.Background(), store.DomainMapping{ID: 999, ProjectID: 999, Domain: "live.example.com", Service: "web", TargetPort: 80})

	p.cache.mu.Lock()
	n := len(p.cache.m)
	p.cache.mu.Unlock()
	if n != 1 {
		t.Errorf("cache holds %d entries, want 1 — expired entries were never removed", n)
	}
}
