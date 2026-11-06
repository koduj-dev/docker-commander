package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakePackaging points the package-detection paths at temp locations, so a
// test decides what "installed from a package" means instead of the host it
// runs on. dpkgList is the content of dockercmd's dpkg file list ("" = no such
// file), binDir the directory packages install into, rpmDB whether an rpm
// database exists.
func fakePackaging(t *testing.T, dpkgList, binDir string, rpmDB bool) {
	t.Helper()
	tmp := t.TempDir()
	prevList, prevRPM, prevBin := dpkgFileList, rpmDBDirs, packageBinDirs
	t.Cleanup(func() { dpkgFileList, rpmDBDirs, packageBinDirs = prevList, prevRPM, prevBin })

	dpkgFileList = filepath.Join(tmp, "dockercmd.list")
	if dpkgList != "" {
		if err := os.WriteFile(dpkgFileList, []byte(dpkgList), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rpmDBDirs = []string{filepath.Join(tmp, "rpm")}
	if rpmDB {
		if err := os.Mkdir(rpmDBDirs[0], 0o755); err != nil {
			t.Fatal(err)
		}
	}
	packageBinDirs = []string{binDir}
}

// exeIn creates a fake dockercmd in dir and points osExecutable at it.
func exeIn(t *testing.T, dir string) string {
	t.Helper()
	exe := filepath.Join(dir, "dockercmd")
	if err := os.WriteFile(exe, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := osExecutable
	osExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { osExecutable = prev })
	return exe
}

func TestPackageManager(t *testing.T) {
	bin := t.TempDir()
	exe := filepath.Join(bin, "dockercmd")
	other := filepath.Join(t.TempDir(), "dockercmd")

	cases := []struct {
		name     string
		dpkgList string
		rpmDB    bool
		exe      string
		want     string
	}{
		{"listed by dpkg", "/.\n/usr\n" + exe + "\n/lib/systemd/system/dockercmd.service\n", false, exe, "apt"},
		{"dpkg list without this path", "/usr/share/man/man1/dockercmd.1\n", false, exe, ""},
		{"rpm system, binary in the package dir", "", true, exe, "dnf"},
		{"rpm system, binary installed by hand elsewhere", "", true, other, ""},
		{"package dir but no package manager knows it", "", false, exe, ""},
		{"manual install", "", false, other, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakePackaging(t, c.dpkgList, bin, c.rpmDB)
			if got := packageManager(c.exe); got != c.want {
				t.Errorf("packageManager(%q) = %q, want %q", c.exe, got, c.want)
			}
		})
	}
}

func TestErrPackageManagedCommand(t *testing.T) {
	if got := (&ErrPackageManaged{Manager: "apt"}).Command(); got != "sudo apt update && sudo apt install --only-upgrade dockercmd" {
		t.Errorf("apt command = %q", got)
	}
	if got := (&ErrPackageManaged{Manager: "dnf"}).Command(); got != "sudo dnf upgrade dockercmd" {
		t.Errorf("dnf command = %q", got)
	}
}

func TestPrecheck(t *testing.T) {
	t.Run("hand-installed in a writable dir may update", func(t *testing.T) {
		dir := t.TempDir()
		exeIn(t, dir)
		fakePackaging(t, "", t.TempDir(), false)
		// It answers from access(2): loading the update banner must not create
		// (even briefly) a file next to the binary. Creating and removing one
		// bumps the directory's mtime, so pin it in the past and compare.
		past := time.Now().Add(-time.Hour).Truncate(time.Second)
		if err := os.Chtimes(dir, past, past); err != nil {
			t.Fatal(err)
		}
		if err := Precheck(); err != nil {
			t.Fatalf("Precheck = %v, want nil", err)
		}
		if fi, err := os.Stat(dir); err != nil || !fi.ModTime().Equal(past) {
			t.Errorf("Precheck wrote into the binary's directory (mtime %v, want %v)", fi.ModTime(), past)
		}
	})

	t.Run("packaged binary is refused even when writable", func(t *testing.T) {
		dir := t.TempDir()
		exe := exeIn(t, dir)
		fakePackaging(t, exe+"\n", t.TempDir(), false)
		var pkg *ErrPackageManaged
		if err := Precheck(); !errors.As(err, &pkg) {
			t.Fatalf("Precheck = %v, want *ErrPackageManaged", err)
		}
		if pkg.Manager != "apt" || pkg.Exe != exe {
			t.Errorf("got %+v", pkg)
		}
	})

	t.Run("unwritable dir is reported before any install", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("access(2)-based check is a no-op on Windows")
		}
		if os.Geteuid() == 0 {
			t.Skip("running as root: chmod-based permission denial doesn't apply")
		}
		dir := t.TempDir()
		exeIn(t, dir)
		fakePackaging(t, "", t.TempDir(), false)
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

		var nw *ErrNotWritable
		if err := Precheck(); !errors.As(err, &nw) {
			t.Fatalf("Precheck = %v, want *ErrNotWritable", err)
		}
		if nw.Dir != dir {
			t.Errorf("Dir = %q, want %q", nw.Dir, dir)
		}
	})
}

// PENTEST-adjacent: a packaged binary must never be swapped by the self-update
// path — not from the web UI, not by auto-apply, not by `--self-upgrade` —
// even when its directory happens to be writable (root, no sandbox). Proven by
// a release server whose download fails the test if it's ever hit, and by the
// binary staying as it was.
func TestApplyNeverReplacesPackagedBinary(t *testing.T) {
	downloaded := false
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == "download" {
			downloaded = true
			_, _ = w.Write([]byte("NEW"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v9.9.9", "assets": []map[string]any{
				{"name": platformAsset(), "browser_download_url": base + "/download", "digest": "sha256:deadbeef"},
			},
		})
	}))
	defer srv.Close()
	base = srv.URL
	prevBase := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() { apiBaseURL = prevBase })

	exe := exeIn(t, t.TempDir())
	fakePackaging(t, exe+"\n", t.TempDir(), false)

	_, err := Apply(context.Background(), "1.0.0")
	var pkg *ErrPackageManaged
	if !errors.As(err, &pkg) {
		t.Fatalf("Apply err = %v, want *ErrPackageManaged", err)
	}
	var nw *ErrNotWritable
	if errors.As(err, &nw) {
		t.Error("must not be ErrNotWritable — the CLI would then offer to re-run with sudo and overwrite it")
	}
	if downloaded {
		t.Error("the release asset was downloaded for a packaged binary")
	}
	if got, _ := os.ReadFile(exe); string(got) != "OLD" {
		t.Errorf("packaged binary was replaced: %q", got)
	}
}
