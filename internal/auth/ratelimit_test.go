package auth

import (
	"strconv"
	"testing"
	"time"
)

// The limiter's map used to grow for ever: an entry was only removed when the
// same key came back, so every distinct client address left one behind. A botnet,
// an IPv6 /64, or simply a long-lived instance would grow it until the process
// died — and the same limiter backs the MCP OAuth throttle, whose keys are
// unauthenticated client IPs.
func TestLoginLimiterEvictsExpiredWindows(t *testing.T) {
	l := NewLoginLimiter(5, time.Millisecond)

	for i := range 200 {
		l.Fail("10.0.0." + strconv.Itoa(i))
	}
	time.Sleep(5 * time.Millisecond) // every window is now expired

	// Each subsequent Fail sweeps a bounded slice of the map, so a handful of
	// calls is enough to drain what one burst left behind.
	for i := range 10 {
		l.Fail("cleanup-" + strconv.Itoa(i))
	}

	l.mu.Lock()
	remaining := len(l.attempts)
	l.mu.Unlock()
	if remaining > 20 {
		t.Errorf("expired windows are not being evicted: %d entries left of 200", remaining)
	}
}

// Eviction must not forget a window that is still running — that would hand the
// budget back to an attacker mid-attack.
func TestLoginLimiterKeepsLiveWindows(t *testing.T) {
	l := NewLoginLimiter(3, time.Hour)

	for range 3 {
		l.Fail("attacker")
	}
	if l.Allow("attacker") {
		t.Fatal("three failures should have used the budget")
	}

	// Plenty of unrelated traffic, each call sweeping.
	for i := range 300 {
		l.Fail("noise-" + strconv.Itoa(i))
	}
	if l.Allow("attacker") {
		t.Error("SECURITY: the sweep cleared a live window; the attacker got a fresh budget")
	}
}

func TestLoginLimiterWindowExpiry(t *testing.T) {
	l := NewLoginLimiter(2, 20*time.Millisecond)
	l.Fail("k")
	l.Fail("k")
	if l.Allow("k") {
		t.Fatal("budget should be spent")
	}
	time.Sleep(30 * time.Millisecond)
	if !l.Allow("k") {
		t.Error("the window should have expired")
	}
}
