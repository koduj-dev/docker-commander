package api

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The read deadline has two jobs that pull in opposite directions: refuse a client
// that holds a handler open by sending nothing, and leave alone an upload that
// legitimately takes minutes. These tests pin both, because a change that satisfies
// only one of them looks correct from wherever you happen to be standing.

// deadlineServer runs one handler under the same timeouts main.go uses, with the
// clock scaled down so the test does not take a minute.
func deadlineServer(t *testing.T, readTimeout time.Duration, h http.HandlerFunc) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       readTimeout,
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// sendSlowBody writes a body one byte at a time with a gap between bytes, on a raw
// connection, and returns what the server said (or the error it gave up with).
func sendSlowBody(t *testing.T, addr string, total int, gap time.Duration) (*http.Response, error) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	head := fmt.Sprintf("POST /upload HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", total)
	if _, err := conn.Write([]byte(head)); err != nil {
		t.Fatal(err)
	}
	go func() {
		for i := 0; i < total; i++ {
			time.Sleep(gap)
			if _, err := conn.Write([]byte("x")); err != nil {
				return
			}
		}
	}()
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	return http.ReadResponse(bufio.NewReader(conn), nil)
}

// A handler that does NOT opt into streaming is bounded by the whole-request
// deadline: a body delivered slowly enough is refused, however long it would
// eventually have taken.
func TestSlowBodyIsRefusedOnAnOrdinaryRoute(t *testing.T) {
	addr := deadlineServer(t, 500*time.Millisecond, func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			w.WriteHeader(http.StatusRequestTimeout)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	resp, err := sendSlowBody(t, addr, 20, 100*time.Millisecond) // 2s of dribbling
	if err != nil {
		return // the server hung up mid-request, which is the refusal
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Errorf("SECURITY: a body dribbled out past the read timeout was accepted (%d)", resp.StatusCode)
	}
}

// …and a handler that DOES opt in is not, as long as the data keeps coming. This
// is the half that a blanket timeout gets wrong: the same traffic pattern, over a
// route where taking a while is the point.
func TestSlowButProgressingUploadSurvives(t *testing.T) {
	addr := deadlineServer(t, 500*time.Millisecond, func(w http.ResponseWriter, r *http.Request) {
		body := streamingBody(w, r)
		defer body.Close()
		n, err := io.Copy(io.Discard, body)
		if err != nil {
			w.WriteHeader(http.StatusRequestTimeout)
			return
		}
		fmt.Fprintf(w, "read %d", n)
	})

	// The idle window is shortened so this can distinguish a deadline that MOVES
	// from one merely set once and generously. The upload runs for 2s — ten times
	// the window — while never going quiet for longer than half of it.
	restore := streamIdle
	streamIdle = 200 * time.Millisecond
	t.Cleanup(func() { streamIdle = restore })

	resp, err := sendSlowBody(t, addr, 20, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("a progressing upload was cut off: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a progressing upload returned %d: %s", resp.StatusCode, got)
	}
	if !strings.Contains(string(got), "read 20") {
		t.Errorf("the whole body should have arrived, got %q", got)
	}
}

// streamingBody must not pretend to have set a deadline it could not set. Under a
// ResponseWriter with no deadline support it hands back the body unchanged, so the
// server's own timeout still governs — refusing a slow upload rather than
// silently allowing a stalled one.
func TestStreamingBodyFallsBackWhenDeadlinesAreUnsupported(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/upload", strings.NewReader("hello"))
	body := streamingBody(rec, r)
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("body = %q, want hello", got)
	}
}

// The work these routes exist for happens AFTER the body arrives: a build compiles,
// an image loads, a file is copied into a container. So the deadline must be gone by
// then — net/http starts a background read on the connection once the handler
// finishes reading, and any failure of that read is taken as "the client is gone"
// and cancels the request context.
//
// Left armed, this turned every long build into "context canceled" a couple of
// minutes after its upload finished. Nothing else in this file covers the window
// after the last byte, which is exactly where that hid.
func TestRequestContextSurvivesTheBodyEnding(t *testing.T) {
	restore := streamIdle
	streamIdle = 300 * time.Millisecond
	t.Cleanup(func() { streamIdle = restore })

	ctxErr := make(chan error, 1)
	addr := deadlineServer(t, 500*time.Millisecond, func(w http.ResponseWriter, r *http.Request) {
		body := streamingBody(w, r)
		defer body.Close()
		if _, err := io.Copy(io.Discard, body); err != nil {
			ctxErr <- err
			return
		}
		// Stand in for the build. Well past the idle window, with nothing more to
		// read — which is the whole point: silence here is not a stalled client.
		time.Sleep(4 * streamIdle)
		ctxErr <- r.Context().Err()
		w.WriteHeader(http.StatusOK)
	})

	if resp, err := sendSlowBody(t, addr, 3, 50*time.Millisecond); err == nil {
		resp.Body.Close()
	}
	if err := <-ctxErr; err != nil {
		t.Fatalf("the request was cancelled after its body finished: %v", err)
	}
}

// A stalled upload has to be answered and let go of, not merely refused.
//
// When a handler returns without draining the body, net/http drains the remainder
// itself — inline, before the response headers go out. Clearing the read deadline
// when the body FAILS leaves that drain unbounded, so a client that declares a
// body, sends two bytes and goes quiet without closing pins a connection goroutine
// and its descriptor forever, and never sees the timeout the handler wrote. That is
// worse than the hole this file exists to close: unbounded rather than 60 seconds.
//
// Keep-alive matters here: net/http skips the drain entirely when the response will
// close the connection, so a "Connection: close" version of this test passes
// against the broken code and proves nothing.
func TestStalledUploadIsAnsweredAndReleased(t *testing.T) {
	restore := streamIdle
	streamIdle = 200 * time.Millisecond
	t.Cleanup(func() { streamIdle = restore })

	addr := deadlineServer(t, 300*time.Millisecond, func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(streamingBody(w, r)); err != nil {
			writeErr(w, http.StatusRequestTimeout, "stalled")
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// A body that is promised, barely started, and then abandoned without closing.
	head := "POST /upload HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\n"
	if _, err := conn.Write([]byte(head + "xy")); err != nil {
		t.Fatal(err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("the connection was never answered or released: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestTimeout {
		t.Errorf("a stalled upload returned %d, want 408", resp.StatusCode)
	}
}

// A WebSocket must not be on the read-timeout clock.
//
// ReadTimeout arms a deadline on the connection for the whole request, and a
// WebSocket is a request that never ends — so if the deadline survived the upgrade,
// every stream in the app would die a minute in. It does not: net/http clears
// deadlines when a handler hijacks the connection (server.go, hijackLocked).
//
// Honest about what this proves: it passes trivially today, and it cannot be
// mutation-tested from here — setting a deadline before the upgrade is cleared by
// the same hijack, so there is no way to make it fail on purpose without editing
// the standard library. It is a canary for that stdlib behaviour changing, not a
// guard over code in this repo. Worth keeping anyway: a silently dropped stream is
// a failure this project has already shipped once, in #145.
func TestWebSocketOutlivesTheReadTimeout(t *testing.T) {
	const readTimeout = 300 * time.Millisecond

	addr := deadlineServer(t, readTimeout, func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		// Sit well past the deadline, then speak. If the upgrade inherited it, the
		// connection is already gone by now.
		time.Sleep(3 * readTimeout)
		if err := c.Write(r.Context(), websocket.MessageText, []byte("still here")); err != nil {
			return
		}
		<-r.Context().Done()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws://"+addr+"/stream", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	typ, msg, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("the stream died after the read timeout: %v", err)
	}
	if typ != websocket.MessageText || string(msg) != "still here" {
		t.Errorf("got %v %q", typ, msg)
	}
}

// A request with no body must be left alone.
//
// net/http disarms the read deadline and starts its background read BEFORE such a
// handler runs, so arming one here times that read out and cancels the request —
// for a handler that may never read the body at all, and so never reach the clear.
// Reproduced as `context canceled` mid-handler on a Content-Length: 0 POST.
func TestBodylessRequestIsLeftAlone(t *testing.T) {
	restore := streamIdle
	streamIdle = 200 * time.Millisecond
	t.Cleanup(func() { streamIdle = restore })

	ctxErr := make(chan error, 1)
	addr := deadlineServer(t, 500*time.Millisecond, func(w http.ResponseWriter, r *http.Request) {
		// Asks for the rolling deadline and then never reads — the shape of a
		// handler that hands the body to a library which returns early.
		_ = streamingBody(w, r)
		time.Sleep(3 * streamIdle)
		ctxErr <- r.Context().Err()
		w.WriteHeader(http.StatusOK)
	})

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("POST /x HTTP/1.1\r\nHost: x\r\nContent-Length: 0\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := <-ctxErr; err != nil {
		t.Fatalf("a bodyless request was cancelled: %v", err)
	}
}
