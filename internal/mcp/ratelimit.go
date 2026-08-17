package mcp

import (
	"fmt"
	"sync"
	"time"
)

// A ceiling on how fast one identity can make CHANGES through MCP.
//
// Every other control in this package answers "is this allowed?" — the token's
// narrowing, the user's RBAC, the per-host scope. None of them answers "how
// much, how fast", and that is the question that matters when the caller is a
// model that has gone wrong or a token that has been stolen. Both cases look
// exactly like an authorized user: the calls are permitted, there are just
// suddenly thousands of them. A loop that stops every container on every
// reachable host is made of individually legitimate requests.
//
// So this bounds the blast radius rather than the permission. It is the
// difference between an incident that stops one stack and one that stops an
// estate before anyone reads the alert.
//
// Deliberately WRITES ONLY. Reads are how an assistant works out what is wrong,
// and throttling them would push it toward acting without looking — the exact
// behaviour this file exists to prevent. Reads also change nothing: a stolen
// token reading logs quickly is an exfiltration problem, and a rate limit does
// not solve exfiltration, it only makes it slower while reliably breaking
// legitimate diagnosis. The controls that do address that are the token's
// section narrowing and its expiry.
const (
	// controlBurst is how many changes can happen back-to-back. Sized for the
	// largest sensible human-driven batch — restarting the services of a big
	// stack one by one — so ordinary work never meets it.
	controlBurst = 30
	// controlRefill is the sustained rate once the burst is spent: 30 changes a
	// minute. A person clicking through work stays under it without noticing; a
	// runaway loop is pinned to it, which turns "the estate is down" into "a few
	// containers stopped and the audit log is screaming".
	controlRefill = time.Minute / 30
)

// controlLimiter is a token bucket per acting user.
//
// Keyed by USER, not by token: one user may hold several tokens, and an attacker
// who has taken one of them should not get a fresh allowance by minting another.
// The user is also what the audit log records, so the limit and the evidence
// agree on who "one identity" means.
type controlLimiter struct {
	mu      sync.Mutex
	buckets map[int64]*controlBucket
	burst   float64
	refill  time.Duration
	// now is injectable so the tests can advance time instead of sleeping through
	// a refill window.
	now func() time.Time
}

type controlBucket struct {
	tokens float64
	last   time.Time
	// tripped records that this identity is currently over the limit, so the
	// audit log gets the moment it happened and not one line per rejected call.
	// A runaway loop generates thousands of refusals; writing them all would bury
	// the evidence under itself and hand the attacker a way to flood the table.
	tripped bool
}

func newControlLimiter() *controlLimiter {
	return &controlLimiter{
		buckets: map[int64]*controlBucket{},
		burst:   controlBurst,
		refill:  controlRefill,
		now:     time.Now,
	}
}

// allow consumes one unit for the given user — the n=1 case of reserve. It
// reports whether the change may proceed, and whether this is the FIRST
// refusal of the current episode — the caller audits on that so a
// compromised token leaves exactly one clear mark per time it runs away,
// rather than none or thousands.
//
// A limiter that was never constructed allows everything, so a zero-value
// handler in a test is not silently throttled.
func (l *controlLimiter) allow(userID int64) (ok, firstTrip bool) {
	return l.reserve(userID, 1)
}

// reserve atomically consumes n units for the given user: if fewer than n are
// available after refill, NONE are charged. This is what makes charging for a
// multi-container action safe — a loop that charged one unit at a time would
// leave whatever it had already spent in place even when the call as a whole
// gets refused, silently draining a caller's budget on a batch that never ran.
func (l *controlLimiter) reserve(userID int64, n int) (ok, firstTrip bool) {
	if l == nil {
		return true, false
	}
	if n < 1 {
		n = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, exists := l.buckets[userID]
	if !exists {
		b = &controlBucket{tokens: l.burst, last: now}
		l.buckets[userID] = b
	} else if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens += float64(elapsed) / float64(l.refill)
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}

	if b.tokens < float64(n) {
		first := !b.tripped
		b.tripped = true
		return false, first
	}
	b.tokens -= float64(n)
	// Back under the limit: the next overrun is a new episode worth recording.
	b.tripped = false
	return true, false
}

// errControlRateLimited explains the refusal in terms the model can act on.
// It names the limit and says the call was not performed, because a model told
// only "denied" tends to retry immediately, and one told nothing about whether
// the action happened may assume it did.
func errControlRateLimited() error {
	return fmt.Errorf(
		"rate limited: no more than %d changes per minute may be made through MCP, and this one was NOT performed. "+
			"This limit exists to bound the damage from a runaway loop or a stolen token. "+
			"Wait before retrying, and if a large batch of changes is genuinely intended, make it from the web UI",
		controlBurst)
}
