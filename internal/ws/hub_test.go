package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

// fakeStreamer emits one sample/line then blocks until the subscription's
// context is cancelled, mimicking a live stream.
type fakeStreamer struct{}

func (fakeStreamer) StreamStats(ctx context.Context, _ int64, id string, emit func(docker.StatsSample)) error {
	emit(docker.StatsSample{ContainerID: id, CPUPercent: 12.5})
	<-ctx.Done()
	return nil
}

func (fakeStreamer) StreamLogs(ctx context.Context, _ int64, _ string, _ bool, _ string, emit func(docker.LogLine)) error {
	emit(docker.LogLine{Stream: "stdout", Message: "hello from stream"})
	<-ctx.Done()
	return nil
}

func TestHubSubscribeStatsAndLogs(t *testing.T) {
	hub := NewHub(fakeStreamer{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		hub.Serve(r.Context(), c, nil) // nil allow → all channels permitted
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// subscribe to stats + logs, then ping.
	must := func(m clientMsg) {
		if err := wsjson.Write(ctx, conn, m); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	must(clientMsg{Type: "subscribe", SubID: "s1", Channel: "stats", ContainerID: "c1"})
	must(clientMsg{Type: "subscribe", SubID: "l1", Channel: "logs", ContainerID: "c1"})
	must(clientMsg{Type: "ping"})

	// Collect frames until we've seen a stats, a log and a pong (or time out).
	var sawStats, sawLog, sawPong bool
	for i := 0; i < 10 && !(sawStats && sawLog && sawPong); i++ {
		var msg serverMsg
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			t.Fatalf("read: %v", err)
		}
		switch msg.Type {
		case "stats":
			sawStats = true
		case "log":
			sawLog = true
		case "pong":
			sawPong = true
		}
	}
	if !sawStats || !sawLog || !sawPong {
		t.Errorf("missing frames: stats=%v log=%v pong=%v", sawStats, sawLog, sawPong)
	}

	// Unknown channel → an error frame.
	must(clientMsg{Type: "subscribe", SubID: "bad", Channel: "nope", ContainerID: "c1"})
	for i := 0; i < 10; i++ {
		var msg serverMsg
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			t.Fatalf("read err frame: %v", err)
		}
		if msg.Type == "error" && msg.SubID == "bad" {
			return // success
		}
	}
	t.Error("expected an error frame for an unknown channel")
}

// TestHubChannelGate verifies the per-channel RBAC gate: a channel the allow
// predicate rejects yields a permission error and never starts a stream, while
// an allowed channel streams normally.
func TestHubChannelGate(t *testing.T) {
	hub := NewHub(fakeStreamer{})
	// Permit "stats", deny everything else (e.g. "logs").
	allow := func(channel string, _ int64) bool { return channel == "stats" }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		hub.Serve(r.Context(), c, allow)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	must := func(m clientMsg) {
		if err := wsjson.Write(ctx, conn, m); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	// Denied channel → a permission error frame, no stream.
	must(clientMsg{Type: "subscribe", SubID: "l1", Channel: "logs", ContainerID: "c1"})
	// Allowed channel → a stats frame.
	must(clientMsg{Type: "subscribe", SubID: "s1", Channel: "stats", ContainerID: "c1"})

	var deniedErr, sawStats bool
	for i := 0; i < 10 && !(deniedErr && sawStats); i++ {
		var msg serverMsg
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			t.Fatalf("read: %v", err)
		}
		switch {
		case msg.Type == "error" && msg.SubID == "l1":
			if !strings.Contains(msg.Message, "permitted") {
				t.Errorf("denied channel error = %q, want a permission message", msg.Message)
			}
			deniedErr = true
		case msg.Type == "stats" && msg.SubID == "s1":
			sawStats = true
		case msg.Type == "log":
			t.Error("denied 'logs' channel must not stream a log frame")
		}
	}
	if !deniedErr || !sawStats {
		t.Errorf("gate wrong: deniedErr=%v sawStats=%v", deniedErr, sawStats)
	}
}

// gatedStreamer holds the first stream open until the test releases it, and
// reports when each later stream is cancelled. That is enough to stage the
// re-subscribe race deterministically.
type gatedStreamer struct {
	mu        sync.Mutex
	started   int
	release   chan struct{} // closed to end stream #1
	cancelled chan int      // stream index, when its context is cancelled
}

func newGatedStreamer() *gatedStreamer {
	return &gatedStreamer{release: make(chan struct{}), cancelled: make(chan int, 8)}
}

func (g *gatedStreamer) StreamStats(ctx context.Context, _ int64, id string, emit func(docker.StatsSample)) error {
	g.mu.Lock()
	g.started++
	n := g.started
	g.mu.Unlock()
	emit(docker.StatsSample{ContainerID: id})
	if n == 1 {
		// The first stream ignores cancellation for a moment, the way a real one
		// does while it is still draining, and ends only when the test says so.
		<-g.release
		return nil
	}
	<-ctx.Done()
	g.cancelled <- n
	return nil
}

func (g *gatedStreamer) StreamLogs(ctx context.Context, _ int64, _ string, _ bool, _ string, _ func(docker.LogLine)) error {
	<-ctx.Done()
	return nil
}

// PENTEST-adjacent (correctness, but it leaks a stream): re-subscribing with the
// same id must not let the OLD stream's cleanup delete the NEW subscription.
//
// The frontend reuses deterministic ids ("stats:<container>"), so leaving a
// container page and coming straight back — or React StrictMode's double mount —
// hits this. The old stream's deferred finish removed whatever sat under the id,
// which by then was the new subscription: the new stream kept running with
// nothing able to cancel it short of closing the socket, and the client was told
// its live subscription had ended.
func TestHubResubscribeDoesNotCancelTheNewSubscription(t *testing.T) {
	streamer := newGatedStreamer()
	hub := NewHub(streamer)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		hub.Serve(r.Context(), c, nil)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	sub := clientMsg{Type: "subscribe", SubID: "stats:c1", Channel: "stats", ContainerID: "c1"}
	if err := wsjson.Write(ctx, conn, sub); err != nil {
		t.Fatal(err)
	}
	readFrame(t, ctx, conn) // the first stream's sample

	// Re-subscribe under the same id: the old stream is cancelled but has not
	// returned yet, so its finish is still pending.
	if err := wsjson.Write(ctx, conn, sub); err != nil {
		t.Fatal(err)
	}
	readFrame(t, ctx, conn) // the second stream's sample
	close(streamer.release) // now let the first stream return and run its finish

	// Ask for the second stream to stop. If the stale finish deleted its entry,
	// the unsubscribe finds nothing and the stream runs on.
	if err := wsjson.Write(ctx, conn, clientMsg{Type: "unsubscribe", SubID: "stats:c1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-streamer.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("the live subscription could not be cancelled — its entry was deleted by the previous stream's cleanup")
	}
}

// readFrame reads one server frame, failing the test on error.
func readFrame(t *testing.T, ctx context.Context, conn *websocket.Conn) serverMsg {
	t.Helper()
	var m serverMsg
	if err := wsjson.Read(ctx, conn, &m); err != nil {
		t.Fatalf("read: %v", err)
	}
	return m
}
