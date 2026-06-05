package docker

import (
	"context"
	"testing"
)

// composeProbe must return false (not panic/hang) when the binary doesn't
// exist, so the feature degrades gracefully where the CLI is absent.
func TestComposeProbeMissingBinary(t *testing.T) {
	if composeProbe(context.Background(), "dc-no-such-binary-xyz") {
		t.Error("probe of a non-existent binary should be false")
	}
}
