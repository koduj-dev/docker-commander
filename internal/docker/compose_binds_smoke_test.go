package docker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The remote-bind rewrite rests on three behaviours of the real compose CLI:
// service volumes merge by container target (so an override replaces a bind
// rather than adding a second mount), `external: true` keeps the volume name
// unprefixed (so it matches the volume we seeded), and `volume.subpath` mounts a
// single file out of a volume. This test drives the actual CLI to prove all
// three, so a change in compose's merge semantics fails here instead of silently
// deploying a project with its bind mounts still pointing at local paths.
func TestComposeBindOverride_AgainstRealCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("needs the docker compose CLI; skipped under -short")
	}
	ctx := context.Background()
	if !composeProbe(ctx, "docker") {
		t.Skip("docker compose CLI not available")
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "html"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nginx.conf"), []byte("worker_processes 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compose := `services:
  web:
    image: nginx:alpine
    volumes:
      - ./html:/usr/share/nginx/html:ro
      - ./nginx.conf:/etc/nginx/nginx.conf:ro
      - keep:/var/cache/nginx
volumes:
  keep:
`
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	const slug = "smoke-binds"
	cfgJSON, err := ComposeConfigJSON(ctx, dir, slug)
	if err != nil {
		t.Fatalf("resolve compose config: %v", err)
	}
	internal, external, err := ClassifyProjectBinds(cfgJSON, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(external) != 0 {
		t.Fatalf("nothing here points outside the project: %+v", external)
	}
	if len(internal) != 2 {
		t.Fatalf("expected the two project binds, got %+v", internal)
	}

	ov, err := BindOverrideJSON(slug, internal)
	if err != nil {
		t.Fatal(err)
	}
	ovPath := filepath.Join(t.TempDir(), "override.json")
	if err := os.WriteFile(ovPath, ov, 0o600); err != nil {
		t.Fatal(err)
	}

	// Resolve again with the override layered on top, exactly as a deploy does.
	out, err := runComposeFiles(ctx, dir, slug, nil,
		[]string{"compose.yml", ovPath}, "config", "--format", "json")
	if err != nil {
		t.Fatalf("compose config with override failed: %v\n%s", err, out)
	}
	var cfg struct {
		Services map[string]struct {
			Volumes []struct {
				Type     string `json:"type"`
				Source   string `json:"source"`
				Target   string `json:"target"`
				ReadOnly bool   `json:"read_only"`
				Volume   *struct {
					Subpath string `json:"subpath"`
				} `json:"volume"`
			} `json:"volumes"`
		} `json:"services"`
		Volumes map[string]struct {
			Name     string `json:"name"`
			External bool   `json:"external"`
		} `json:"volumes"`
	}
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("parse resolved config: %v\n%s", err, out)
	}

	mounts := cfg.Services["web"].Volumes
	if len(mounts) != 3 {
		t.Fatalf("override must REPLACE the two binds, not add mounts — got %d: %s", len(mounts), out)
	}
	byTarget := map[string]int{}
	for i, m := range mounts {
		byTarget[m.Target] = i
		if m.Type == "bind" {
			t.Errorf("SECURITY: %s is still a bind mount after the override — a remote deploy would mount a local path", m.Target)
		}
	}

	htmlVol := SeedVolumeName(slug, "html")
	if i, ok := byTarget["/usr/share/nginx/html"]; !ok {
		t.Error("the html mount disappeared")
	} else {
		m := mounts[i]
		if m.Source != htmlVol {
			t.Errorf("html mount source = %q, want the seeded volume %q", m.Source, htmlVol)
		}
		if !m.ReadOnly {
			t.Error("read_only was lost in the override")
		}
	}

	confVol := SeedVolumeName(slug, "nginx.conf")
	if i, ok := byTarget["/etc/nginx/nginx.conf"]; !ok {
		t.Error("the nginx.conf mount disappeared")
	} else {
		m := mounts[i]
		if m.Source != confVol {
			t.Errorf("conf mount source = %q, want %q", m.Source, confVol)
		}
		if m.Volume == nil || m.Volume.Subpath != "nginx.conf" {
			t.Errorf("single-file bind must survive as a subpath mount, got %+v", m.Volume)
		}
	}

	// A named volume the project already declared must be left alone.
	if _, ok := byTarget["/var/cache/nginx"]; !ok {
		t.Error("the project's own named volume was dropped by the override")
	}

	// external:true must keep the exact name we seeded — a prefixed name would
	// point compose at a volume that has no project files in it.
	for _, name := range []string{htmlVol, confVol} {
		v, ok := cfg.Volumes[name]
		if !ok {
			t.Errorf("seeded volume %q missing from the resolved config: %s", name, out)
			continue
		}
		if !v.External {
			t.Errorf("volume %q should be external", name)
		}
		if v.Name != name {
			t.Errorf("volume name = %q, want the unprefixed %q", v.Name, name)
		}
	}
}
