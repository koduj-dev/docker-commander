package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
