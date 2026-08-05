package ws

import (
	"context"
	"fmt"
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

// Cancelling ONE subscription must not close the whole connection.
//
// coder/websocket registers a context.AfterFunc for the context handed to Write,
// and that function closes the entire connection (conn.go, setupWriteTimeout). So
// writing a sample under the subscription's own context means: unsubscribe while a
// frame is in flight and every other subscription on that socket dies with it.
//
// In the app that is leaving a container page while its stats are streaming. In CI
// it showed up as TestHubResubscribeDoesNotCancelTheNewSubscription failing with
// "failed to read frame header: EOF" — twice, on a loaded runner, and never on a
// quiet laptop.

// chattyStreamer emits continuously, so an unsubscribe is overwhelmingly likely to
// land while a write is in progress. That is what makes this deterministic enough
// to be worth having.
type chattyStreamer struct{}

func (chattyStreamer) StreamStats(ctx context.Context, _ int64, id string, emit func(docker.StatsSample)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		emit(docker.StatsSample{ContainerID: id})
	}
}

func (chattyStreamer) StreamLogs(ctx context.Context, _ int64, _ string, _ bool, _ string, _ func(docker.LogLine)) error {
	<-ctx.Done()
	return nil
}

func TestUnsubscribeDoesNotCloseTheConnection(t *testing.T) {
	hub := NewHub(chattyStreamer{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		hub.Serve(r.Context(), c, nil)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(1 << 20)

	for round := 0; round < 20; round++ {
		if err := wsjson.Write(ctx, conn, clientMsg{
			Type: "subscribe", SubID: "stats:c1", Channel: "stats", ContainerID: "c1",
		}); err != nil {
			t.Fatalf("round %d: subscribe: %v", round, err)
		}
		// Read one sample, so the stream is definitely mid-flight, then stop it.
		if err := readOne(ctx, conn); err != nil {
			t.Fatalf("round %d: reading a sample: %v", round, err)
		}
		if err := wsjson.Write(ctx, conn, clientMsg{Type: "unsubscribe", SubID: "stats:c1"}); err != nil {
			t.Fatalf("round %d: unsubscribe: %v", round, err)
		}

		// The connection must still be usable. Drain whatever samples were already
		// queued, then require an answer to a ping.
		if err := pingSurvives(ctx, conn); err != nil {
			t.Fatalf("round %d: the connection died when a subscription was cancelled: %v", round, err)
		}
	}
}

func readOne(ctx context.Context, conn *websocket.Conn) error {
	var msg serverMsg
	return wsjson.Read(ctx, conn, &msg)
}

// pingSurvives sends a ping and reads until the pong comes back, skipping the
// samples still in flight from the cancelled stream.
func pingSurvives(ctx context.Context, conn *websocket.Conn) error {
	if err := wsjson.Write(ctx, conn, clientMsg{Type: "ping", SubID: "alive"}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		var msg serverMsg
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			return err
		}
		if msg.Type == "pong" {
			return nil
		}
	}
}

// …and the tail of a REPLACED subscription must not reach its replacement.
//
// Subscription ids are deterministic ("stats:<container>"), so leaving a container
// page and coming straight back reuses the id. The old stream is still winding down
// and its last frames now survive the cancellation (they used to be "impossible"
// only because cancelling killed the socket) — delivered under that id, they would
// arrive at the new handler as a duplicate log line or a stale sample.
type replacedStreamer struct {
	release chan struct{}
	// tailWritten closes after the old stream's last emit has RETURNED, i.e. after
	// the write was either sent or dropped. Without it the test can finish while
	// that frame is still in flight: the pong overtakes it, the test returns happy,
	// and a build with no guard at all passes. (Measured: it slipped through 2 runs
	// in 600.) Waiting on this puts the frame on the wire — if it is written at all
	// — before the ping that ends the test.
	tailWritten chan struct{}
	mu          sync.Mutex
	n           int
}

func (r *replacedStreamer) StreamStats(ctx context.Context, _ int64, _ string, emit func(docker.StatsSample)) error {
	r.mu.Lock()
	r.n++
	mine := r.n
	r.mu.Unlock()

	// Each stream tags its frames, so the test can tell whose they are.
	emit(docker.StatsSample{ContainerID: fmt.Sprintf("stream-%d", mine)})
	if mine == 1 {
		// Wind down slowly: emit once more only after the test has replaced this
		// subscription, which is exactly the frame that must be dropped.
		<-r.release
		emit(docker.StatsSample{ContainerID: "tail-of-stream-1"})
		close(r.tailWritten)
		return nil
	}
	<-ctx.Done()
	return nil
}

func (r *replacedStreamer) StreamLogs(ctx context.Context, _ int64, _ string, _ bool, _ string, _ func(docker.LogLine)) error {
	<-ctx.Done()
	return nil
}

func TestReplacedSubscriptionsTailIsDropped(t *testing.T) {
	streamer := &replacedStreamer{release: make(chan struct{}), tailWritten: make(chan struct{})}
	hub := NewHub(streamer)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		hub.Serve(r.Context(), c, nil)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	sub := clientMsg{Type: "subscribe", SubID: "stats:c1", Channel: "stats", ContainerID: "c1"}
	if err := wsjson.Write(ctx, conn, sub); err != nil {
		t.Fatal(err)
	}
	if got := containerOf(t, ctx, conn); got != "stream-1" {
		t.Fatalf("first frame came from %q", got)
	}

	// Re-subscribe under the same id, then let the old stream emit its last frame.
	if err := wsjson.Write(ctx, conn, sub); err != nil {
		t.Fatal(err)
	}
	if got := containerOf(t, ctx, conn); got != "stream-2" {
		t.Fatalf("after re-subscribing, the frame came from %q", got)
	}
	close(streamer.release)
	<-streamer.tailWritten // the tail frame has now been written, or dropped

	// Anything that arrives before the pong must belong to the live subscription.
	if err := wsjson.Write(ctx, conn, clientMsg{Type: "ping", SubID: "alive"}); err != nil {
		t.Fatal(err)
	}
	readCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	for {
		var msg serverMsg
		if err := wsjson.Read(readCtx, conn, &msg); err != nil {
			t.Fatalf("reading after the replacement: %v", err)
		}
		if msg.Type == "pong" {
			return
		}
		if data, ok := msg.Data.(map[string]any); ok && data["containerId"] == "tail-of-stream-1" {
			t.Fatal("the replaced subscription's tail frame was delivered to its replacement")
		}
	}
}

// containerOf reads one frame and returns the container id it carries.
func containerOf(t *testing.T, ctx context.Context, conn *websocket.Conn) string {
	t.Helper()
	var msg serverMsg
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		t.Fatalf("read: %v", err)
	}
	data, ok := msg.Data.(map[string]any)
	if !ok {
		t.Fatalf("frame carried no data: %+v", msg)
	}
	id, _ := data["containerId"].(string)
	return id
}
