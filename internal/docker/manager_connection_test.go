package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/client"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// managerFixture returns a Manager backed by an in-memory store holding one tcp
// host, plus that host's id.
func managerFixture(t *testing.T, address string) (*Manager, int64) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	id, err := st.CreateHost(t.Context(), &store.Host{Name: "remote", Kind: "tcp", Address: address})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(st)
	t.Cleanup(m.Close)
	return m, id
}

func (m *Manager) cachedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.clients)
}

// A dead connection must not stay cached.
//
// The client for an ssh host captures its SSH connection in the transport's
// dialer, so once that connection dies every later call fails against the same
// object — forever. Nothing evicted it: Disconnect is only called when a host is
// edited, deleted or re-trusted. The host therefore alerted as offline and never
// recovered, which is precisely the behaviour that teaches operators to ignore
// host alerts. Ping is where the health loop notices, so Ping is where it drops.
func TestPingEvictsADeadClient(t *testing.T) {
	var mu sync.Mutex
	healthy := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ok := healthy
		mu.Unlock()
		if !ok {
			// Close the connection abruptly, the way a dead peer does.
			hj, canHijack := w.(http.Hijacker)
			if canHijack {
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
					return
				}
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("API-Version", "1.43")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m, id := managerFixture(t, "tcp://"+strings.TrimPrefix(srv.URL, "http://"))

	if err := m.Ping(t.Context(), id); err != nil {
		t.Fatalf("a healthy host should ping: %v", err)
	}
	if got := m.cachedCount(); got != 1 {
		t.Fatalf("a successful ping should leave the client cached, have %d", got)
	}

	mu.Lock()
	healthy = false
	mu.Unlock()

	if err := m.Ping(t.Context(), id); err == nil {
		t.Fatal("a dead daemon should fail the ping")
	}
	if got := m.cachedCount(); got != 0 {
		t.Errorf("a failed ping must drop the cached client so the next call redials, have %d", got)
	}

	// And the recovery that used to be impossible: the host answers again.
	mu.Lock()
	healthy = true
	mu.Unlock()
	if err := m.Ping(t.Context(), id); err != nil {
		t.Errorf("the host came back, so the next ping must succeed: %v", err)
	}
}

// A successful ping must NOT churn the cache — otherwise every health sweep
// redials every host, which for ssh means a full handshake every 30 seconds.
func TestPingKeepsAHealthyClientCached(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("API-Version", "1.43")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m, id := managerFixture(t, "tcp://"+strings.TrimPrefix(srv.URL, "http://"))

	first, err := m.Client(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := m.Ping(t.Context(), id); err != nil {
			t.Fatalf("ping %d: %v", i, err)
		}
	}
	again, err := m.Client(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Error("healthy pings must keep the same cached client, not rebuild it")
	}
}

// A slow dial for one host must not block another.
//
// buildClient dials synchronously for ssh hosts, and ssh.Dial's handshake is not
// bounded by the context — so while the manager-wide mutex was held across it, a
// single asleep laptop froze every Docker call in the app, local host included.
func TestClientDialDoesNotBlockOtherHosts(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	slowID, err := st.CreateHost(t.Context(), &store.Host{Name: "slow", Kind: "tcp", Address: "tcp://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	fastID, err := st.CreateHost(t.Context(), &store.Host{Name: "fast", Kind: "tcp", Address: "tcp://127.0.0.1:2"})
	if err != nil {
		t.Fatal(err)
	}

	m := NewManager(st)
	t.Cleanup(m.Close)
	release := make(chan struct{})
	dialing := make(chan struct{})
	m.newClient = func(h *store.Host) (*client.Client, error) {
		if h.ID == slowID {
			close(dialing)
			<-release // stands in for an ssh handshake against a dead peer
		}
		return client.NewClientWithOpts(client.WithHost(h.Address))
	}

	go func() { _, _ = m.Client(context.Background(), slowID) }()
	<-dialing

	done := make(chan error, 1)
	go func() {
		_, err := m.Client(context.Background(), fastID)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the second host should connect while the first is dialing: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("a slow dial for one host blocked another — the lock is held across buildClient")
	}
	close(release)
}

// Two concurrent first-time callers must end up sharing one client, with the
// loser closed rather than leaked.
func TestConcurrentClientCallsShareOneConnection(t *testing.T) {
	m, id := managerFixture(t, "tcp://127.0.0.1:1")

	var built int
	var mu sync.Mutex
	m.newClient = func(h *store.Host) (*client.Client, error) {
		mu.Lock()
		built++
		mu.Unlock()
		time.Sleep(10 * time.Millisecond) // widen the race window
		return client.NewClientWithOpts(client.WithHost(h.Address))
	}

	const callers = 8
	var wg sync.WaitGroup
	got := make([]*client.Client, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := m.Client(context.Background(), id)
			if err != nil {
				t.Errorf("Client: %v", err)
				return
			}
			got[i] = c
		}()
	}
	wg.Wait()

	for i, c := range got {
		if c != got[0] {
			t.Fatalf("caller %d got a different client — the cache is not shared", i)
		}
	}
	if m.cachedCount() != 1 {
		t.Errorf("exactly one client should be cached, have %d", m.cachedCount())
	}
	mu.Lock()
	t.Logf("builders that ran: %d (racing callers may build more than one; only one is kept)", built)
	mu.Unlock()
}
