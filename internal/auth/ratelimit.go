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
	w := l.attempts[key]
	if w == nil || now.After(w.reset) {
		l.attempts[key] = &attemptWindow{count: 1, reset: now.Add(l.dur)}
		return
	}
	w.count++
}

// Reset clears the counter for key after a successful login.
func (l *LoginLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}
