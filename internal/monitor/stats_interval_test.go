package monitor

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/crypto"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// TestSetStatsInterval checks the configurable sweep interval: New defaults to
// 15s, a positive override is applied, and a non-positive one is ignored.
func TestSetStatsInterval(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	c, _ := crypto.New(key)
	st.SetCipher(c)

	m := New(st, docker.NewManager(st), nil)
	if m.statsInterval != defaultStatsInterval {
		t.Fatalf("default interval = %v, want %v", m.statsInterval, defaultStatsInterval)
	}

	m.SetStatsInterval(45 * time.Second)
	if m.statsInterval != 45*time.Second {
		t.Errorf("override not applied: %v", m.statsInterval)
	}

	// Non-positive values are ignored — the previous interval stands.
	m.SetStatsInterval(0)
	m.SetStatsInterval(-1 * time.Second)
	if m.statsInterval != 45*time.Second {
		t.Errorf("non-positive override should be ignored, got %v", m.statsInterval)
	}
}
