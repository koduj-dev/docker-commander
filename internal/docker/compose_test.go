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

func TestComposeWarnings(t *testing.T) {
	out := `time="2026-06-08T14:51:55+02:00" level=warning msg="The \"NGINX_TAG\" variable is not set. Defaulting to a blank string."
name: demo
time="..." level=warning msg="The \"HOSTPORT\" variable is not set."
services:
  web: {}`
	ws := ComposeWarnings(out)
	if len(ws) != 2 {
		t.Fatalf("expected 2 warnings, got %d: %v", len(ws), ws)
	}
	if ws[0] != `The "NGINX_TAG" variable is not set. Defaulting to a blank string.` {
		t.Errorf("message not unescaped correctly: %q", ws[0])
	}
	if got := ComposeWarnings("name: demo\nservices: {}"); len(got) != 0 {
		t.Errorf("no warnings expected, got %v", got)
	}
}
