// Package service implements `dockercmd --install-service` and friends: it
// installs Docker Commander as a background service for the current OS so the
// monitor, alert engine and metric history keep running without a browser.
//
// Linux installs the hardened systemd unit (the same one in deploy/, embedded
// here and kept identical by a test); macOS installs a per-user launchd
// LaunchAgent. Windows is not supported here yet — use deploy/install-windows.ps1.
//
// The OS-specific Install/Uninstall/Status live in service_{linux,darwin}.go;
// this file holds the embedded templates and small shared helpers.
package service

import (
	_ "embed"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// systemdUnit is byte-identical to deploy/dockercmd.service (enforced by
// TestSystemdUnitMatchesDeployFile) so the embedded and reference units never
// drift apart.
//
//go:embed unit.service
var systemdUnit string

// launchdPlistTmpl is the per-user LaunchAgent plist, rendered with the paths
// computed at install time (text/template; see service_darwin.go).
//
//go:embed agent.plist.tmpl
var launchdPlistTmpl string

// manPage is byte-identical to deploy/dockercmd.1 (enforced by
// TestManPageMatchesDeployFile); the installer writes it so `man dockercmd`
// works on the installed machine.
//
//go:embed dockercmd.1
var manPage string

// manFileName is the installed man page's filename (section 1).
const manFileName = "dockercmd.1"

const (
	// binDest is where the installer places the binary so the service points at
	// a stable path that survives moving/`--self-upgrade` of the source binary.
	binDest = "/usr/local/bin/dockercmd"

	svcUser      = "dockercmd"
	linuxDataDir = "/var/lib/dockercmd"
	unitPath     = "/etc/systemd/system/dockercmd.service"
	launchdLabel = "dev.koduj.dockercmd"

	// manDir is a standard man-page directory on the default manpath of both
	// Linux and macOS, so `man dockercmd` resolves after install.
	manDir = "/usr/local/share/man/man1"
)

// copyFile copies src to dst (creating parent dirs) via a temp file + rename, so
// replacing a running executable doesn't fail with ETXTBSY mid-write.
func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp-install"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		// Close too (and surface its error if any), but the copy error stays primary.
		err = errors.Join(err, out.Close())
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// runCmd runs an external command, streaming its output to w.
func runCmd(w io.Writer, name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdout = w
	c.Stderr = w
	return c.Run()
}

// installManPage writes the embedded man page into man1Dir. It is best-effort:
// a failure (e.g. the dir isn't writable) is reported but never fails the
// install — the service works fine without `man dockercmd`.
func installManPage(w io.Writer, man1Dir string) {
	dst := filepath.Join(man1Dir, manFileName)
	if err := os.MkdirAll(man1Dir, 0o755); err != nil {
		fmt.Fprintf(w, "note: man page not installed (mkdir %s: %v)\n", man1Dir, err)
		return
	}
	if err := os.WriteFile(dst, []byte(manPage), 0o644); err != nil {
		fmt.Fprintf(w, "note: man page not installed (%v)\n", err)
		return
	}
	fmt.Fprintf(w, "Wrote man page → %s (try: man dockercmd)\n", dst)
}

// removeManPage deletes the installed man page; a missing file is not an error.
func removeManPage(w io.Writer, man1Dir string) {
	dst := filepath.Join(man1Dir, manFileName)
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(w, "note: could not remove man page %s: %v\n", dst, err)
	}
}

// xmlEscape escapes a string for safe interpolation into the launchd plist
// (XML). The values are our own paths, not untrusted input, so this is defensive
// correctness — a home dir with an '&' would otherwise produce an invalid plist
// that launchctl refuses to load.
func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// selfPath returns the absolute, symlink-resolved path of the running binary.
func selfPath() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	return self, nil
}
