package auth

import (
	"sync"
	"time"
)

// LoginLimiter is a small in-memory fixed-window rate limiter keyed by client
// identity (IP or username). It throttles brute-force login attempts without
// any external dependency. Suitable for a single-instance local tool.
type LoginLimiter struct {
	mu       sync.Mutex
	attempts map[string]*attemptWindow
	max      int
	dur      time.Duration
}

type attemptWindow struct {
	count int
	reset time.Time
}

// NewLoginLimiter allows max failed attempts within the given window.
func NewLoginLimiter(max int, window time.Duration) *LoginLimiter {
	return &LoginLimiter{
		attempts: make(map[string]*attemptWindow),
		max:      max,
		dur:      window,
	}
}

// Allow reports whether another attempt is permitted for key right now.
// It does not consume an attempt; call Fail to record a failed attempt.
func (l *LoginLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.attempts[key]
	if w == nil || time.Now().After(w.reset) {
		return true
	}
	return w.count < l.max
}

// Fail records a failed attempt for key, starting a window if needed.
func (l *LoginLimiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.sweep(now)
	w := l.attempts[key]
	if w == nil || now.After(w.reset) {
		l.attempts[key] = &attemptWindow{count: 1, reset: now.Add(l.dur)}
		return
	}
	w.count++
}

// sweep drops windows that have expired.
//
// Entries were only ever removed when the same key came back, so every distinct
// key left one behind for good: a botnet, an IPv6 /64, or simply a long-lived
// instance accumulating client addresses grows this map without bound until the
// process is killed. The same limiter also backs the MCP OAuth throttle, whose
// keys are unauthenticated client IPs.
//
// Called from Fail rather than on a timer: it only grows on failures, so that is
// the one path that needs to pay for the cleanup, and a background goroutine for
// a map that is usually tiny would be the wrong trade.
//
// Caller holds l.mu.
func (l *LoginLimiter) sweep(now time.Time) {
	// Bounded work per call: a full scan is fine while the map is small, and when
	// it is not, this still drains it steadily without stalling a request.
	const maxScan = 128
	scanned := 0
	for k, w := range l.attempts {
		if now.After(w.reset) {
			delete(l.attempts, k)
		}
		if scanned++; scanned >= maxScan {
			return
		}
	}
}

// Reset clears the counter for key after a successful login.
func (l *LoginLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}
