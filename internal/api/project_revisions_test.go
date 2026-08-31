package api

import (
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

func TestBuildDigestPinOverride_OnlyServicesWithADigest(t *testing.T) {
	images := []store.RevisionImage{
		{Service: "web", Image: "nginx:1.25", Digest: "sha256:aaaa"},
		{Service: "cache", Image: "redis:7"}, // no digest recorded — must be left alone
		{Service: "db", Image: "postgres:16@sha256:oldref", Digest: "sha256:bbbb"},
	}
	out := buildDigestPinOverride(images)

	if !strings.Contains(out, "web:") || !strings.Contains(out, "nginx:1.25@sha256:aaaa") {
		t.Errorf("web should be pinned to its recorded digest: %q", out)
	}
	if strings.Contains(out, "cache:") {
		t.Errorf("a service with no recorded digest must not appear in the override: %q", out)
	}
	// The stored Image may itself already carry an old @digest suffix (from a
	// prior pin) — the repo part must be re-extracted, not double-pinned.
	if !strings.Contains(out, "postgres:16@sha256:bbbb") || strings.Contains(out, "oldref") {
		t.Errorf("db should be pinned to postgres:16@sha256:bbbb, not the stale stored digest: %q", out)
	}
}

func TestBuildDigestPinOverride_NoDigestsIsEmpty(t *testing.T) {
	images := []store.RevisionImage{{Service: "web", Image: "nginx:1.25"}}
	if out := buildDigestPinOverride(images); out != "" {
		t.Errorf("expected an empty override when nothing has a recorded digest, got %q", out)
	}
	if out := buildDigestPinOverride(nil); out != "" {
		t.Errorf("expected an empty override for no images at all, got %q", out)
	}
}
