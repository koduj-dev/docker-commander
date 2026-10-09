package docker

import (
	"context"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Client records the host it connects to, with "no host given" (0) resolved to
// the concrete default, so an audit entry never reads the local daemon as 0.
// Creating a client doesn't contact the daemon, so this needs none.
func TestClientRecordsTheResolvedHost(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.EnsureLocalHost(ctx); err != nil {
		t.Fatal(err)
	}
	local, err := st.LocalHostID(ctx)
	if err != nil || local == 0 {
		t.Fatalf("local host id = %d, %v", local, err)
	}
	m := NewManager(st)

	rctx, rec := WithHostRecorder(ctx)
	if _, ok := rec.Host(); ok {
		t.Fatal("a fresh recorder already holds a host")
	}
	if _, err := m.Client(rctx, 0); err != nil {
		t.Fatal(err)
	}
	if got, ok := rec.Host(); !ok || got != local {
		t.Fatalf("recorded %d (%v), want the local host's own id %d", got, ok, local)
	}

	// The first host wins: a later lookup elsewhere doesn't relabel the action.
	other, err := st.CreateHost(ctx, &store.Host{Name: "other", Kind: "tcp", Address: "tcp://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = m.Client(rctx, other)
	if got, _ := rec.Host(); got != local {
		t.Fatalf("a second client call relabelled the action to %d", got)
	}
}

// Without a recorder in the context, Client works as before.
func TestClientWithoutARecorder(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.EnsureLocalHost(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(st).Client(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := HostRecorderFrom(context.Background()).Host(); ok {
		t.Fatal("a nil recorder reported a host")
	}
}
