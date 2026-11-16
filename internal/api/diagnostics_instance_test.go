package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/selfupdate"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// baseEnv is a healthy Linux install under systemd with self-update possible.
func baseEnv() instanceEnv {
	return instanceEnv{
		version: "1.7.4", exe: "/usr/local/bin/dockercmd", runAs: "dockercmd",
		systemd: true, goos: "linux", selfUpdate: true, canRestart: true,
		dataDir: "/var/lib/dockercmd", dataDirPaths: []string{"/var/lib/dockercmd"},
		writable: func(string) error { return nil },
		hasLocal: true, compose: true, buildx: true, trivy: false,
	}
}

func hasDetail(r docker.CheckResult, sub string) bool {
	for _, d := range r.Details {
		if strings.Contains(d, sub) {
			return true
		}
	}
	return false
}

// PENTEST: the instance diagnostics name the binary, the service user, the
// data directory and local stack paths. A "diagnostics" grant (which is per
// host) must not reach them; only an admin may.
func TestPentestInstanceDiagnostics_AdminOnly(t *testing.T) {
	if got := sectionForPath("/api/instance/diagnostics"); got != "__admin" {
		t.Fatalf(`sectionForPath = %q, want "__admin"`, got)
	}
	srv, st, u := scopedFixture(t, "diagnostics", 7)
	admin, err := st.CreateUser(context.Background(), &store.User{Username: "root", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	reached := false
	h := srv.permissions(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(200)
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/instance/diagnostics?host=7", nil).WithContext(ctxAs(u.ID, "user")))
	if w.Code != http.StatusForbidden || reached {
		t.Errorf("SECURITY: a user with a diagnostics grant reached instance diagnostics: %d, reached=%v", w.Code, reached)
	}

	reached = false
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/instance/diagnostics", nil).WithContext(ctxAs(admin, "admin")))
	if w.Code != 200 || !reached {
		t.Errorf("an admin should reach it: %d, reached=%v", w.Code, reached)
	}
}

func TestInstanceChecks_FixedOrder(t *testing.T) {
	var ids []string
	for _, c := range instanceChecks(baseEnv()) {
		ids = append(ids, c.ID)
	}
	want := "dc_install,dc_sandbox,dc_data_dir,dc_docker,dc_stack_files,dc_tools"
	if got := strings.Join(ids, ","); got != want {
		t.Errorf("checks = %s, want %s", got, want)
	}
}

func TestCheckInstall(t *testing.T) {
	cases := []struct {
		name       string
		mut        func(*instanceEnv)
		status     docker.CheckStatus
		msg, cmd   string
		extraCheck string
	}{
		{"can update in place", func(e *instanceEnv) {}, docker.CheckOK, "Update & restart can install", "", ""},
		{"turned off wins over anything else", func(e *instanceEnv) {
			e.selfUpdate = false
			e.precheck = &selfupdate.ErrNotWritable{Dir: "/usr/local/bin", Err: syscall.EROFS}
		}, docker.CheckOK, "DC_SELF_UPDATE=0", "", ""},
		{"no in-place restart", func(e *instanceEnv) { e.canRestart = false }, docker.CheckOK, "aren't offered on this platform", "", ""},
		{"package", func(e *instanceEnv) {
			e.precheck = &selfupdate.ErrPackageManaged{Exe: "/usr/bin/dockercmd", Manager: "apt"}
		}, docker.CheckOK, "package manager", "sudo apt update", ""},
		{"container", func(e *instanceEnv) { e.precheck = &selfupdate.ErrInContainer{} }, docker.CheckOK, "container", "docker pull", "Started in: a container"},
		{"unwritable dir is a warning", func(e *instanceEnv) {
			e.precheck = &selfupdate.ErrNotWritable{Dir: "/usr/local/bin", Err: syscall.EROFS}
		}, docker.CheckWarn, "/usr/local/bin", "sudo dockercmd --self-upgrade", "ReadWritePaths"},
		{"unknown failure is a warning", func(e *instanceEnv) { e.precheck = errors.New("boom") }, docker.CheckWarn, "boom", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := baseEnv()
			c.mut(&e)
			r := checkInstall(e)
			if r.Status != c.status {
				t.Errorf("status = %s, want %s (%s)", r.Status, c.status, r.Message)
			}
			if !strings.Contains(r.Message, c.msg) {
				t.Errorf("message = %q, want %q in it", r.Message, c.msg)
			}
			if c.cmd != "" && !hasDetail(r, "Update with: "+c.cmd) {
				t.Errorf("details = %v, want the command %q", r.Details, c.cmd)
			}
			if c.extraCheck != "" && !hasDetail(r, c.extraCheck) {
				t.Errorf("details = %v, want %q", r.Details, c.extraCheck)
			}
			if !hasDetail(r, "Version: 1.7.4") || !hasDetail(r, "Runs as: dockercmd") {
				t.Errorf("details = %v, want version and user", r.Details)
			}
		})
	}
}

func TestCheckSandbox(t *testing.T) {
	t.Run("ProtectHome and ProtectSystem detected", func(t *testing.T) {
		e := baseEnv()
		e.homeErr = &fs.PathError{Op: "open", Path: "/home", Err: fs.ErrPermission}
		e.etcErr = syscall.EROFS
		r := checkSandbox(e)
		if r.Status != docker.CheckOK || !hasDetail(r, "ProtectHome") || !hasDetail(r, "ProtectSystem=strict") || !hasDetail(r, "systemctl edit") {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("plain permission denial on /etc isn't ProtectSystem", func(t *testing.T) {
		e := baseEnv()
		e.etcErr = syscall.EACCES // a normal user can't write /etc either
		if r := checkSandbox(e); hasDetail(r, "ProtectSystem") || !strings.Contains(r.Message, "no filesystem sandbox") {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("not under systemd", func(t *testing.T) {
		e := baseEnv()
		e.systemd = false
		if r := checkSandbox(e); !strings.Contains(r.Message, "Not running as a systemd service") {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("skipped off Linux", func(t *testing.T) {
		e := baseEnv()
		e.goos = "darwin"
		if r := checkSandbox(e); r.Status != docker.CheckSkipped {
			t.Errorf("status = %s", r.Status)
		}
	})
}

func TestCheckDataDir(t *testing.T) {
	t.Run("all writable", func(t *testing.T) {
		e := baseEnv()
		e.dataDirPaths = []string{"/var/lib/dockercmd", "/var/lib/dockercmd/commander.db"}
		if r := checkDataDir(e); r.Status != docker.CheckOK || !strings.Contains(r.Message, "2 paths checked") {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("root-owned leftovers fail with the chown fix", func(t *testing.T) {
		e := baseEnv()
		bad := "/var/lib/dockercmd/projects/3"
		e.dataDirPaths = []string{"/var/lib/dockercmd", bad}
		e.writable = func(p string) error {
			if p == bad {
				return syscall.EACCES
			}
			return nil
		}
		r := checkDataDir(e)
		if r.Status != docker.CheckFail || !hasDetail(r, bad) || !hasDetail(r, "sudo chown -R dockercmd: /var/lib/dockercmd") {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("no data dir", func(t *testing.T) {
		e := baseEnv()
		e.dataDir = ""
		if r := checkDataDir(e); r.Status != docker.CheckSkipped {
			t.Errorf("status = %s", r.Status)
		}
	})
}

func TestDataDirPaths(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"projects/1", "projects/2", "project-revisions/1", "keys"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "commander.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// The check must not write into the data directory: pin its mtime.
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(dir, past, past); err != nil {
		t.Fatal(err)
	}
	paths, more := dataDirPaths(dir)
	got := map[string]bool{}
	for _, p := range paths {
		rel, _ := filepath.Rel(dir, p)
		got[rel] = true
		if err := docker.PathWritable(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, want := range []string{".", "commander.db", "keys", "projects", "projects/1", "projects/2", "project-revisions/1"} {
		if !got[want] {
			t.Errorf("missing %q in %v", want, paths)
		}
	}
	if more {
		t.Error("more should be false for a small dir")
	}
	if fi, err := os.Stat(dir); err != nil || !fi.ModTime().Equal(past) {
		t.Error("checking the data directory wrote into it")
	}
}

func TestDataDirPaths_Capped(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < dataDirCheckMax+5; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths, more := dataDirPaths(dir)
	if len(paths) != dataDirCheckMax || !more {
		t.Errorf("len = %d, more = %v; want %d, true", len(paths), more, dataDirCheckMax)
	}
}

func TestCheckDockerAccess(t *testing.T) {
	cases := []struct {
		name   string
		mut    func(*instanceEnv)
		status docker.CheckStatus
		want   string
	}{
		{"connected", func(e *instanceEnv) {}, docker.CheckOK, "Connected"},
		{"no local host", func(e *instanceEnv) { e.hasLocal = false }, docker.CheckSkipped, "No local Docker host"},
		{"socket permission", func(e *instanceEnv) {
			e.dockerErr = errors.New("dial unix /var/run/docker.sock: connect: permission denied")
		}, docker.CheckFail, `(user "dockercmd") isn't allowed`},
		{"daemon down", func(e *instanceEnv) { e.dockerErr = errors.New("connection refused") }, docker.CheckFail, "Can't reach"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := baseEnv()
			c.mut(&e)
			r := checkDockerAccess(e)
			if r.Status != c.status || !strings.Contains(r.Message, c.want) {
				t.Errorf("got %s %q, want %s %q", r.Status, r.Message, c.status, c.want)
			}
		})
	}
	e := baseEnv()
	e.dockerErr = errors.New("permission denied")
	if r := checkDockerAccess(e); !hasDetail(r, "SupplementaryGroups=docker") {
		t.Errorf("details = %v, want the docker group fix", r.Details)
	}
}

func TestCheckStackFiles(t *testing.T) {
	ok := docker.StackFileAccess{Project: "b-ok", Path: "/srv/b/compose.yml", Readable: true, Writable: true}
	ro := docker.StackFileAccess{Project: "c-ro", Path: "/srv/c/compose.yml", Readable: true, Reason: "read-only file system"}
	hidden := docker.StackFileAccess{Project: "a-home", Path: "/home/u/a/compose.yml", Reason: "permission denied — ProtectHome"}

	t.Run("unreadable is a warning, listed first and sorted", func(t *testing.T) {
		e := baseEnv()
		e.stacks = []docker.StackFileAccess{ro, ok, hidden}
		r := checkStackFiles(e)
		if r.Status != docker.CheckWarn || !strings.Contains(r.Message, "1 of 3") {
			t.Fatalf("got %+v", r)
		}
		if len(r.Details) != 2 || !strings.HasPrefix(r.Details[0], "a-home: ") || !strings.HasPrefix(r.Details[1], "c-ro (read-only): ") {
			t.Errorf("details = %v", r.Details)
		}
	})
	t.Run("read-only alone is fine but listed", func(t *testing.T) {
		e := baseEnv()
		e.stacks = []docker.StackFileAccess{ok, ro}
		r := checkStackFiles(e)
		if r.Status != docker.CheckOK || !strings.Contains(r.Message, "1 open read-only") || len(r.Details) != 1 {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("all good", func(t *testing.T) {
		e := baseEnv()
		e.stacks = []docker.StackFileAccess{ok}
		if r := checkStackFiles(e); r.Status != docker.CheckOK || len(r.Details) != 0 {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("skipped without a reachable local daemon", func(t *testing.T) {
		e := baseEnv()
		e.dockerErr = errors.New("permission denied")
		if r := checkStackFiles(e); r.Status != docker.CheckSkipped {
			t.Errorf("status = %s", r.Status)
		}
	})
}

func TestCheckTools(t *testing.T) {
	e := baseEnv()
	if r := checkTools(e); r.Status != docker.CheckOK || !hasDetail(r, "trivy (image Scan, optional): not found") {
		t.Errorf("got %+v", r)
	}
	e.compose = false
	if r := checkTools(e); r.Status != docker.CheckWarn || !strings.Contains(r.Message, "Projects can't be deployed") {
		t.Errorf("got %+v", r)
	}
}
