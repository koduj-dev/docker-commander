package monitor

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// A follower that ends on its own — the container stopped, the stream errored —
// must cancel its context, not merely forget it. The context is derived from the
// monitor's long-lived root, so a forgotten one stays attached for the life of
// the process; the entry disappearing from the map looks identical either way,
// which is why this went unnoticed.
func TestRunFollowerCancelsWhenTheStreamEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cleaned := make(chan struct{})
	runFollower(cancel, func() { close(cleaned) }, func() error {
		return nil // the stream ended by itself
	})

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Error("the follower's context was left open — a leak per follower that ends on its own")
	}
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Error("the cleanup did not run")
	}
}

// Cancelling must happen even when the stream fails, which is the common case:
// a container disappears and the stream returns an error.
func TestRunFollowerCancelsWhenTheStreamFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runFollower(cancel, func() {}, func() error { return errors.New("stream died") })

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Error("a failed stream must still cancel its context")
	}
}

// The socket must not be left open when the SMTP greeting fails — a relay that
// answers with an error instead of a greeting would otherwise leak a descriptor
// per alert.
//
// The review reported this as a missing conn.Close(), and #135 added one. It was
// dead code: smtp.NewClient wraps the socket and closes it itself when the
// greeting doesn't arrive, so the close could never be shown to matter (a
// mutation run proved exactly that — removing it changed nothing). The line is
// gone; this test stays, because the behaviour is real and the ownership contract
// is worth pinning: a refactor that dials and then does its own handshake, rather
// than handing the socket to NewClient, would break it and this would say so.
func TestSendMailClosesTheConnectionWhenTheGreetingFails(t *testing.T) {
	ours, theirs := net.Pipe()
	original := tlsDial
	tlsDial = func(string, string, *tls.Config) (net.Conn, error) { return ours, nil }
	t.Cleanup(func() { tlsDial = original })

	// A server that refuses instead of greeting: smtp.NewClient reads this, gives
	// up, and closes the socket itself on the way out (textproto.Conn.Close).
	// That ownership is exactly what this pins — a refactor that dials and then
	// runs its own handshake would own the socket instead, and leak it.
	served := make(chan error, 1)
	go func() {
		if _, err := theirs.Write([]byte("500 go away\r\n")); err != nil {
			served <- err
			return
		}
		// Reading now blocks until the other end closes. That is the assertion:
		// a leaked connection never produces this.
		_, err := io.ReadAll(theirs)
		served <- err
	}()

	err := SendMail(store.SMTPConfig{
		Host: "smtp.example.com", Port: 465, TLS: true,
		From: "a@example.com", To: "b@example.com",
	}, "subject", "body")
	if err == nil {
		t.Fatal("a refusing server should fail the send")
	}

	select {
	case <-served:
	case <-time.After(2 * time.Second):
		t.Error("the TLS connection was left open after the SMTP handshake failed")
	}
}
