package docker

import (
	"testing"
	"time"
)

// This is a trimmed but faithful copy of real `docker compose config
// --format json` output (Compose v5.4.0) for a service declaring env, a
// published port, a named volume and a bind mount, a custom network, a
// resource limit and a healthcheck — captured against a live daemon rather
// than guessed, since compose's JSON shape (memory already converted to a
// byte count, network names needing the top-level networks section to
// resolve to their real Docker name, ports.published as a string) is exactly
// the kind of detail worth getting from the real CLI, not memory.
const realComposeConfigJSON = `{
  "name": "dc-compose-sample",
  "networks": {
    "front": { "name": "dc-compose-sample_front", "ipam": {} }
  },
  "services": {
    "web": {
      "deploy": {
        "resources": { "limits": { "cpus": 0.5, "memory": "268435456" }, "placement": {} }
      },
      "environment": { "BAZ": "qux", "FOO": "bar" },
      "healthcheck": {
        "test": ["CMD", "curl", "-f", "http://localhost"],
        "timeout": "5s", "interval": "30s", "retries": 3
      },
      "image": "nginx:1.25",
      "networks": { "front": null },
      "ports": [{ "mode": "ingress", "target": 80, "published": "8080", "protocol": "tcp" }],
      "restart": "unless-stopped",
      "volumes": [
        { "type": "volume", "source": "webdata", "target": "/usr/share/nginx/html", "volume": {} },
        { "type": "bind", "source": "/tmp/dc-compose-sample/conf", "target": "/etc/nginx/conf.d", "bind": {} }
      ]
    }
  },
  "volumes": { "webdata": { "name": "dc-compose-sample_webdata" } }
}`

func TestParseComposeServices_FullFieldExtraction(t *testing.T) {
	out, err := ParseComposeServices([]byte(realComposeConfigJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d services, want 1", len(out))
	}
	s := out[0]

	if !s.Detailed {
		t.Error("a compose-parsed spec should be Detailed")
	}
	if s.Env["FOO"] != "bar" || s.Env["BAZ"] != "qux" || len(s.Env) != 2 {
		t.Errorf("Env = %+v, want {FOO:bar BAZ:qux}", s.Env)
	}
	if len(s.Ports) != 1 || s.Ports[0] != (ServicePort{Target: 80, Published: "8080", Protocol: "tcp"}) {
		t.Errorf("Ports = %+v", s.Ports)
	}
	if len(s.Volumes) != 2 {
		t.Fatalf("got %d volumes, want 2: %+v", len(s.Volumes), s.Volumes)
	}
	// Sorted by target: /etc/nginx/conf.d before /usr/share/nginx/html.
	if s.Volumes[0] != (VolumeSpec{Type: "bind", Source: "/tmp/dc-compose-sample/conf", Target: "/etc/nginx/conf.d"}) {
		t.Errorf("Volumes[0] = %+v", s.Volumes[0])
	}
	if s.Volumes[1] != (VolumeSpec{Type: "volume", Source: "webdata", Target: "/usr/share/nginx/html"}) {
		t.Errorf("Volumes[1] = %+v", s.Volumes[1])
	}
	// The service declares network "front", but a running container's real
	// Docker network is "dc-compose-sample_front" — the top-level networks
	// section is what resolves that, and comparisons must use the real name.
	if len(s.Networks) != 1 || s.Networks[0] != "dc-compose-sample_front" {
		t.Errorf("Networks = %+v, want [dc-compose-sample_front]", s.Networks)
	}
	if s.Restart != "unless-stopped" {
		t.Errorf("Restart = %q", s.Restart)
	}
	if s.CPULimit != 0.5 {
		t.Errorf("CPULimit = %v, want 0.5", s.CPULimit)
	}
	if s.MemoryLimit != 268435456 {
		t.Errorf("MemoryLimit = %v, want 268435456", s.MemoryLimit)
	}
	if s.Healthcheck == nil {
		t.Fatal("Healthcheck should be populated")
	}
	if s.Healthcheck.Interval != 30*time.Second || s.Healthcheck.Timeout != 5*time.Second || s.Healthcheck.Retries != 3 {
		t.Errorf("Healthcheck = %+v", s.Healthcheck)
	}
}

func fullSvc(name, image string) ServiceSpec {
	return ServiceSpec{Name: name, Image: image, Detailed: true}
}

// ExtendServiceComparison must never flag a field the compose file is silent
// on, even when the running container has one — see the function's own doc
// comment on why (no stored record of what compose has ever managed).
func TestExtendServiceComparison_SilentFieldsNeverFlagged(t *testing.T) {
	resolved := []ServiceSpec{fullSvc("web", "nginx:1")} // no env/ports/volumes/networks/restart/resources/healthcheck declared
	running := []ServiceSpec{{
		Name: "web", Image: "nginx:1", Detailed: true,
		Env:         map[string]string{"SOME": "thing"},
		Ports:       []ServicePort{{Target: 80, Published: "8080", Protocol: "tcp"}},
		Restart:     "always",
		CPULimit:    1,
		MemoryLimit: 512 << 20,
	}}
	prev := DeployPreview{Services: resolved, Running: running, Changes: []ServiceChange{}, Unchanged: 1}
	ExtendServiceComparison(&prev, resolved, running)
	if len(prev.Changes) != 0 {
		t.Errorf("compose declared nothing on any of these fields; should flag nothing, got %+v", prev.Changes)
	}
	if prev.Unchanged != 1 {
		t.Errorf("Unchanged = %d, want 1", prev.Unchanged)
	}
}

func TestExtendServiceComparison_FlagsDeclaredMismatches(t *testing.T) {
	resolved := []ServiceSpec{{
		Name: "web", Image: "nginx:1", Detailed: true,
		Env:         map[string]string{"FOO": "bar"},
		Ports:       []ServicePort{{Target: 80, Published: "8080", Protocol: "tcp"}},
		Volumes:     []VolumeSpec{{Type: "volume", Source: "data", Target: "/data"}},
		Networks:    []string{"proj_front"},
		Restart:     "unless-stopped",
		CPULimit:    0.5,
		MemoryLimit: 256 << 20,
		Healthcheck: &HealthcheckSpec{Test: []string{"CMD", "true"}, Interval: 30 * time.Second, Retries: 3},
	}}
	running := []ServiceSpec{{
		Name: "web", Image: "nginx:1", Detailed: true,
		Env:         map[string]string{"FOO": "old"}, // changed
		Ports:       []ServicePort{{Target: 81, Published: "8081", Protocol: "tcp"}},
		Volumes:     []VolumeSpec{{Type: "volume", Source: "olddata", Target: "/olddata"}},
		Networks:    []string{"proj_back"},
		Restart:     "always",
		CPULimit:    0.25,
		MemoryLimit: 128 << 20,
		Healthcheck: &HealthcheckSpec{Test: []string{"CMD", "true"}, Interval: 15 * time.Second, Retries: 3},
	}}
	prev := DeployPreview{Services: resolved, Running: running, Changes: []ServiceChange{}, Unchanged: 1}
	ExtendServiceComparison(&prev, resolved, running)

	byKind := map[string]ServiceChange{}
	for _, c := range prev.Changes {
		byKind[c.Kind] = c
	}
	for _, kind := range []string{"env", "ports", "volumes", "networks", "restart", "resources", "healthcheck"} {
		c, ok := byKind[kind]
		if !ok {
			t.Errorf("expected a %q change, got changes: %+v", kind, prev.Changes)
			continue
		}
		if !c.Recreates {
			t.Errorf("%q change should flag Recreates (downtime risk): %+v", kind, c)
		}
	}
	if prev.Unchanged != 0 {
		t.Errorf("Unchanged = %d, want 0 (the one service had changes)", prev.Unchanged)
	}
	// envDiff must never leak the value, only the key name.
	if env := byKind["env"]; env.Detail == "" || containsSubstr(env.Detail, "old") || containsSubstr(env.Detail, "bar") {
		t.Errorf("env change detail must name the key, never the value: %q", env.Detail)
	}
}

// A container with NO resource limit at all is a real, known state (Docker's
// own default), unlike a compose file silent on the field — so a compose
// file that now wants a limit must be flagged even though the running side
// reads zero. Found via manually driving the UI: an early version of
// resourcesDiff required BOTH sides nonzero and silently missed exactly this.
func TestExtendServiceComparison_FlagsNewlyAddedResourceLimit(t *testing.T) {
	resolved := []ServiceSpec{{Name: "web", Image: "nginx:1", Detailed: true, CPULimit: 0.5, MemoryLimit: 256 << 20}}
	running := []ServiceSpec{{Name: "web", Image: "nginx:1", Detailed: true}} // no limit ever set
	prev := DeployPreview{Services: resolved, Running: running, Changes: []ServiceChange{}, Unchanged: 1}
	ExtendServiceComparison(&prev, resolved, running)
	if len(prev.Changes) != 1 || prev.Changes[0].Kind != "resources" {
		t.Fatalf("expected one resources change, got %+v", prev.Changes)
	}
	if !containsSubstr(prev.Changes[0].Detail, "none") {
		t.Errorf("detail should show the running side as having no limit: %q", prev.Changes[0].Detail)
	}
}

// A service BuildDeployPreview already flagged (e.g. an image change) must
// not get a redundant field-level diff piled on top.
func TestExtendServiceComparison_SkipsAlreadyFlaggedService(t *testing.T) {
	resolved := []ServiceSpec{{Name: "web", Image: "nginx:2", Detailed: true, Restart: "always"}}
	running := []ServiceSpec{{Name: "web", Image: "nginx:1", Detailed: true, Restart: "no"}}
	prev := DeployPreview{
		Services: resolved, Running: running,
		Changes:   []ServiceChange{{Service: "web", Kind: "image", From: "nginx:1", To: "nginx:2"}},
		Unchanged: 0,
	}
	ExtendServiceComparison(&prev, resolved, running)
	if len(prev.Changes) != 1 {
		t.Errorf("should not add to an already-flagged service, got %+v", prev.Changes)
	}
}
