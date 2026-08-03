package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// The limiter is the only control here that answers "how much, how fast" rather
// than "is this allowed", so these tests care about the boundary itself: the
// last permitted change, the first refused one, and that refusing a change does
// not also refuse the reads an assistant needs to understand what it just hit.

func fixedClock(t0 time.Time) (*time.Time, func() time.Time) {
	now := t0
	return &now, func() time.Time { return now }
}

func TestControlLimiterBurstBoundary(t *testing.T) {
	l := newControlLimiter()
	_, clock := fixedClock(time.Unix(1_700_000_000, 0))
	l.now = clock

	for i := range controlBurst {
		if ok, _ := l.allow(1); !ok {
			t.Fatalf("change %d of %d refused inside the burst", i+1, controlBurst)
		}
	}
	if ok, _ := l.allow(1); ok {
		t.Fatalf("change %d allowed: the burst is not a ceiling", controlBurst+1)
	}
}

func TestControlLimiterRefills(t *testing.T) {
	l := newControlLimiter()
	now, clock := fixedClock(time.Unix(1_700_000_000, 0))
	l.now = clock

	for range controlBurst {
		l.allow(1)
	}
	if ok, _ := l.allow(1); ok {
		t.Fatal("burst not exhausted; the rest of this test proves nothing")
	}

	// One refill interval buys exactly one change back — not the whole bucket.
	*now = now.Add(controlRefill)
	if ok, _ := l.allow(1); !ok {
		t.Fatal("no change allowed after a full refill interval: the bucket never recovers")
	}
	if ok, _ := l.allow(1); ok {
		t.Fatal("two changes allowed after one refill interval: refill is too generous")
	}
}

func TestControlLimiterDoesNotAccumulateBeyondBurst(t *testing.T) {
	l := newControlLimiter()
	now, clock := fixedClock(time.Unix(1_700_000_000, 0))
	l.now = clock

	l.allow(1) // create the bucket
	// A token idle for a day must not bank a day's worth of changes; otherwise
	// the ceiling is only a ceiling for callers that were already busy.
	*now = now.Add(24 * time.Hour)
	allowed := 0
	for range controlBurst * 3 {
		if ok, _ := l.allow(1); ok {
			allowed++
		}
	}
	if allowed != controlBurst {
		t.Fatalf("allowed %d after a long idle, want the burst cap of %d", allowed, controlBurst)
	}
}

func TestControlLimiterIsPerUser(t *testing.T) {
	l := newControlLimiter()
	_, clock := fixedClock(time.Unix(1_700_000_000, 0))
	l.now = clock

	for range controlBurst {
		l.allow(1)
	}
	if ok, _ := l.allow(1); ok {
		t.Fatal("user 1 not exhausted")
	}
	if ok, _ := l.allow(2); !ok {
		t.Fatal("user 2 refused because user 1 was busy: one noisy identity must not deny everyone else")
	}
}

func TestNilControlLimiterAllows(t *testing.T) {
	var l *controlLimiter
	if ok, _ := l.allow(1); !ok {
		t.Fatal("a nil limiter must allow, so a zero-value handler is not silently throttled")
	}
}

// newHandler is the only construction point precisely so this cannot regress:
// if someone adds a second one that forgets the limiter, MCP would run in
// production with no ceiling and every existing test would still pass.
func TestHandlerIsConstructedWithALimiter(t *testing.T) {
	if newHandler(Deps{}).limiter == nil {
		t.Fatal("newHandler left the limiter nil: MCP would accept unlimited changes")
	}
}

func TestAuthorizeCapsWritesAndLeavesReadsAlone(t *testing.T) {
	h, _ := newTestHandler(t, nil)
	ctx := context.Background()
	p := &principal{user: &store.User{ID: 7, Username: "alice", Role: "admin"}}
	re := reqFor(p).Extra

	for i := range controlBurst {
		if _, err := h.authorizeExtra(ctx, re, "containers", true, 0); err != nil {
			t.Fatalf("write %d refused inside the burst: %v", i+1, err)
		}
	}

	_, err := h.authorizeExtra(ctx, re, "containers", true, 0)
	if err == nil {
		t.Fatal("writes are not capped: a runaway loop would run unbounded")
	}
	// The message has to say the change did NOT happen — a model told only
	// "denied" may record the action as done and move on.
	if !strings.Contains(err.Error(), "rate limited") || !strings.Contains(err.Error(), "NOT performed") {
		t.Fatalf("refusal does not explain itself: %v", err)
	}

	// Reads must survive the cap: this is the moment an assistant most needs to
	// look at what it has already done.
	if _, err := h.authorizeExtra(ctx, re, "containers", false, 0); err != nil {
		t.Fatalf("read refused once writes were capped: %v", err)
	}
}

func TestDeniedWritesDoNotSpendAllowance(t *testing.T) {
	denied := errors.New("forbidden")
	deny := true
	h, _ := newTestHandler(t, func(context.Context, *store.User, string, bool, int64) error {
		if deny {
			return denied
		}
		return nil
	})
	ctx := context.Background()
	p := &principal{user: &store.User{ID: 9, Username: "mallory", Role: "user"}}
	re := reqFor(p).Extra

	// Someone probing what a stolen token can reach collects refusals. Those must
	// not burn the real user's allowance, or the probe becomes a denial of
	// service against them — and the resulting errors would misreport an
	// authorization failure as a rate limit.
	for range controlBurst * 2 {
		if _, err := h.authorizeExtra(ctx, re, "containers", true, 0); !errors.Is(err, denied) {
			t.Fatalf("want the authorization error, got %v", err)
		}
	}

	deny = false
	for i := range controlBurst {
		if _, err := h.authorizeExtra(ctx, re, "containers", true, 0); err != nil {
			t.Fatalf("allowance was spent by denied calls: write %d refused: %v", i+1, err)
		}
	}
}

// Hitting the ceiling has to leave a mark, because the mark is the point: a
// stolen token looks exactly like its owner right up until it starts making
// changes far faster than a person would. But it has to leave ONE mark per
// episode — a loop that keeps hammering must not be able to write the audit
// table full and bury the very entry that identifies it.
func TestRateLimitTripIsAuditedOncePerEpisode(t *testing.T) {
	h, uid := newTestHandler(t, nil)
	now, clock := fixedClock(time.Unix(1_700_000_000, 0))
	h.limiter.now = clock
	ctx := context.Background()
	p := &principal{user: &store.User{ID: uid, Username: "alice", Role: "admin"}, ip: "10.0.0.5"}
	re := reqFor(p).Extra

	trips := func() int {
		entries, err := h.deps.Store.RecentAudit(ctx, 500, 0)
		if err != nil {
			t.Fatalf("read audit: %v", err)
		}
		n := 0
		for _, e := range entries {
			if e.Action == "mcp.ratelimit" {
				n++
			}
		}
		return n
	}

	for range controlBurst {
		if _, err := h.authorizeExtra(ctx, re, "containers", true, 0); err != nil {
			t.Fatalf("refused inside the burst: %v", err)
		}
	}
	if trips() != 0 {
		t.Fatalf("audited a trip while still inside the burst: %d", trips())
	}

	// The runaway: many refusals, one entry.
	for range 50 {
		if _, err := h.authorizeExtra(ctx, re, "containers", true, 0); err == nil {
			t.Fatal("a write got through after the burst was spent")
		}
	}
	if got := trips(); got != 1 {
		t.Fatalf("50 refused changes produced %d audit entries, want exactly 1", got)
	}

	// Recovering and running away again is a NEW episode, and must be recorded.
	*now = now.Add(2 * controlRefill)
	if _, err := h.authorizeExtra(ctx, re, "containers", true, 0); err != nil {
		t.Fatalf("no recovery after refill: %v", err)
	}
	for range 10 {
		_, _ = h.authorizeExtra(ctx, re, "containers", true, 0)
	}
	if got := trips(); got != 2 {
		t.Fatalf("a second runaway produced %d entries in total, want 2", got)
	}
}
