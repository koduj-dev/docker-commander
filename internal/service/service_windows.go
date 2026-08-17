//go:build windows

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// winServiceName is both the SCM service name and the name Run/IsWindowsService
// look for; winDisplayName/winDescription only affect how it shows up in
// services.msc.
const (
	winServiceName = "dockercmd"
	winDisplayName = "Docker Commander"
	winDescription = "Monitors and controls Docker containers. https://github.com/koduj-dev/docker-commander"
	// scheduledTaskName must match deploy/install-windows.ps1's $TaskName —
	// the older, dependency-free installer this one exists alongside.
	scheduledTaskName = "DockerCommander"
)

// winInstallDir mirrors deploy/install-windows.ps1's $InstallDir so the two
// installers (native service vs. Scheduled Task) don't fight over different
// locations if someone switches between them.
func winInstallDir() string {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	return filepath.Join(pf, "docker-commander")
}

// winDataDir mirrors deploy/install-windows.ps1's $DataDir.
func winDataDir() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, "docker-commander", "data")
}

// isElevated reports whether the current process token has admin rights —
// creating/removing a service needs SC_MANAGER_ALL_ACCESS, which an
// unelevated token doesn't get even for a local administrator account (UAC).
func isElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// Install registers dockercmd as a native Windows service with the Service
// Control Manager (SCM), so it starts at boot, is supervised/restarted by the
// SCM on failure, and responds correctly to Stop instead of the SCM giving up
// with error 1053 (a plain console exe never answers the SCM handshake).
// Requires an elevated (Administrator) prompt.
func Install(w io.Writer) error {
	if !isElevated() {
		return errors.New("installing the Windows service needs an elevated (Administrator) PowerShell/cmd — right-click, \"Run as administrator\"")
	}
	if exists, err := scheduledTaskExists(scheduledTaskName); err != nil {
		fmt.Fprintf(w, "note: could not check for a conflicting Scheduled Task (%v), continuing\n", err)
	} else if exists {
		return conflictingScheduledTaskError()
	}

	installDir := winInstallDir()
	dataDir := winDataDir()
	binDest := filepath.Join(installDir, "dockercmd.exe")

	self, err := selfPath()
	if err != nil {
		return fmt.Errorf("locate running binary: %w", err)
	}
	preexisting := dirExists(dataDir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if preexisting {
		// A dir left over from a prior install (or, worse, planted by
		// someone else under %ProgramData% before this run) is not trusted
		// just because it already has the right path — its ACL might already
		// grant more than SYSTEM+Administrators.
		if err := verifyDataDirACL(dataDir); err != nil {
			return fmt.Errorf("refusing to reuse existing data dir %s: %w", dataDir, err)
		}
	}
	if err := secureDataDirACL(dataDir); err != nil {
		return fmt.Errorf("set data dir permissions: %w", err)
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service control manager: %w", err)
	}
	defer m.Disconnect()

	// A re-run of --install-service (e.g. after an upgrade, or pointing at a
	// new data dir) reconfigures rather than erroring on "already exists".
	// Stop it — and wait for Stopped — BEFORE touching binDest: that's very
	// likely the running service's own executable, and Windows keeps a
	// mapped-for-execution file locked, so overwriting it while the old
	// process still has it open fails with a sharing violation. Any failure
	// here — the stop request itself, or the wait for Stopped — aborts
	// before binDest is touched, rather than optimistically proceeding: the
	// whole point of stopping first is that overwriting a locked binary
	// fails ugly, not that it's merely unlikely to happen.
	existing, openErr := m.OpenService(winServiceName)
	if openErr == nil {
		if _, err := existing.Control(svc.Stop); err != nil && err != windows.ERROR_SERVICE_NOT_ACTIVE {
			existing.Close()
			return fmt.Errorf("stop existing service before reinstall: %w", err)
		}
		if err := waitStopped(existing.Query, 15*time.Second); err != nil {
			existing.Close()
			return fmt.Errorf("existing service did not stop cleanly, aborting reinstall (binary/service left untouched): %w", err)
		}
	}

	if !strings.EqualFold(self, binDest) {
		if err := copyFile(self, binDest, 0o755); err != nil {
			if openErr == nil {
				existing.Close()
			}
			return fmt.Errorf("install binary to %s: %w", binDest, err)
		}
		fmt.Fprintf(w, "Installed binary -> %s\n", binDest)
	}

	if openErr == nil {
		delErr := existing.Delete()
		existing.Close()
		if delErr != nil {
			return fmt.Errorf("remove existing service before reinstall: %w", delErr)
		}
		// Delete() marks the service for deletion; the SCM only actually
		// drops the name once every open handle to it is released (ours,
		// but also e.g. an open services.msc). Racing CreateService against
		// that teardown fails with ERROR_SERVICE_MARKED_FOR_DELETE, so wait
		// for the name to genuinely disappear instead of assuming Delete()
		// was synchronous.
		if err := waitServiceGone(m, winServiceName, 10*time.Second); err != nil {
			return err
		}
	}

	s, err := m.CreateService(winServiceName, binDest, mgr.Config{
		DisplayName:      winDisplayName,
		Description:      winDescription,
		StartType:        mgr.StartAutomatic,
		ErrorControl:     mgr.ErrorNormal,
		DelayedAutoStart: true,
	}, "-data-dir", dataDir)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()
	fmt.Fprintf(w, "Registered Windows service %q\n", winServiceName)

	// Restart on crash: a few quick retries, then back off. This is the SCM
	// equivalent of systemd's Restart=on-failure in deploy/dockercmd.service.
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 86400); err != nil {
		fmt.Fprintf(w, "note: could not set recovery actions: %v\n", err)
	}

	if err := s.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}

	fmt.Fprintln(w, "\nService installed and started.")
	fmt.Fprintln(w, "   Status: dockercmd --service-status   (logs: Event Viewer -> Windows Logs -> Application)")
	fmt.Fprintf(w, "   Data dir: %s\n", dataDir)
	fmt.Fprintln(w, "   Listen address + TLS come from your config (DC_HOST/DC_PORT/DC_TLS_*). Then create the admin account in the UI.")
	return nil
}

// Uninstall stops and removes the Windows service. The data dir is left in
// place so reinstalling keeps the database and keys.
func Uninstall(w io.Writer) error {
	if !isElevated() {
		return errors.New("uninstalling the Windows service needs an elevated (Administrator) PowerShell/cmd")
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service control manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(winServiceName)
	if err != nil {
		return fmt.Errorf("service %q is not installed", winServiceName)
	}
	defer s.Close()

	if _, err := s.Control(svc.Stop); err != nil && err != windows.ERROR_SERVICE_NOT_ACTIVE {
		fmt.Fprintf(w, "note: stop returned: %v (continuing)\n", err)
	}
	if err := waitStopped(s.Query, 15*time.Second); err != nil {
		fmt.Fprintf(w, "note: %v (continuing with delete)\n", err)
	}

	if err := s.Delete(); err != nil {
		return fmt.Errorf("remove service: %w", err)
	}
	fmt.Fprintf(w, "\nRemoved Windows service %q.\n", winServiceName)
	fmt.Fprintf(w, "Left in place: %s and data dir %s (delete them by hand to purge).\n", winInstallDir(), winDataDir())
	return nil
}

// Status queries the SCM for the service's current state.
func Status(w io.Writer) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service control manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(winServiceName)
	if err != nil {
		fmt.Fprintf(w, "Service %q is not installed.\n", winServiceName)
		return nil
	}
	defer s.Close()

	st, err := s.Query()
	if err != nil {
		return fmt.Errorf("query service status: %w", err)
	}
	fmt.Fprintf(w, "Service %q: %s\n", winServiceName, svcStateString(st.State))
	if st.State == svc.Running && st.ProcessId != 0 {
		fmt.Fprintf(w, "  PID: %d\n", st.ProcessId)
	}
	fmt.Fprintln(w, "  Logs: Event Viewer -> Windows Logs -> Application (source \"dockercmd\")")
	return nil
}

// waitStopped polls query until it reports Stopped or timeout elapses,
// returning an error on a genuine timeout or if query itself keeps failing —
// callers that need to know the service actually stopped (Install, before
// overwriting the binary) must not treat either as silent success. query is
// normally a *mgr.Service's own Query method (a bound method value matches
// this signature); tests inject a fake so the polling logic is verifiable
// without a real SCM handle.
func waitStopped(query func() (svc.Status, error), timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		st, err := query()
		if err != nil {
			lastErr = err
		} else if st.State == svc.Stopped {
			return nil
		} else {
			lastErr = nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	if lastErr != nil {
		return fmt.Errorf("querying service status: %w", lastErr)
	}
	return fmt.Errorf("service did not reach Stopped within %s", timeout)
}

// dirExists reports whether path already exists (any type) — used to tell a
// freshly-created data dir from a pre-existing one, since os.MkdirAll returns
// nil either way.
func dirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// secureDataDirACL applies a protected (non-inherited) DACL to path granting
// FullControl to SYSTEM and Administrators only. The service normally runs as
// LocalSystem with no ServiceStartName set, and this directory holds the
// database, TLS private keys and an at-rest encryption key (docs/gotchas.md)
// — an inherited ACL from %ProgramData% that grants Users/Authenticated Users
// read access would expose all of it to any local account.
func secureDataDirACL(path string) error {
	systemSid, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("resolve SYSTEM sid: %w", err)
	}
	adminSid, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return fmt.Errorf("resolve Administrators sid: %w", err)
	}

	inherit := uint32(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
	entries := []windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inherit,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(systemSid),
			},
		},
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inherit,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(adminSid),
			},
		},
	}

	dacl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("build ACL: %w", err)
	}

	// PROTECTED_DACL_SECURITY_INFORMATION strips inherited ACEs (e.g. a
	// permissive one inherited from %ProgramData% itself) so only the two
	// explicit grants above apply.
	err = windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil,
	)
	if err != nil {
		return fmt.Errorf("apply ACL to %s: %w", path, err)
	}
	return nil
}

// verifyDataDirACL refuses a pre-existing data dir whose DACL grants access
// to anything beyond SYSTEM, Administrators, or CREATOR OWNER (an inherited
// default that doesn't itself widen access). Called before secureDataDirACL
// repairs a fresh/legitimate dir's ACL, so a dir that already grants broader
// access — planted, misconfigured, or left over from something else entirely
// — is surfaced for manual inspection instead of silently adopted and then
// "fixed" as if it had always been fine.
func verifyDataDirACL(path string) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read existing ACL: %w", err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read existing DACL: %w", err)
	}
	if acl == nil {
		return errors.New("existing data dir has no DACL (fully permissive) — remove it or repair its ACL by hand")
	}
	for i := uint16(0); i < acl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, uint32(i), &ace); err != nil {
			return fmt.Errorf("read ACE %d: %w", i, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue // deny/audit ACEs aren't a disclosure risk
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsWellKnown(windows.WinLocalSystemSid) &&
			!sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) &&
			!sid.IsWellKnown(windows.WinCreatorOwnerSid) {
			return fmt.Errorf("existing data dir grants access to unexpected SID %s — inspect it manually before reinstalling", sid.String())
		}
	}
	return nil
}

// conflictingScheduledTaskError is its own function so its exact wording is
// unit-testable without a real schtasks.exe.
func conflictingScheduledTaskError() error {
	return fmt.Errorf(
		"Scheduled Task %q is already installed (deploy/install-windows.ps1) — stop and remove it first "+
			"(Stop-ScheduledTask -TaskName %q; Unregister-ScheduledTask -TaskName %q -Confirm:$false) before "+
			"installing the native service, to avoid two copies of dockercmd running at once",
		scheduledTaskName, scheduledTaskName, scheduledTaskName)
}

// scheduledTaskExists shells out to schtasks.exe (rather than a Task
// Scheduler COM binding) since it's always present on Windows and a simple
// exit-code check is all that's needed: 0 if the task exists, non-zero if it
// doesn't.
func scheduledTaskExists(name string) (bool, error) {
	err := exec.Command("schtasks", "/Query", "/TN", name).Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil // schtasks' own "not found" signal
	}
	return false, err // schtasks.exe missing, PATH issue, etc — unknown, not "found"
}

// waitServiceGone polls until OpenService(name) fails (the SCM has actually
// dropped the name, not just marked it for deletion) or the timeout elapses,
// returning a clear error in the latter case instead of letting the caller's
// next CreateService fail with the far more cryptic
// ERROR_SERVICE_MARKED_FOR_DELETE.
func waitServiceGone(m *mgr.Mgr, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		s, err := m.OpenService(name)
		if err != nil {
			return nil
		}
		s.Close()
		if time.Now().After(deadline) {
			return fmt.Errorf("service %q is still marked for deletion after %s — close any open Services console/sc.exe handles on it and re-run --install-service", name, timeout)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func svcStateString(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start pending"
	case svc.StopPending:
		return "stop pending"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continue pending"
	case svc.PausePending:
		return "pause pending"
	case svc.Paused:
		return "paused"
	default:
		return fmt.Sprintf("unknown (%d)", s)
	}
}

// IsWindowsService reports whether the current process was started by the
// Service Control Manager (as opposed to an interactive console) — the signal
// cmd/dockercmd uses to pick RunWindowsService over the normal signal-driven
// foreground path. A detection error is treated as "no" (run in the console).
func IsWindowsService() bool {
	is, err := svc.IsWindowsService()
	return err == nil && is
}

// windowsHandler adapts an ordinary "run until ctx is cancelled" server
// function to svc.Handler, so the same server code that answers SIGTERM in
// the foreground/systemd/launchd path also answers the SCM here.
type windowsHandler struct {
	run func(ctx context.Context) error
}

// serviceStopTimeout bounds how long Execute waits for run() to return after
// asking it to shut down, before reporting Stopped to the SCM anyway — the SCM
// itself gives a service ~30s (WaitHint-dependent) before declaring it hung.
// A var (not const) so tests can shrink it instead of sleeping for real.
var serviceStopTimeout = 20 * time.Second

// Execute implements svc.Handler. It starts run() in the background, reports
// Running to the SCM, and translates Stop/Shutdown control requests into
// cancelling run()'s context — the same shutdown path the foreground binary
// gets from signal.NotifyContext(os.Interrupt, syscall.SIGTERM).
func (h windowsHandler) Execute(_ []string, r <-chan svc.ChangeRequest, statusCh chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	statusCh <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- h.run(ctx) }()

	statusCh <- svc.Status{State: svc.Running, Accepts: accepted}

	for {
		select {
		case err := <-done:
			// The server exited on its own (e.g. a fatal config error) instead
			// of being asked to stop.
			if err != nil {
				statusCh <- svc.Status{State: svc.Stopped}
				return false, 1
			}
			statusCh <- svc.Status{State: svc.Stopped}
			return false, 0

		case req := <-r:
			switch req.Cmd {
			case svc.Interrogate:
				statusCh <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				statusCh <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-done:
					statusCh <- svc.Status{State: svc.Stopped}
					return false, 0
				case <-time.After(serviceStopTimeout):
					// run() ignored cancellation. The process is about to exit
					// either way (the SCM itself considers a service hung around
					// this point), but this was not a clean stop — say so with a
					// service-specific non-zero code instead of reporting the
					// same Stopped/0 a graceful shutdown would, which would mask
					// the timeout in the Event Log/SCM history.
					statusCh <- svc.Status{State: svc.Stopped}
					return true, 1
				}
			default:
				// Unrecognized control (e.g. session/power events) — report the
				// current status back so the SCM doesn't consider it dropped.
				statusCh <- svc.Status{State: svc.Running, Accepts: accepted}
			}
		}
	}
}

// RunWindowsService runs `run` under SCM control until the SCM asks the
// service to stop, at which point run's context is cancelled. Call only when
// IsWindowsService() is true; it blocks for the lifetime of the service.
func RunWindowsService(run func(ctx context.Context) error) error {
	return svc.Run(winServiceName, windowsHandler{run: run})
}
