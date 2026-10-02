package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// The snapshot source is read-only at the connection level, not just by
// convention: a write through it must fail. Nothing in the backup path writes
// today, and this keeps it that way.
func TestSnapshotSourceRefusesWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "docker-commander.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	src, err := OpenSnapshotSource(path)
	if err != nil {
		t.Fatalf("open snapshot source: %v", err)
	}
	defer src.Close()
	_, err = src.CreateUser(context.Background(), &User{Username: "intruder", Role: "admin", PasswordHash: "h"})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Fatalf("a write through the snapshot source was not refused as read-only: %v", err)
	}
}
