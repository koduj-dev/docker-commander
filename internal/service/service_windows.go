//go:build windows

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

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

	installDir := winInstallDir()
	dataDir := winDataDir()
	binDest := filepath.Join(installDir, "dockercmd.exe")

	self, err := selfPath()
	if err != nil {
		return fmt.Errorf("locate running binary: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
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
	// process still has it open fails with a sharing violation.
	existing, openErr := m.OpenService(winServiceName)
	if openErr == nil {
		_, _ = existing.Control(svc.Stop)
		waitStopped(existing, 15*time.Second)
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
	waitStopped(s, 15*time.Second)

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

// waitStopped polls the service until it reports Stopped or the timeout
// elapses. Delete() succeeds regardless, but giving a just-signalled Stop a
// moment to land avoids racing the just-copied binary with a process that
// still has it open on the next Install.
func waitStopped(s *mgr.Service, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil || st.State == svc.Stopped {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
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
