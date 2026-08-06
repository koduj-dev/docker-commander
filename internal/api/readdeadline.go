package api

import (
	"io"
	"log"
	"net/http"
	"time"
)

// Bounding how long a client may take to deliver a request body.
//
// The server sets a ReadTimeout, so an ordinary request cannot be dribbled out a
// byte at a time to hold a handler open — that is a resource the client should not
// get to reserve for free, and it is what let a stalled WebAuthn registration
// straddle a change in the account's protection.
//
// A few routes legitimately take minutes: loading a multi-gigabyte image, sending a
// build context, uploading a file into a container. A whole-request deadline is the
// wrong shape for those, so they swap it for a rolling one. The limit becomes "this
// upload went quiet" rather than "this upload took a while", which is the thing
// actually worth refusing.

// streamIdle is how long a streaming body may deliver nothing before the
// connection is given up on. Generous: a slow link moving real data resets it on
// every chunk, so this only measures silence.
//
// A var rather than a const so a test can shorten it. Pinning "the deadline moves
// forward" otherwise needs a test that runs longer than this does, and a test that
// slow is a test that gets deleted.
var streamIdle = 2 * time.Minute

// streamingBody swaps the server's whole-request read deadline for a rolling one,
// and returns the body to read from.
//
// Handlers that stream MUST use it. Without it the server's ReadTimeout applies to
// the whole transfer and a large upload dies partway through — which is the loud
// failure, and the reason the default is the strict one: a route that forgets this
// breaks visibly in testing, where a route that had to opt IN to a deadline would
// silently keep the hole.
func streamingBody(w http.ResponseWriter, r *http.Request) io.ReadCloser {
	// Nothing to wait for, so nothing to arm. net/http has already disarmed the
	// deadline for a bodyless request and started its background read; arming one
	// here would time that read out and cancel the request — for a handler that may
	// never read the body at all, and so never reach the clear below.
	if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		if r.Body == nil {
			return http.NoBody
		}
		return r.Body
	}
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Now().Add(streamIdle)); err != nil {
		// No deadline support under this ResponseWriter (an httptest recorder, or a
		// wrapper that does not Unwrap). The server's own ReadTimeout still applies,
		// so this errs towards refusing a slow upload rather than allowing a stalled
		// one — but that means large uploads start failing at 60 seconds with
		// nothing to explain why. Unreachable in production today; said out loud so
		// that the middleware change which makes it reachable is not silent.
		// %q, not %s: the path is decoded, so a route parameter can carry a newline
		// and forge a log line.
		log.Printf("streaming upload on %q cannot set a read deadline (%v); "+
			"large uploads will be bounded by the server's ReadTimeout", r.URL.Path, err)
		return r.Body
	}
	return &idleBody{body: r.Body, rc: rc}
}

// idleBody pushes the read deadline forward as data arrives.
type idleBody struct {
	body io.ReadCloser
	rc   *http.ResponseController
}

// Read extends the deadline AFTER a successful read, arming the next one — the
// deadline has to already be in place before a read blocks, which is what
// streamingBody sets up.
//
// The deadline is CLEARED the moment the body ends, and that is not tidiness. When
// a handler finishes reading, net/http clears the read deadline itself and starts a
// background read on the connection to notice the client going away
// (server.go, startBackgroundRead). If a deadline is left armed, that background
// read times out, and net/http reads any background-read failure as "the client is
// gone" — it cancels the request context (handleReadErrorLocked).
//
// For these routes that is catastrophic rather than cosmetic: the work happens
// AFTER the body arrives. A docker build whose context uploaded in seconds and then
// compiles for three minutes would die with "context canceled", and so would an
// image load, and so would a file copy into a container. With Content-Length the
// final read returns bytes AND io.EOF together, so this has to be checked before
// the extension, not after.
func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	switch {
	case err == io.EOF:
		_ = b.rc.SetReadDeadline(time.Time{})
	case err != nil:
		// Any OTHER failure leaves the deadline exactly where it is, and that is
		// deliberate. When a handler returns without having drained the body,
		// net/http drains the remainder itself — inline, inside
		// chunkWriter.writeHeader, BEFORE the response headers go out. Clearing the
		// deadline here would leave that drain unbounded: a client that declares a
		// body, sends two bytes and then goes quiet without closing would pin a
		// connection goroutine and its file descriptor for the life of the process,
		// and never receive the timeout the handler wrote. That is the very
		// resource reservation this file exists to refuse, made permanent.
	case n > 0:
		_ = b.rc.SetReadDeadline(time.Now().Add(streamIdle))
	}
	return n, err
}

func (b *idleBody) Close() error { return b.body.Close() }
