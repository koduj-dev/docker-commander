package docker

import (
	"context"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// The preview's job is to answer "what would change", so the tests are about
// what it reports as a change — and, just as much, what it declines to.

func svc(name, image string) ServiceSpec { return ServiceSpec{Name: name, Image: image} }

func TestBuildDeployPreviewClassifiesChanges(t *testing.T) {
	resolved := []ServiceSpec{
		svc("web", "nginx:1.28"), // image changed
		svc("api", "app:2.0"),    // unchanged
		svc("cache", "redis:7"),  // new
	}
	running := []ServiceSpec{
		svc("web", "nginx:1.27"),
		svc("api", "app:2.0"),
		svc("old-worker", "worker:1"), // gone from the file
	}

	p := BuildDeployPreview(resolved, running)

	byService := map[string]ServiceChange{}
	for _, c := range p.Changes {
		byService[c.Service] = c
	}
	if len(p.Changes) != 3 {
		t.Fatalf("got %d changes, want 3: %+v", len(p.Changes), p.Changes)
	}
	if c := byService["web"]; c.Kind != "image" || c.From != "nginx:1.27" || c.To != "nginx:1.28" {
		t.Errorf("web should be an image change with both sides: %+v", c)
	}
	if c := byService["cache"]; c.Kind != "added" {
		t.Errorf("cache should be added: %+v", c)
	}
	if c := byService["old-worker"]; c.Kind != "removed" {
		t.Errorf("old-worker should be removed: %+v", c)
	}
	// The unchanged service must NOT be listed — the point of a diff is what moves.
	if _, listed := byService["api"]; listed {
		t.Error("an unchanged service should be counted, not listed")
	}
	if p.Unchanged != 1 {
		t.Errorf("unchanged = %d, want 1", p.Unchanged)
	}
}

// TestPreviewDoesNotClaimOrphansAreDeleted: compose only removes orphans with
// --remove-orphans, which the app deliberately does not pass. Telling an operator
// a container "will be removed" when it will keep running is worse than saying
// nothing.
func TestPreviewDoesNotClaimOrphansAreDeleted(t *testing.T) {
	p := BuildDeployPreview([]ServiceSpec{svc("web", "nginx:1")}, []ServiceSpec{
		svc("web", "nginx:1"), svc("stray", "old:1"),
	})
	var stray ServiceChange
	for _, c := range p.Changes {
		if c.Service == "stray" {
			stray = c
		}
	}
	if stray.Kind != "removed" {
		t.Fatalf("expected the orphan to be reported: %+v", p.Changes)
	}
	if stray.Detail == "" {
		t.Fatal("the orphan needs an explanation")
	}
	for _, wrong := range []string{"will be removed", "will be deleted", "will be destroyed"} {
		if stray.Detail == wrong {
			t.Errorf("the detail claims deletion, but a deploy leaves orphans running: %q", stray.Detail)
		}
	}
	if !containsSubstr(stray.Detail, "leaves it running") {
		t.Errorf("the detail should say the orphan is left alone: %q", stray.Detail)
	}
}

// TestPreviewIgnoresMissingImages: a service that builds locally has no image in
// the resolved config until it is built. Comparing "" against a running image
// would report a change on every single preview.
func TestPreviewIgnoresMissingImages(t *testing.T) {
	p := BuildDeployPreview([]ServiceSpec{svc("web", "")}, []ServiceSpec{svc("web", "project-web:latest")})
	for _, c := range p.Changes {
		if c.Kind == "image" {
			t.Errorf("a build-only service must not read as an image change: %+v", c)
		}
	}
	if p.Unchanged != 1 {
		t.Errorf("unchanged = %d, want 1", p.Unchanged)
	}
}

func TestParseComposeServicesIsOrdered(t *testing.T) {
	raw := []byte(`{"services":{"zeta":{"image":"z:1"},"alpha":{"image":"a:1"},"mid":{"image":"m:1"}}}`)
	for i := 0; i < 20; i++ {
		got, err := ParseComposeServices(raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 || got[0].Name != "alpha" || got[2].Name != "zeta" {
			// Go randomises map iteration; without an explicit sort the preview
			// would reorder between calls and be unreadable as a diff.
			t.Fatalf("services out of order on run %d: %+v", i, got)
		}
	}
}

func TestRunningServicesDeduplicates(t *testing.T) {
	// A scaled service has several containers; the preview compares services.
	st := &Stack{Containers: []StackContainer{
		{Service: "web", Image: "nginx:1"},
		{Service: "web", Image: "nginx:1"},
		{Service: "db", Image: "pg:16"},
		{Service: "", Image: "unlabelled:1"}, // not compose-managed
	}}
	got := RunningServices(st)
	if len(got) != 2 {
		t.Fatalf("got %d services, want 2 (web, db): %+v", len(got), got)
	}
	if got[0].Name != "db" || got[1].Name != "web" {
		t.Errorf("expected a stable sorted order, got %+v", got)
	}
}

func containsSubstr(s, sub string) bool { return strings.Contains(s, sub) }

// MarkIgnoredChanges must flag exactly the (service, kind) pairs given, and
// leave everything else — including a same-service, different-kind change —
// untouched: ignoring one drift on a service must not silently swallow a
// different drift on that same service.
func TestMarkIgnoredChanges(t *testing.T) {
	changes := []ServiceChange{
		{Service: "web", Kind: "env"},
		{Service: "web", Kind: "restart"},
		{Service: "db", Kind: "env"},
	}
	MarkIgnoredChanges(changes, map[[2]string]bool{{"web", "env"}: true})

	if !changes[0].Ignored {
		t.Error("web:env should be marked ignored")
	}
	if changes[1].Ignored {
		t.Error("web:restart was not ignored and must not be marked")
	}
	if changes[2].Ignored {
		t.Error("db:env was not ignored and must not be marked")
	}
	if got := ActiveChanges(changes); got != 2 {
		t.Errorf("ActiveChanges = %d, want 2 (3 total minus 1 ignored)", got)
	}
}

func TestActiveChanges_AllIgnoredIsZero(t *testing.T) {
	changes := []ServiceChange{{Service: "web", Kind: "env", Ignored: true}}
	if got := ActiveChanges(changes); got != 0 {
		t.Errorf("ActiveChanges = %d, want 0", got)
	}
}

// AugmentDigestDrift must never re-flag a service BuildDeployPreview already
// classified (an image string change already implies recreation — a digest
// check on top would just be noise), and must skip a service with no
// currently-running container to inspect. Both checks are pure decision
// logic that doesn't need a live registry or daemon, unlike digest resolution
// itself (see TestResolveImageDigest_* in digest_test.go and
// TestRunningImageDigest_RealContainer in stacks_test.go).
func TestAugmentDigestDrift_SkipsAlreadyChangedAndUnmatched(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := &Manager{store: st}

	prev := DeployPreview{
		Services: []ServiceSpec{
			svc("web", "nginx:1.25"), // already flagged below — must be skipped
			svc("cache", "redis:7"),  // unchanged, but no running container — must be skipped
		},
		Changes: []ServiceChange{
			{Service: "web", Kind: "image", From: "nginx:1.24", To: "nginx:1.25"},
		},
		Unchanged: 1,
	}
	before := len(prev.Changes)

	m.AugmentDigestDrift(context.Background(), 0, &prev, []StackContainer{
		{Service: "web", ID: "c1"}, // "cache" deliberately has no matching container
	})

	if len(prev.Changes) != before {
		t.Errorf("should not add a digest change for an already-changed or unmatched service: %+v", prev.Changes)
	}
	if prev.Unchanged != 1 {
		t.Errorf("Unchanged should be untouched when nothing new is found, got %d", prev.Unchanged)
	}
}
