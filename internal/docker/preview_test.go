package docker

import (
	"strings"
	"testing"
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
