package auth

import (
	"sync"
	"time"
)

// usedChallenges remembers MFA-challenge tokens that have already been spent, so
// each one buys exactly one attempt.
//
// Why one attempt and not five: the rate limiter bounds guesses per window, but
// a challenge token is handed out on a correct password and then accepted
// repeatedly for five minutes. That let one password entry fund a burst of code
// guesses. Burning the token on *any* attempt — right or wrong — means a wrong
// code costs another password round trip, which is the expensive half.
//
// Held in memory rather than the database: these live five minutes, a restart
// invalidates every session anyway (the epoch check reloads from the store, but
// the challenge itself is only useful mid-login), and a token that survives a
// restart within its own window is a far smaller thing than a row per login.
type usedChallenges struct {
	mu   sync.Mutex
	seen map[string]time.Time // token id → when it expires
}

func newUsedChallenges() *usedChallenges {
	return &usedChallenges{seen: make(map[string]time.Time)}
}

// spend marks a challenge id used and reports whether it was still unspent.
//
// A token with no id is refused outright. It fails closed either way — an empty
// key would simply be spent by the first caller and refuse everyone after — but
// refusing it explicitly makes the case deterministic instead of "whoever got
// there first". It is reachable in one real situation: a challenge issued by the
// previous build, held by someone mid-login across an upgrade. They see "invalid
// credentials" and log in again, which is the correct outcome and not a silent
// one.
func (u *usedChallenges) spend(id string, expiry time.Time) bool {
	if id == "" {
		return false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	now := time.Now()
	u.sweep(now)
	if exp, ok := u.seen[id]; ok && now.Before(exp) {
		return false
	}
	u.seen[id] = expiry
	return true
}

// sweep drops entries whose tokens have expired; they can never be presented
// again, so keeping them only grows the map. Bounded per call for the same
// reason the rate limiter's sweep is. Caller holds u.mu.
func (u *usedChallenges) sweep(now time.Time) {
	const maxScan = 128
	scanned := 0
	for id, exp := range u.seen {
		if now.After(exp) {
			delete(u.seen, id)
		}
		if scanned++; scanned >= maxScan {
			return
		}
	}
}
