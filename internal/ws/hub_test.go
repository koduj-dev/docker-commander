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
	allow := func(channel string) bool { return channel == "stats" }
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
