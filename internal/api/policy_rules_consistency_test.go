package api

import (
	"sort"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// internal/store can't import internal/docker (internal/docker already
// imports internal/store, so the reverse would cycle), so the policy rule id
// list is duplicated as plain strings in both packages. This test is the
// guard that keeps them from silently drifting apart.
func TestPolicyRuleIDsMatchBetweenStoreAndDocker(t *testing.T) {
	var fromDocker []string
	for _, r := range docker.AllPolicyRules {
		fromDocker = append(fromDocker, string(r))
	}
	fromStore := append([]string(nil), store.PolicyRuleIDs...)

	sort.Strings(fromDocker)
	sort.Strings(fromStore)

	if len(fromDocker) != len(fromStore) {
		t.Fatalf("docker.AllPolicyRules has %d rules, store.PolicyRuleIDs has %d: %v vs %v", len(fromDocker), len(fromStore), fromDocker, fromStore)
	}
	for i := range fromDocker {
		if fromDocker[i] != fromStore[i] {
			t.Errorf("mismatch at %d: docker has %q, store has %q (full sets: %v vs %v)", i, fromDocker[i], fromStore[i], fromDocker, fromStore)
		}
	}
}
