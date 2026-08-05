package auth

import (
	"strconv"
	"testing"
	"time"
)

func TestUsedChallengesSpendsOnce(t *testing.T) {
	u := newUsedChallenges()
	exp := time.Now().Add(5 * time.Minute)

	if !u.spend("abc", exp) {
		t.Fatal("the first use of a token must be allowed")
	}
	if u.spend("abc", exp) {
		t.Error("SECURITY: the same challenge token was spent twice")
	}
	if !u.spend("other", exp) {
		t.Error("a different token must be unaffected")
	}
}

// A challenge carrying no id is refused rather than sharing one empty slot with
// every other such token. Reachable across an upgrade, by anyone holding a
// challenge issued by the previous build.
func TestUsedChallengesRefusesAnUnidentifiedToken(t *testing.T) {
	u := newUsedChallenges()
	if u.spend("", time.Now().Add(time.Minute)) {
		t.Error("a token with no id must not be spendable")
	}
	// And it must not have consumed anything on the way: a real token still works.
	if !u.spend("real", time.Now().Add(time.Minute)) {
		t.Error("refusing an empty id must not affect other tokens")
	}
}

// Once a token has expired it can never be presented again, so keeping its id
// only grows the map.
func TestUsedChallengesForgetsExpiredIDs(t *testing.T) {
	u := newUsedChallenges()
	past := time.Now().Add(-time.Minute)
	for i := range 200 {
		u.spend("old-"+strconv.Itoa(i), past)
	}
	for i := range 10 {
		u.spend("new-"+strconv.Itoa(i), time.Now().Add(time.Minute))
	}

	u.mu.Lock()
	remaining := len(u.seen)
	u.mu.Unlock()
	if remaining > 30 {
		t.Errorf("expired ids are not being swept: %d left of 210", remaining)
	}
}

// Sweeping must not forget a token that is still live — that would hand a spent
// challenge back for a second attempt.
func TestUsedChallengesKeepsLiveIDs(t *testing.T) {
	u := newUsedChallenges()
	live := time.Now().Add(5 * time.Minute)
	if !u.spend("mine", live) {
		t.Fatal("first use should be allowed")
	}
	for i := range 300 {
		u.spend("noise-"+strconv.Itoa(i), time.Now().Add(-time.Minute))
	}
	if u.spend("mine", live) {
		t.Error("SECURITY: the sweep released a live token for a second attempt")
	}
}
