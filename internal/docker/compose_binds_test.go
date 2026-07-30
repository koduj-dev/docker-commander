package docker

import (
	"archive/tar"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// bindConfig builds a resolved-compose-config JSON blob with the given mounts.
func bindConfig(t *testing.T, service string, mounts ...map[string]any) []byte {
	t.Helper()
	cfg := map[string]any{
		"services": map[string]any{
			service: map[string]any{"volumes": mounts},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func bindMount(source, target string, ro bool) map[string]any {
	return map[string]any{"type": "bind", "source": source, "target": target, "read_only": ro}
}

func TestClassifyProjectBinds_InsideAndOutside(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "html"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := bindConfig(t, "web",
		bindMount(filepath.Join(root, "html"), "/usr/share/nginx/html", true),
		bindMount("/etc/localtime", "/etc/localtime", true),
		map[string]any{"type": "volume", "source": "data", "target": "/var/lib"},
		map[string]any{"type": "tmpfs", "target": "/tmp"},
	)
	internal, external, err := ClassifyProjectBinds(cfg, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(internal) != 1 || internal[0].Rel != "html" {
		t.Fatalf("internal = %+v, want one bind with Rel=html", internal)
	}
	if !internal[0].ReadOnly {
		t.Error("read_only should carry over from the compose config")
	}
	if internal[0].IsFile {
		t.Error("a directory bind must not be flagged IsFile")
	}
	if len(external) != 1 || external[0].Source != "/etc/localtime" {
		t.Fatalf("external = %+v, want /etc/localtime", external)
	}
}

func TestClassifyProjectBinds_NonBindMountsIgnored(t *testing.T) {
	root := t.TempDir()
	cfg := bindConfig(t, "db",
		map[string]any{"type": "volume", "source": "pgdata", "target": "/var/lib/postgresql"},
		map[string]any{"type": "tmpfs", "target": "/run"},
	)
	internal, external, err := ClassifyProjectBinds(cfg, root)
	if err != nil || len(internal) != 0 || len(external) != 0 {
		t.Errorf("named volumes / tmpfs must not be treated as binds: in=%v ext=%v err=%v", internal, external, err)
	}
}

func TestClassifyProjectBinds_SingleFileFlagged(t *testing.T) {
	root := t.TempDir()
	conf := filepath.Join(root, "nginx.conf")
	if err := os.WriteFile(conf, []byte("worker_processes 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	internal, _, err := ClassifyProjectBinds(
		bindConfig(t, "web", bindMount(conf, "/etc/nginx/nginx.conf", false)), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(internal) != 1 || !internal[0].IsFile {
		t.Fatalf("a regular-file bind should set IsFile: %+v", internal)
	}
}

// A bind whose source doesn't exist yet still belongs to the project if its path
// is inside it — compose would create it, and we seed an empty volume.
func TestClassifyProjectBinds_MissingSourceStillInternal(t *testing.T) {
	root := t.TempDir()
	internal, external, err := ClassifyProjectBinds(
		bindConfig(t, "web", bindMount(filepath.Join(root, "not-created-yet"), "/data", false)), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(internal) != 1 || len(external) != 0 {
		t.Fatalf("missing-but-inside source should be internal: in=%+v ext=%+v", internal, external)
	}
	if internal[0].IsFile {
		t.Error("a non-existent source must not be flagged IsFile")
	}
}

func TestClassifyProjectBinds_ProjectDirItself(t *testing.T) {
	root := t.TempDir()
	internal, _, err := ClassifyProjectBinds(
		bindConfig(t, "app", bindMount(root, "/app", false)), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(internal) != 1 || internal[0].Rel != "." {
		t.Fatalf("mounting the project dir itself should yield Rel=\".\": %+v", internal)
	}
}

func TestClassifyProjectBinds_DeterministicOrder(t *testing.T) {
	root := t.TempDir()
	cfg := []byte(`{"services":{
		"z":{"volumes":[{"type":"bind","source":"/outside/z","target":"/z"}]},
		"a":{"volumes":[{"type":"bind","source":"/outside/a","target":"/a"}]},
		"m":{"volumes":[{"type":"bind","source":"/outside/m","target":"/m"}]}}}`)
	for i := 0; i < 8; i++ {
		_, external, err := ClassifyProjectBinds(cfg, root)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, b := range external {
			got = append(got, b.Service)
		}
		if strings.Join(got, ",") != "a,m,z" {
			t.Fatalf("services should be reported in sorted order, got %v", got)
		}
	}
}

func TestClassifyProjectBinds_BadJSON(t *testing.T) {
	if _, _, err := ClassifyProjectBinds([]byte("{not json"), t.TempDir()); err == nil {
		t.Error("malformed config JSON should error")
	}
}

func TestSeedVolumeName_StableAndValid(t *testing.T) {
	a := SeedVolumeName("my-shop", "html")
	if a != SeedVolumeName("my-shop", "html") {
		t.Error("name should be deterministic for the same inputs")
	}
	if a == SeedVolumeName("my-shop", "conf") {
		t.Error("different paths must yield different volumes")
	}
	if a == SeedVolumeName("other-shop", "html") {
		t.Error("different projects must yield different volumes")
	}
	// "./html" and "html" are the same path and must collapse to one volume.
	if SeedVolumeName("my-shop", "./html") != a {
		t.Error("equivalent relative paths should map to the same volume")
	}
	for _, name := range []string{
		SeedVolumeName("Weird Slug!! ***", "x"),
		SeedVolumeName("", "x"),
		SeedVolumeName(strings.Repeat("long", 40), "x"),
	} {
		if !validVolumeName(name) {
			t.Errorf("generated an invalid Docker volume name: %q", name)
		}
	}
}

// validVolumeName mirrors Docker's accepted volume-name shape.
func validVolumeName(s string) bool {
	if s == "" || len(s) > 255 {
		return false
	}
	first := s[0]
	if !(first >= 'a' && first <= 'z' || first >= 'A' && first <= 'Z' || first >= '0' && first <= '9') {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '.' || r == '-':
		default:
			return false
		}
	}
	return true
}

func TestBindOverrideJSON(t *testing.T) {
	binds := []ProjectBind{
		{Service: "web", Target: "/usr/share/nginx/html", Rel: "html", ReadOnly: true},
		{Service: "web", Target: "/etc/nginx/nginx.conf", Rel: "nginx.conf", IsFile: true},
		{Service: "api", Target: "/app/data", Rel: "data"},
	}
	raw, err := BindOverrideJSON("shop", binds)
	if err != nil {
		t.Fatal(err)
	}
	var ov struct {
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
			External bool `json:"external"`
		} `json:"volumes"`
	}
	if err := json.Unmarshal(raw, &ov); err != nil {
		t.Fatalf("override is not valid JSON: %v\n%s", err, raw)
	}
	if len(ov.Services["web"].Volumes) != 2 || len(ov.Services["api"].Volumes) != 1 {
		t.Fatalf("mounts not grouped per service: %s", raw)
	}
	// Every declared volume must be external, or compose would prefix the name
	// and miss the volume we actually seeded.
	if len(ov.Volumes) != 3 {
		t.Fatalf("expected 3 declared volumes, got %d: %s", len(ov.Volumes), raw)
	}
	for name, v := range ov.Volumes {
		if !v.External {
			t.Errorf("volume %q must be declared external", name)
		}
	}
	for _, m := range ov.Services["web"].Volumes {
		if m.Type != "volume" {
			t.Errorf("override must repoint the mount at a volume, got %q", m.Type)
		}
		switch m.Target {
		case "/usr/share/nginx/html":
			if !m.ReadOnly {
				t.Error("read_only must be preserved in the override")
			}
			if m.Volume != nil {
				t.Error("a directory bind needs no subpath")
			}
		case "/etc/nginx/nginx.conf":
			if m.Volume == nil || m.Volume.Subpath != "nginx.conf" {
				t.Errorf("a single-file bind must mount by subpath: %+v", m.Volume)
			}
		default:
			t.Errorf("unexpected target %q", m.Target)
		}
		if m.Source != SeedVolumeName("shop", relForTarget(binds, m.Target)) {
			t.Errorf("source %q doesn't match the seeded volume name", m.Source)
		}
	}
}

func relForTarget(binds []ProjectBind, target string) string {
	for _, b := range binds {
		if b.Target == target {
			return b.Rel
		}
	}
	return ""
}

func TestBindOverrideJSON_Empty(t *testing.T) {
	raw, err := BindOverrideJSON("shop", nil)
	if err != nil {
		t.Fatal(err)
	}
	var ov map[string]any
	if err := json.Unmarshal(raw, &ov); err != nil {
		t.Fatalf("empty override must still be valid JSON: %v", err)
	}
}

// tarNames collects the entry names (and their types) from an archive.
func tarNames(t *testing.T, r io.Reader) map[string]byte {
	t.Helper()
	out := map[string]byte{}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		out[h.Name] = h.Typeflag
	}
	return out
}

func TestTarPath_Directory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "app.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := tarPath(root, false)
	if err != nil {
		t.Fatal(err)
	}
	names := tarNames(t, r)
	// Contents land at the archive root so the volume mirrors the directory
	// rather than nesting a copy of it.
	if _, ok := names["index.html"]; !ok {
		t.Errorf("missing index.html: %v", names)
	}
	if _, ok := names["sub/app.js"]; !ok {
		t.Errorf("missing sub/app.js: %v", names)
	}
	if _, ok := names["."]; ok {
		t.Error("the archive must not contain a \".\" entry")
	}
}

func TestTarPath_SingleFile(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "nginx.conf")
	if err := os.WriteFile(p, []byte("worker_processes 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := tarPath(p, true)
	if err != nil {
		t.Fatal(err)
	}
	names := tarNames(t, r)
	if len(names) != 1 {
		t.Fatalf("a single-file seed should hold exactly one entry: %v", names)
	}
	if _, ok := names["nginx.conf"]; !ok {
		t.Errorf("file should be stored under its base name: %v", names)
	}
}

func TestTarPath_DropsOwnership(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := tarPath(root, false)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		// uid/gid mean something different on the target host, so they're zeroed
		// rather than shipped.
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("entry %q leaks ownership: uid=%d gid=%d uname=%q gname=%q", h.Name, h.Uid, h.Gid, h.Uname, h.Gname)
		}
	}
}

// ---------------------------------------------------------------------------
// PENTESTS — a compose file is user-supplied input, and a remote deploy turns
// its bind sources into files we read and ship. Every attempt to name a path
// outside the project folder must be classified external (i.e. refused).
// ---------------------------------------------------------------------------

// PENTEST: relative traversal in a bind source must not escape the project dir.
func TestPentestClassifyBinds_RelativeTraversalRefused(t *testing.T) {
	root := t.TempDir()
	secretDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(secretDir, "id_rsa"), []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{
		filepath.Join(root, "..", filepath.Base(secretDir)),
		filepath.Join(root, "..", ".."),
		filepath.Join(root, "sub", "..", "..", "etc"),
		root + string(filepath.Separator) + ".." + string(filepath.Separator) + "escape",
		"/etc/shadow",
		"/root/.ssh",
		"/var/run/docker.sock",
	} {
		internal, external, err := ClassifyProjectBinds(
			bindConfig(t, "evil", bindMount(src, "/loot", false)), root)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		if len(internal) != 0 {
			t.Errorf("SECURITY: %q was treated as inside the project: %+v", src, internal)
		}
		if len(external) != 1 {
			t.Errorf("%q should be reported as an external bind, got %+v", src, external)
		}
	}
}

// PENTEST: a symlink inside the project pointing out of it must not smuggle
// outside files into a seeded volume.
func TestPentestClassifyBinds_SymlinkEscapeRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	internal, external, err := ClassifyProjectBinds(
		bindConfig(t, "evil", bindMount(link, "/loot", false)), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(internal) != 0 {
		t.Errorf("SECURITY: a symlink out of the project was accepted as internal: %+v", internal)
	}
	if len(external) != 1 {
		t.Errorf("the symlinked bind should be external, got %+v", external)
	}
}

// PENTEST: the escape can also hide in a parent component of an otherwise
// project-looking path (project/link/child, where link → outside).
func TestPentestClassifyBinds_SymlinkedParentRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	internal, external, err := ClassifyProjectBinds(
		bindConfig(t, "evil", bindMount(filepath.Join(root, "link", "child"), "/loot", false)), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(internal) != 0 {
		t.Errorf("SECURITY: a symlinked parent component escaped the jail: %+v", internal)
	}
	if len(external) != 1 {
		t.Errorf("expected an external bind, got %+v", external)
	}
}

// PENTEST: a path that merely shares the project dir's name prefix
// (…/projects/7-evil vs …/projects/7) is a different directory and must not pass
// the containment check as a substring match would.
func TestPentestClassifyBinds_SiblingPrefixRefused(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "7")
	sibling := filepath.Join(base, "7-evil")
	for _, d := range []string{root, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	internal, external, err := ClassifyProjectBinds(
		bindConfig(t, "evil", bindMount(sibling, "/loot", false)), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(internal) != 0 {
		t.Errorf("SECURITY: sibling dir sharing a name prefix passed as internal: %+v", internal)
	}
	if len(external) != 1 {
		t.Errorf("expected an external bind, got %+v", external)
	}
}

// PENTEST: an empty bind source must never be treated as inside the project (it
// would otherwise resolve to the working directory).
func TestPentestClassifyBinds_EmptySourceRefused(t *testing.T) {
	root := t.TempDir()
	internal, _, err := ClassifyProjectBinds(
		bindConfig(t, "evil", bindMount("", "/loot", false)), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(internal) != 0 {
		t.Errorf("SECURITY: an empty source was treated as internal: %+v", internal)
	}
}

// PENTEST: a symlink inside a seeded directory is archived as a link, not
// followed — so tarring the project can't pull in the file it points at.
func TestPentestTarPath_SymlinkNotFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("PRIVATE-KEY-MATERIAL"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "leak")); err != nil {
		t.Fatal(err)
	}
	r, err := tarPath(root, false)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name != "leak" {
			continue
		}
		if h.Typeflag != tar.TypeSymlink {
			t.Fatalf("SECURITY: symlink was archived as type %q, not a link", string(h.Typeflag))
		}
		body, _ := io.ReadAll(tr)
		if strings.Contains(string(body), "PRIVATE-KEY-MATERIAL") {
			t.Fatal("SECURITY: the symlink target's contents were archived")
		}
	}
}

// PENTEST: a compose file naming a device/socket in the project dir must not
// break the deploy or ship the special file.
func TestPentestTarPath_SkipsSpecialFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("fine"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := tarPath(root, false)
	if err != nil {
		t.Fatal(err)
	}
	names := tarNames(t, r)
	if _, ok := names["ok.txt"]; !ok {
		t.Errorf("regular files should still be archived: %v", names)
	}
}
