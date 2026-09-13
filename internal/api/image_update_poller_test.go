package api

import (
	"reflect"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/docker"
)

// imageUpdatesToNotify is the only decision in the poller worth unit
// testing in isolation: everything around it (real containers, a real
// registry) needs a live Docker daemon, but the dedup logic — notify once
// per newly-observed digest, never again for the same one, again if it
// changes — is pure and deserves its own mutation-provable coverage.
func TestImageUpdatesToNotify(t *testing.T) {
	digest := func(service, to string) docker.ServiceChange {
		return docker.ServiceChange{Service: service, Kind: "digest", To: to}
	}

	t.Run("a brand-new digest drift is notified", func(t *testing.T) {
		changes := []docker.ServiceChange{digest("web", "sha256:new")}
		got := imageUpdatesToNotify(changes, map[string]string{})
		if len(got) != 1 || got[0].Service != "web" {
			t.Errorf("got %+v, want the web digest change", got)
		}
	})

	t.Run("the same digest already notified is not renotified", func(t *testing.T) {
		changes := []docker.ServiceChange{digest("web", "sha256:new")}
		got := imageUpdatesToNotify(changes, map[string]string{"web": "sha256:new"})
		if len(got) != 0 {
			t.Errorf("expected no notification for an already-notified digest, got %+v", got)
		}
	})

	t.Run("a digest that moved again since the last notification is notified", func(t *testing.T) {
		changes := []docker.ServiceChange{digest("web", "sha256:newer")}
		got := imageUpdatesToNotify(changes, map[string]string{"web": "sha256:old-notified"})
		if len(got) != 1 || got[0].To != "sha256:newer" {
			t.Errorf("got %+v, want a fresh notification for the newer digest", got)
		}
	})

	t.Run("non-digest changes (image/env/ports/...) are never notified", func(t *testing.T) {
		changes := []docker.ServiceChange{
			{Service: "web", Kind: "image", To: "app:2.0"},
			{Service: "web", Kind: "env", To: "changed"},
		}
		got := imageUpdatesToNotify(changes, map[string]string{})
		if len(got) != 0 {
			t.Errorf("only digest-kind changes should ever notify, got %+v", got)
		}
	})

	t.Run("multiple services are handled independently", func(t *testing.T) {
		changes := []docker.ServiceChange{digest("web", "sha256:a"), digest("worker", "sha256:b")}
		got := imageUpdatesToNotify(changes, map[string]string{"web": "sha256:a"}) // web already notified, worker is not
		want := []docker.ServiceChange{digest("worker", "sha256:b")}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
}
