//go:build darwin

package service

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/template"
)

// darwinPaths returns the per-user install locations. Everything lives under the
// user's Library so the install needs no sudo — a per-user LaunchAgent is also
// the only thing that can reach Docker Desktop's user-owned socket.
func darwinPaths() (dataDir, binPath, plistPath, logPath string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", "", "", err
	}
	dataDir = filepath.Join(home, "Library", "Application Support", "dockercmd")
	binPath = filepath.Join(dataDir, "dockercmd")
	plistPath = filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
	logPath = filepath.Join(home, "Library", "Logs", "dockercmd.log")
	return dataDir, binPath, plistPath, logPath, nil
}

// Install sets Docker Commander up as a per-user launchd LaunchAgent. Run it as
// your normal user (not sudo) so the agent inherits access to Docker Desktop.
func Install(w io.Writer) error {
	if os.Geteuid() == 0 {
		return errors.New("run this as your normal user, NOT sudo — the agent must reach your Docker socket")
	}
	dataDir, binPath, plistPath, logPath, err := darwinPaths()
	if err != nil {
		return err
	}

	// Place the binary under the (user-writable) data dir — no /usr/local/bin
	// sudo step needed for a user agent.
	self, err := selfPath()
	if err != nil {
		return fmt.Errorf("locate running binary: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if self != binPath {
		if err := copyFile(self, binPath, 0o755); err != nil {
			return fmt.Errorf("install binary to %s: %w", binPath, err)
		}
		fmt.Fprintf(w, "Installed binary → %s\n", binPath)
	}

	// Render and write the LaunchAgent plist.
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return err
	}
	tmpl, err := template.New("plist").Funcs(template.FuncMap{"xml": xmlEscape}).Parse(launchdPlistTmpl)
	if err != nil {
		return fmt.Errorf("parse plist template: %w", err)
	}
	f, err := os.OpenFile(plistPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	data := map[string]string{"Label": launchdLabel, "BinPath": binPath, "DataDir": dataDir, "LogPath": logPath}
	if err := tmpl.Execute(f, data); err != nil {
		f.Close()
		return fmt.Errorf("write plist: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(w, "Wrote LaunchAgent → %s\n", plistPath)

	// (Re)load the agent in the user's GUI domain.
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = runCmd(io.Discard, "launchctl", "bootout", domain+"/"+launchdLabel) // ignore "not loaded"
	if err := runCmd(w, "launchctl", "bootstrap", domain, plistPath); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w", err)
	}
	_ = runCmd(io.Discard, "launchctl", "enable", domain+"/"+launchdLabel)

	fmt.Fprintln(w, "\n✅ Service installed and started.")
	fmt.Fprintf(w, "   Status: dockercmd --service-status   (logs: tail -f %q)\n", logPath)
	fmt.Fprintln(w, "   Listen address + TLS come from DC_HOST/DC_PORT/DC_TLS_* (default 127.0.0.1:8470).")
	fmt.Fprintln(w, "   Then create the admin account in the UI.")
	return nil
}

// Uninstall unloads and removes the LaunchAgent. The data dir is left in place
// so reinstalling keeps the database and keys.
func Uninstall(w io.Writer) error {
	if os.Geteuid() == 0 {
		return errors.New("run this as your normal user, NOT sudo")
	}
	dataDir, _, plistPath, _, err := darwinPaths()
	if err != nil {
		return err
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	if err := runCmd(w, "launchctl", "bootout", domain+"/"+launchdLabel); err != nil {
		fmt.Fprintf(w, "note: launchctl bootout returned: %v (continuing)\n", err)
	}
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", plistPath, err)
	}
	fmt.Fprintf(w, "\nRemoved %s.\n", plistPath)
	fmt.Fprintf(w, "Left in place: data dir %s (delete it by hand to purge).\n", dataDir)
	return nil
}

// Status prints `launchctl print` for the agent.
func Status(w io.Writer) error {
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = runCmd(w, "launchctl", "print", domain+"/"+launchdLabel)
	return nil
}
