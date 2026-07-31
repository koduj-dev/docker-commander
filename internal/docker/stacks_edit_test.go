package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Unit tests for the pure parts of editing a CLI-discovered stack: which paths
// the compose file might be at, whether a host can be edited at all, and the
// containment rule that decides where a write is allowed to land. None of these
// need a Docker daemon, so they run under -short where they'll actually be seen.

func TestComposeCandidates(t *testing.T) {
	cases := []struct {
		name string
		st   Stack
		want []string
	}{
		{
			name: "absolute config file is used as-is, plus a workdir fallback",
			st:   Stack{ConfigFile: "/srv/app/compose.yml", WorkingDir: "/srv/app"},
			want: []string{"/srv/app/compose.yml", "/srv/app/compose.yml"},
		},
		{
			name: "relative config file joins the working dir",
			st:   Stack{ConfigFile: "compose.yml", WorkingDir: "/srv/app"},
			want: []string{"/srv/app/compose.yml", "/srv/app/compose.yml"},
		},
		{
			name: "only the first of a comma-separated list is used",
			st:   Stack{ConfigFile: "/srv/app/a.yml,/srv/app/b.yml", WorkingDir: "/srv/app"},
			want: []string{"/srv/app/a.yml", "/srv/app/a.yml"},
		},
		{
			name: "no working dir leaves a relative path alone",
			st:   Stack{ConfigFile: "compose.yml"},
			want: []string{"compose.yml"},
		},
		{
			name: "a config file elsewhere still offers the workdir basename fallback",
			st:   Stack{ConfigFile: "/other/place/docker-compose.yaml", WorkingDir: "/srv/app"},
			want: []string{"/other/place/docker-compose.yaml", "/srv/app/docker-compose.yaml"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := composeCandidates(&c.st)
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("candidate %d = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// TestStackEditableReason covers the single question every entry point asks
// before touching a stack: may we act on this compose file at all?
//
// It deliberately includes the containment rule. StackEditable feeds the UI's
// decision to show an editor, so a target the write path would refuse must not
// report itself as editable — otherwise the app offers an editor whose Save can
// only ever fail.
func TestStackEditableReason(t *testing.T) {
	work := t.TempDir()
	inside := filepath.Join(work, "compose.yml")
	if err := os.WriteFile(inside, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "compose.yml")
	if err := os.WriteFile(outside, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		kind       string
		workDir    string
		path       string
		wantEdit   bool
		wantSubstr string
	}{
		{name: "local host with a contained compose file", kind: "local", workDir: work, path: inside, wantEdit: true},
		{name: "empty kind is treated as local", kind: "", workDir: work, path: inside, wantEdit: true},
		{name: "ssh host, contained lexically", kind: "ssh", workDir: "/srv/app", path: "/srv/app/compose.yml", wantEdit: true},
		{
			name: "tcp host exposes no filesystem", kind: "tcp", workDir: work, path: inside,
			wantSubstr: "exposes no filesystem",
		},
		{
			name: "no working dir means nothing to scope the edit to", kind: "local", path: inside,
			wantSubstr: "no working directory",
		},
		{
			name: "compose file outside the working dir", kind: "local", workDir: work, path: outside,
			wantSubstr: "outside the stack's working directory",
		},
		{
			name: "ssh host with a path outside the working dir", kind: "ssh", workDir: "/srv/app", path: "/etc/evil.yml",
			wantSubstr: "outside the stack's working directory",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tgt := &stackTarget{host: &store.Host{Kind: c.kind}, workDir: c.workDir, path: c.path}
			got := tgt.editableReason()
			if c.wantEdit {
				if got != "" {
					t.Fatalf("expected editable, got refusal %q", got)
				}
				return
			}
			if got == "" {
				t.Fatal("expected a refusal reason, got none")
			}
			if !strings.Contains(got, c.wantSubstr) {
				t.Errorf("reason %q does not mention %q", got, c.wantSubstr)
			}
		})
	}
}

// TestWriteSiblingAtomicReplacesASymlink is the regression guard for the backup
// write. os.WriteFile follows a symlink at the destination; anyone able to create
// files in the stack's directory could pre-place one and have this process —
// often root — write through it, with content they also control.
func TestWriteSiblingAtomicReplacesASymlink(t *testing.T) {
	dir := t.TempDir()
	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "victim")
	const original = "DO NOT TOUCH\n"
	if err := os.WriteFile(victim, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(dir, "compose.yml.dc-prev")
	if err := os.Symlink(victim, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := writeSiblingAtomic(link, "backup contents\n", 0o600); err != nil {
		t.Fatalf("writeSiblingAtomic: %v", err)
	}

	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("SECURITY: the write followed the symlink and landed in %s:\n%s", victim, got)
	}
	// The link itself must have been replaced by a regular file with our content.
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("the destination is still a symlink — the write did not replace it")
	}
	if b, _ := os.ReadFile(link); string(b) != "backup contents\n" {
		t.Errorf("backup content = %q", b)
	}
	// No temp files left behind in the directory.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".dc-tmp-") {
			t.Errorf("temp file %q was left behind", e.Name())
		}
	}
}

func TestRemoteWithin(t *testing.T) {
	cases := []struct {
		root, p string
		want    bool
	}{
		{"/srv/app", "/srv/app/compose.yml", true},
		{"/srv/app", "/srv/app", true},
		{"/srv/app/", "/srv/app/compose.yml", true},
		{"/srv/app", "/srv/app/nested/deep/compose.yml", true},
		{"/srv/app", "/srv/app/../etc/compose.yml", false}, // traversal out
		{"/srv/app", "/srv/application/compose.yml", false},
		{"/srv/app", "/etc/compose.yml", false},
		{"/srv/app", "/srv", false},
		{"", "/srv/app/compose.yml", false},
		{"/srv/app", "", false},
		{"srv/app", "srv/app/compose.yml", false}, // both must be absolute
		{"/srv/app", "compose.yml", false},
	}
	for _, c := range cases {
		if got := remoteWithin(c.root, c.p); got != c.want {
			t.Errorf("remoteWithin(%q, %q) = %v, want %v", c.root, c.p, got, c.want)
		}
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/srv/app":             `'/srv/app'`,
		"/srv/my app":          `'/srv/my app'`,
		"/srv/a;rm -rf /":      `'/srv/a;rm -rf /'`,
		"/srv/$(whoami)":       `'/srv/$(whoami)'`,
		"/srv/`id`":            "'/srv/`id`'",
		`/srv/it's`:            `'/srv/it'\''s'`,
		`'; rm -rf / ; echo '`: `''\''; rm -rf / ; echo '\'''`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCheckContainedLocal covers the rule that decides whether a write is
// allowed to land: the compose file must sit inside the stack's working
// directory, with symlinks resolved.
func TestCheckContainedLocal(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "app")
	outside := filepath.Join(root, "secrets")
	for _, d := range []string{work, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	inside := filepath.Join(work, "compose.yml")
	if err := os.WriteFile(inside, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "target.yml")
	if err := os.WriteFile(target, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file inside the working dir that is really a symlink pointing out of it.
	escape := filepath.Join(work, "escape.yml")
	if err := os.Symlink(target, escape); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// A subdirectory of the working dir that is a symlink out of it.
	linkDir := filepath.Join(work, "sub")
	if err := os.Symlink(outside, linkDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	cases := []struct {
		name    string
		path    string
		workDir string
		wantErr bool
	}{
		{name: "file directly inside the working dir", path: inside, workDir: work},
		{name: "file outside the working dir", path: target, workDir: work, wantErr: true},
		{name: "symlinked file escaping the working dir", path: escape, workDir: work, wantErr: true},
		{name: "file under a symlinked subdirectory", path: filepath.Join(linkDir, "target.yml"), workDir: work, wantErr: true},
		{name: "traversal out of the working dir", path: filepath.Join(work, "..", "secrets", "target.yml"), workDir: work, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tgt := &stackTarget{
				host: &store.Host{Kind: "local"}, path: c.path, workDir: c.workDir,
			}
			err := tgt.checkContained()
			if c.wantErr && err == nil {
				t.Fatalf("expected %s to be refused, it was allowed", c.path)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("expected %s to be allowed, got %v", c.path, err)
			}
		})
	}
}

// TestCheckContainedRemote covers the same rule on an ssh host, where paths
// can't be canonicalised locally and the check is lexical.
func TestCheckContainedRemote(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		workDir string
		wantErr bool
	}{
		{name: "file inside the working dir", path: "/srv/app/compose.yml", workDir: "/srv/app"},
		{name: "file in a subdirectory", path: "/srv/app/deploy/compose.yml", workDir: "/srv/app"},
		{name: "file outside the working dir", path: "/etc/cron.d/evil.yml", workDir: "/srv/app", wantErr: true},
		{name: "traversal out of the working dir", path: "/srv/app/../../etc/evil.yml", workDir: "/srv/app", wantErr: true},
		{name: "sibling directory sharing a name prefix", path: "/srv/application/compose.yml", workDir: "/srv/app", wantErr: true},
		{name: "relative path", path: "compose.yml", workDir: "/srv/app", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tgt := &stackTarget{
				host: &store.Host{Kind: "ssh"}, path: c.path, workDir: c.workDir,
			}
			err := tgt.checkContained()
			if c.wantErr && err == nil {
				t.Fatalf("expected %s to be refused, it was allowed", c.path)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("expected %s to be allowed, got %v", c.path, err)
			}
		})
	}
}

// TestCheckContainedUnreadableWorkDir makes sure a working directory that can't
// be canonicalised fails closed rather than skipping the containment check.
func TestCheckContainedUnreadableWorkDir(t *testing.T) {
	tgt := &stackTarget{
		host:    &store.Host{Kind: "local"},
		path:    "/does/not/exist/compose.yml",
		workDir: "/does/not/exist",
	}
	if err := tgt.checkContained(); err == nil {
		t.Fatal("a working directory that cannot be resolved must be refused, not allowed through")
	}
}
