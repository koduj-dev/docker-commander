//go:build windows

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

// TestSvcStateString covers every named svc.State plus the fallback for a
// value the switch doesn't recognize, so a future new state doesn't silently
// print nothing.
func TestSvcStateString(t *testing.T) {
	cases := []struct {
		state svc.State
		want  string
	}{
		{svc.Stopped, "stopped"},
		{svc.StartPending, "start pending"},
		{svc.StopPending, "stop pending"},
		{svc.Running, "running"},
		{svc.ContinuePending, "continue pending"},
		{svc.PausePending, "pause pending"},
		{svc.Paused, "paused"},
		{svc.State(999), "unknown (999)"},
	}
	for _, c := range cases {
		if got := svcStateString(c.state); got != c.want {
			t.Errorf("svcStateString(%d) = %q, want %q", c.state, got, c.want)
		}
	}
}

// TestWinInstallDirDefault and TestWinDataDirDefault check the fallback used
// when ProgramFiles/ProgramData aren't set (shouldn't happen on real Windows,
// but the code has a documented default and it should actually be that path).
func TestWinInstallDirDefault(t *testing.T) {
	t.Setenv("ProgramFiles", "")
	want := `C:\Program Files\docker-commander`
	if got := winInstallDir(); got != want {
		t.Errorf("winInstallDir() = %q, want %q", got, want)
	}
}

func TestWinInstallDirRespectsEnv(t *testing.T) {
	t.Setenv("ProgramFiles", `D:\Apps`)
	want := `D:\Apps\docker-commander`
	if got := winInstallDir(); got != want {
		t.Errorf("winInstallDir() = %q, want %q", got, want)
	}
}

func TestWinDataDirDefault(t *testing.T) {
	t.Setenv("ProgramData", "")
	want := `C:\ProgramData\docker-commander\data`
	if got := winDataDir(); got != want {
		t.Errorf("winDataDir() = %q, want %q", got, want)
	}
}

func TestWinDataDirRespectsEnv(t *testing.T) {
	t.Setenv("ProgramData", `D:\Data`)
	want := `D:\Data\docker-commander\data`
	if got := winDataDir(); got != want {
		t.Errorf("winDataDir() = %q, want %q", got, want)
	}
}

// TestWindowsHandlerExecute_Stop drives the svc.Handler.Execute state machine
// the same way the real SCM would for a normal stop: it exercises the full
// Execute goroutine over fake channels (no real SCM/service needed), sends
// Stop, and checks both what Execute reports back to the SCM and that it
// actually cancelled the server's context — the whole point of this handler
// is bridging those two.
func TestWindowsHandlerExecute_Stop(t *testing.T) {
	cancelled := make(chan struct{})
	serverDone := make(chan struct{})
	h := windowsHandler{run: func(ctx context.Context) error {
		<-ctx.Done()
		close(cancelled)
		close(serverDone)
		return nil
	}}

	reqs := make(chan svc.ChangeRequest)
	statuses := make(chan svc.Status, 16)
	execDone := make(chan struct {
		svcSpecific bool
		exitCode    uint32
	}, 1)

	go func() {
		svcSpecific, exitCode := h.Execute(nil, reqs, statuses)
		execDone <- struct {
			svcSpecific bool
			exitCode    uint32
		}{svcSpecific, exitCode}
	}()

	mustRecvStatus(t, statuses, svc.StartPending)
	mustRecvStatus(t, statuses, svc.Running)

	select {
	case reqs <- svc.ChangeRequest{Cmd: svc.Stop}:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute never read the Stop request off the channel")
	}

	mustRecvStatus(t, statuses, svc.StopPending)

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not cancel run()'s context after Stop")
	}

	mustRecvStatus(t, statuses, svc.Stopped)

	select {
	case res := <-execDone:
		if res.exitCode != 0 {
			t.Errorf("Execute exit code = %d, want 0 for a clean stop", res.exitCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return after reporting Stopped")
	}
}

// TestWindowsHandlerExecute_Shutdown mirrors the Stop test for the Shutdown
// control (sent on system shutdown/restart, distinct from an operator Stop) —
// both must lead to the same graceful cancellation.
func TestWindowsHandlerExecute_Shutdown(t *testing.T) {
	cancelled := make(chan struct{})
	h := windowsHandler{run: func(ctx context.Context) error {
		<-ctx.Done()
		close(cancelled)
		return nil
	}}

	reqs := make(chan svc.ChangeRequest)
	statuses := make(chan svc.Status, 16)
	go h.Execute(nil, reqs, statuses)

	mustRecvStatus(t, statuses, svc.StartPending)
	mustRecvStatus(t, statuses, svc.Running)

	select {
	case reqs <- svc.ChangeRequest{Cmd: svc.Shutdown}:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute never read the Shutdown request off the channel")
	}

	mustRecvStatus(t, statuses, svc.StopPending)

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not cancel run()'s context after Shutdown")
	}
}

// TestWindowsHandlerExecute_ServerExitsOnError covers the case where run()
// returns on its own (e.g. a fatal config/listen error) instead of being
// asked to stop — Execute must still report Stopped and surface a non-zero
// exit code rather than hanging waiting for a Stop that will never come.
func TestWindowsHandlerExecute_ServerExitsOnError(t *testing.T) {
	h := windowsHandler{run: func(ctx context.Context) error {
		return errors.New("listen tcp: address already in use")
	}}

	reqs := make(chan svc.ChangeRequest)
	statuses := make(chan svc.Status, 16)
	execDone := make(chan uint32, 1)
	go func() {
		_, exitCode := h.Execute(nil, reqs, statuses)
		execDone <- exitCode
	}()

	mustRecvStatus(t, statuses, svc.StartPending)
	mustRecvStatus(t, statuses, svc.Running)
	mustRecvStatus(t, statuses, svc.Stopped)

	select {
	case ec := <-execDone:
		if ec == 0 {
			t.Error("Execute exit code = 0 after run() returned an error, want non-zero")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return after run() failed on its own")
	}
}

// TestWindowsHandlerExecute_Interrogate checks that an Interrogate request
// (the SCM polling for status) gets the current status echoed back and does
// NOT stop the service — only Stop/Shutdown may do that.
func TestWindowsHandlerExecute_Interrogate(t *testing.T) {
	h := windowsHandler{run: func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	}}

	reqs := make(chan svc.ChangeRequest)
	statuses := make(chan svc.Status, 16)
	go h.Execute(nil, reqs, statuses)

	mustRecvStatus(t, statuses, svc.StartPending)
	running := mustRecvStatus(t, statuses, svc.Running)

	select {
	case reqs <- svc.ChangeRequest{Cmd: svc.Interrogate, CurrentStatus: running}:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute never read the Interrogate request off the channel")
	}

	echoed := mustRecvStatus(t, statuses, svc.Running)
	if echoed.Accepts != running.Accepts {
		t.Errorf("Interrogate echo Accepts = %v, want %v", echoed.Accepts, running.Accepts)
	}

	// Still alive: a real Stop now must still work, proving Interrogate didn't
	// leave the handler in a stopped/broken state.
	select {
	case reqs <- svc.ChangeRequest{Cmd: svc.Stop}:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute stopped responding after Interrogate")
	}
	mustRecvStatus(t, statuses, svc.StopPending)
	mustRecvStatus(t, statuses, svc.Stopped)
}

// TestWindowsHandlerExecute_StopTimeout proves Execute doesn't hang forever
// waiting on a run() that ignores cancellation (e.g. stuck in a blocking
// syscall) — it must still report Stopped once serviceStopTimeout elapses,
// and must NOT report the same success (svcSpecific=false, exitCode=0) a
// graceful stop would: that would mask an unclean shutdown as a clean one in
// the Event Log/SCM history.
func TestWindowsHandlerExecute_StopTimeout(t *testing.T) {
	old := serviceStopTimeout
	serviceStopTimeout = 50 * time.Millisecond
	defer func() { serviceStopTimeout = old }()

	block := make(chan struct{})
	defer close(block) // release the goroutine after the test, don't leak it
	h := windowsHandler{run: func(ctx context.Context) error {
		<-block // never returns on ctx cancellation
		return nil
	}}

	reqs := make(chan svc.ChangeRequest)
	statuses := make(chan svc.Status, 16)
	execDone := make(chan struct {
		svcSpecific bool
		exitCode    uint32
	}, 1)
	go func() {
		svcSpecific, exitCode := h.Execute(nil, reqs, statuses)
		execDone <- struct {
			svcSpecific bool
			exitCode    uint32
		}{svcSpecific, exitCode}
	}()

	mustRecvStatus(t, statuses, svc.StartPending)
	mustRecvStatus(t, statuses, svc.Running)

	select {
	case reqs <- svc.ChangeRequest{Cmd: svc.Stop}:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute never read the Stop request off the channel")
	}

	mustRecvStatus(t, statuses, svc.StopPending)
	mustRecvStatus(t, statuses, svc.Stopped)

	select {
	case res := <-execDone:
		if !res.svcSpecific || res.exitCode == 0 {
			t.Errorf("Execute returned (svcSpecific=%v, exitCode=%d) after a stop timeout, want (true, non-zero) — a hung run() must not be reported the same as a clean stop", res.svcSpecific, res.exitCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute hung past serviceStopTimeout waiting for an unresponsive run()")
	}
}

// TestWaitStopped_ReachesStopped: the ordinary case — query starts pending
// and flips to Stopped before the timeout.
func TestWaitStopped_ReachesStopped(t *testing.T) {
	calls := 0
	query := func() (svc.Status, error) {
		calls++
		if calls < 3 {
			return svc.Status{State: svc.StopPending}, nil
		}
		return svc.Status{State: svc.Stopped}, nil
	}
	if err := waitStopped(query, time.Second); err != nil {
		t.Fatalf("waitStopped() = %v, want nil once query reports Stopped", err)
	}
}

// TestWaitStopped_QueryErrorIsReported proves a persistent Query() error is
// no longer treated as success (the bug: "err != nil || st.State ==
// svc.Stopped" returned on EITHER) — a caller deciding whether it's safe to
// overwrite the service binary needs to know the service's state was never
// actually confirmed, not have that silently read as "it stopped".
func TestWaitStopped_QueryErrorIsReported(t *testing.T) {
	persistent := errors.New("RPC server unavailable")
	query := func() (svc.Status, error) { return svc.Status{}, persistent }
	err := waitStopped(query, 100*time.Millisecond)
	if err == nil {
		t.Fatal("waitStopped() = nil, want an error — Query() never succeeded, let alone reported Stopped")
	}
	if !errors.Is(err, persistent) {
		t.Errorf("waitStopped() error = %v, want it to wrap the underlying Query error", err)
	}
}

// TestWaitStopped_TimeoutIsReported: query never errors and never reports
// Stopped — falling off the end of the loop must be an error, not a silent
// return, so a stuck service doesn't get treated as having stopped cleanly.
func TestWaitStopped_TimeoutIsReported(t *testing.T) {
	query := func() (svc.Status, error) { return svc.Status{State: svc.StopPending}, nil }
	err := waitStopped(query, 100*time.Millisecond)
	if err == nil {
		t.Fatal("waitStopped() = nil after a real timeout, want an error")
	}
}

// TestWaitStopped_RecoversFromTransientError: a transient Query blip
// followed by a real Stopped must still succeed — proving the fix doesn't
// overcorrect into failing on any single hiccup.
func TestWaitStopped_RecoversFromTransientError(t *testing.T) {
	calls := 0
	query := func() (svc.Status, error) {
		calls++
		if calls == 1 {
			return svc.Status{}, errors.New("transient RPC blip")
		}
		return svc.Status{State: svc.Stopped}, nil
	}
	if err := waitStopped(query, time.Second); err != nil {
		t.Fatalf("waitStopped() = %v, want nil — a single transient error before a real Stopped should not fail the call", err)
	}
}

// TestConflictingScheduledTaskErrorNamesTheTask proves the abort message
// actually names the Scheduled Task and both PowerShell commands an operator
// needs — this message is the whole point of the DC-COR-004 fix, so its
// content is worth pinning down directly, not just its non-nil-ness.
func TestConflictingScheduledTaskErrorNamesTheTask(t *testing.T) {
	err := conflictingScheduledTaskError()
	for _, want := range []string{scheduledTaskName, "Stop-ScheduledTask", "Unregister-ScheduledTask"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("conflictingScheduledTaskError() = %q, want it to mention %q", err.Error(), want)
		}
	}
}

func mustRecvStatus(t *testing.T, ch <-chan svc.Status, want svc.State) svc.Status {
	t.Helper()
	select {
	case s := <-ch:
		if s.State != want {
			t.Fatalf("got status %v, want state %v", s, want)
		}
		return s
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for status %v", want)
		return svc.Status{}
	}
}
