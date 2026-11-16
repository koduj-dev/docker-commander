package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/selfupdate"
)

// Diagnostics for Docker Commander itself: where and how it runs, and what that
// stops it from doing. The host diagnostics look at a Docker daemon; these look
// at this process: the install (can it update itself?), the systemd sandbox,
// its data directory, the local stacks' compose files, and the tools it shells
// out to. Every check is read-only: open, stat and access(2), nothing written.
//
// Admin only (the route sits under "instance", mapped to __admin): it names the
// binary, the service user, the data directory and local stack paths, which is
// about the installation, not about any one host a scoped user may hold.

// instanceDiagnostics is the response of POST /api/instance/diagnostics.
type instanceDiagnostics struct {
	GeneratedAt time.Time            `json:"generatedAt"`
	Checks      []docker.CheckResult `json:"checks"`
}

// dataDirCheckMax bounds how many paths the data directory check looks at.
const dataDirCheckMax = 500

// instanceEnv is everything the checks read about the process and its
// surroundings, gathered once so each check is a pure function of it.
type instanceEnv struct {
	version    string
	exe        string
	runAs      string // service user name, "" if unknown
	systemd    bool   // started by systemd (INVOCATION_ID is set)
	goos       string
	selfUpdate bool  // DC_SELF_UPDATE on
	canRestart bool  // in-place restart wired (not on Windows)
	precheck   error // selfupdate.Precheck's answer

	// Sandbox probes: what reading /home and writing /etc answer.
	homeErr error
	etcErr  error

	dataDir      string
	dataDirPaths []string // the data dir and the entries checked under it
	dataDirMore  bool     // dataDirPaths was cut at dataDirCheckMax
	writable     func(string) error
	writeCheck   bool // writable answers for real (not on Windows)

	hasLocal      bool
	localDisabled bool  // the local host is switched off in Hosts
	dockerErr     error // reaching the local daemon
	stacks        []docker.StackFileAccess
	stacksErr     error

	compose, buildx, trivy bool
}

func (s *Server) handleInstanceDiagnostics(w http.ResponseWriter, r *http.Request) {
	env := s.gatherInstanceEnv(r.Context())
	writeJSON(w, http.StatusOK, instanceDiagnostics{GeneratedAt: time.Now(), Checks: instanceChecks(env)})
	s.audit(r, "diagnostics.run", "instance", "")
}

func instanceChecks(e instanceEnv) []docker.CheckResult {
	return []docker.CheckResult{
		checkInstall(e),
		checkSandbox(e),
		checkDataDir(e),
		checkDockerAccess(e),
		checkStackFiles(e),
		checkTools(e),
	}
}

func (s *Server) gatherInstanceEnv(ctx context.Context) instanceEnv {
	e := instanceEnv{
		version:    s.cfg.Version,
		systemd:    os.Getenv("INVOCATION_ID") != "",
		goos:       runtime.GOOS,
		selfUpdate: s.update.selfUpdate,
		canRestart: s.onRestart != nil,
		precheck:   s.update.blocked(),
		dataDir:    s.cfg.DataDir,
		writable:   docker.PathWritable,
		writeCheck: docker.WriteCheckSupported,
		compose:    docker.ComposeAvailable(ctx),
		buildx:     docker.BuildCheckAvailable(ctx),
		trivy:      docker.TrivyAvailable(ctx),
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		e.exe = exe
	}
	if u, err := user.Current(); err == nil {
		e.runAs = u.Username
	}
	if e.goos == "linux" {
		_, e.homeErr = os.ReadDir("/home")
		e.etcErr = docker.PathWritable("/etc")
	}
	e.dataDirPaths, e.dataDirMore = dataDirPaths(e.dataDir)

	hosts, err := s.store.ListHosts(ctx)
	if err != nil {
		e.dockerErr = err
		return e
	}
	for _, h := range hosts {
		if h.Kind != "local" {
			continue
		}
		e.hasLocal = true
		// Switched off in Hosts (a remote-only install keeps the seeded local
		// host that way, often with no local daemon at all): not a failure, and
		// nothing to contact.
		if h.Disabled {
			e.localDisabled = true
			break
		}
		if _, err := s.docker.SystemInfo(ctx, h.ID); err != nil {
			e.dockerErr = err
			break
		}
		e.stacks, e.stacksErr = s.docker.LocalStackFileAccess(ctx, h.ID)
		break
	}
	return e
}

// dataDirPaths lists what the data directory check looks at: the directory,
// every entry directly in it (the database, keys, settings), and the
// per-project directories one level further down, where an edit, a deploy or a
// revision writes. A restore run as root leaves exactly these owned by root.
func dataDirPaths(dir string) (paths []string, more bool) {
	if dir == "" {
		return nil, false
	}
	paths = []string{dir}
	add := func(p string) bool {
		if len(paths) >= dataDirCheckMax {
			more = true
			return false
		}
		paths = append(paths, p)
		return true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return paths, false // the directory itself is checked and reported
	}
	for _, en := range entries {
		if !add(filepath.Join(dir, en.Name())) {
			return paths, more
		}
	}
	for _, sub := range []string{"projects", "project-revisions", "project-templates"} {
		children, err := os.ReadDir(filepath.Join(dir, sub))
		if err != nil {
			continue
		}
		for _, c := range children {
			if c.IsDir() && !add(filepath.Join(dir, sub, c.Name())) {
				return paths, more
			}
		}
	}
	return paths, more
}

func checkInstall(e instanceEnv) docker.CheckResult {
	r := docker.CheckResult{ID: "dc_install", Name: "Installation and updates", Status: docker.CheckOK}
	r.Details = append(r.Details, "Version: "+e.version)
	if e.exe != "" {
		r.Details = append(r.Details, "Binary: "+e.exe)
	}
	if e.runAs != "" {
		r.Details = append(r.Details, "Runs as: "+e.runAs)
	}
	var ctr *selfupdate.ErrInContainer
	switch {
	case errors.As(e.precheck, &ctr):
		r.Details = append(r.Details, "Started in: a container")
	case e.systemd:
		r.Details = append(r.Details, "Started by: systemd")
	}

	var pkg *selfupdate.ErrPackageManaged
	var nw *selfupdate.ErrNotWritable
	switch {
	case !e.selfUpdate:
		r.Message = "In-app updates are turned off (DC_SELF_UPDATE=0)."
	case !e.canRestart:
		r.Message = "In-app updates aren't offered on this platform. Update with dockercmd --self-upgrade, then restart the service."
	case e.precheck == nil:
		r.Message = "Update & restart can install new releases here."
	case errors.As(e.precheck, &pkg), errors.As(e.precheck, &ctr):
		reason, cmd := blockedReason(e.precheck)
		r.Message = reason
		r.Details = append(r.Details, "Update with: "+cmd)
	case errors.As(e.precheck, &nw):
		reason, cmd := blockedReason(e.precheck)
		r.Status = docker.CheckWarn
		r.Message = reason
		r.Details = append(r.Details, "Update with: "+cmd,
			"Or let the service write "+nw.Dir+" (systemd: add it to ReadWritePaths with a drop-in).")
	default:
		reason, _ := blockedReason(e.precheck)
		r.Status = docker.CheckWarn
		r.Message = reason
	}
	return r
}

func checkSandbox(e instanceEnv) docker.CheckResult {
	r := docker.CheckResult{ID: "dc_sandbox", Name: "Service sandbox", Status: docker.CheckOK}
	if e.goos != "linux" {
		r.Status, r.Message = docker.CheckSkipped, "The systemd sandbox only applies on Linux."
		return r
	}
	homeHidden := errors.Is(e.homeErr, fs.ErrPermission)
	readOnly := errors.Is(e.etcErr, syscall.EROFS)
	// Only under systemd are these the unit's ProtectHome/ProtectSystem, with a
	// drop-in as the fix. Elsewhere (a container, a read-only root) say what is
	// observed and leave the cause open.
	if e.systemd {
		if homeHidden {
			r.Details = append(r.Details, "/home, /root and /run/user are hidden from the service (systemd ProtectHome). Stacks and build contexts there can't be read.")
		}
		if readOnly {
			r.Details = append(r.Details, "The system is read-only for the service apart from its ReadWritePaths (systemd ProtectSystem=strict).")
		}
	} else {
		if homeHidden {
			r.Details = append(r.Details, "/home isn't readable for this process, so stacks and build contexts there can't be read.")
		}
		if readOnly {
			r.Details = append(r.Details, "The root filesystem is read-only for this process (for example a container started with a read-only filesystem).")
		}
	}
	switch {
	case len(r.Details) > 0 && e.systemd:
		r.Message = "Docker Commander runs sandboxed. Paths outside what the sandbox allows can't be read or written; the checks below name any that matter."
		r.Details = append(r.Details, "To allow a path, add a drop-in (sudo systemctl edit dockercmd), see docs/deployment.md.")
	case len(r.Details) > 0:
		r.Message = "Not a systemd service, but some paths are off limits to Docker Commander; the checks below name any that matter."
	case e.systemd:
		r.Message = "Runs under systemd with no filesystem sandbox detected."
	default:
		r.Message = "Not running as a systemd service, so no sandbox applies."
	}
	return r
}

func checkDataDir(e instanceEnv) docker.CheckResult {
	r := docker.CheckResult{ID: "dc_data_dir", Name: "Data directory", Status: docker.CheckOK}
	if e.dataDir == "" {
		r.Status, r.Message = docker.CheckSkipped, "No data directory is configured."
		return r
	}
	if !e.writeCheck {
		r.Status, r.Message = docker.CheckSkipped, "Write access can't be checked on this platform (Windows ACLs decide). A save that fails reports its own error."
		return r
	}
	var bad []string
	for _, p := range e.dataDirPaths {
		if err := e.writable(p); err != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", p, err))
		}
	}
	if len(bad) == 0 {
		r.Message = fmt.Sprintf("%s is writable (%s checked).", e.dataDir, plural(len(e.dataDirPaths), "path"))
		if e.dataDirMore {
			r.Message += fmt.Sprintf(" Stopped after the first %d.", dataDirCheckMax)
		}
		return r
	}
	r.Status = docker.CheckFail
	r.Message = fmt.Sprintf("%d of %d paths in %s aren't writable. Saving settings, projects or revisions there fails.", len(bad), len(e.dataDirPaths), e.dataDir)
	r.Details = bad
	if e.runAs != "" && e.goos != "windows" {
		r.Details = append(r.Details, fmt.Sprintf("Usually files left owned by root, e.g. after a restore run with sudo. Fix: sudo chown -R %s: %s", e.runAs, e.dataDir))
	}
	return r
}

func checkDockerAccess(e instanceEnv) docker.CheckResult {
	r := docker.CheckResult{ID: "dc_docker", Name: "Local Docker access", Status: docker.CheckOK}
	switch {
	case !e.hasLocal && e.dockerErr != nil:
		r.Status, r.Message = docker.CheckSkipped, "Could not read the host list: "+e.dockerErr.Error()
	case !e.hasLocal:
		r.Status, r.Message = docker.CheckSkipped, "No local Docker host is configured."
	case e.localDisabled:
		r.Status, r.Message = docker.CheckSkipped, "The local Docker host is disabled in Hosts."
	case e.dockerErr == nil:
		r.Message = "Connected to the local Docker daemon."
	case strings.Contains(strings.ToLower(e.dockerErr.Error()), "permission denied"):
		r.Status = docker.CheckFail
		who := "Docker Commander"
		if e.runAs != "" {
			who += fmt.Sprintf(" (user %q)", e.runAs)
		}
		r.Message = who + " isn't allowed to use the Docker socket."
		r.Details = []string{e.dockerErr.Error(), "Add the user to the docker group. The shipped systemd unit does this with SupplementaryGroups=docker."}
	default:
		r.Status = docker.CheckFail
		r.Message = "Can't reach the local Docker daemon."
		r.Details = []string{e.dockerErr.Error(), "Check that Docker is running, and DOCKER_HOST if the socket isn't at the default path."}
	}
	return r
}

func checkStackFiles(e instanceEnv) docker.CheckResult {
	r := docker.CheckResult{ID: "dc_stack_files", Name: "Local stack compose files", Status: docker.CheckOK}
	switch {
	case !e.hasLocal:
		r.Status, r.Message = docker.CheckSkipped, "No local Docker host is configured. Stacks on SSH hosts are checked when opened."
		return r
	case e.localDisabled:
		r.Status, r.Message = docker.CheckSkipped, "The local Docker host is disabled in Hosts."
		return r
	case e.dockerErr != nil:
		r.Status, r.Message = docker.CheckSkipped, "The local Docker daemon isn't reachable, so its stacks can't be listed."
		return r
	case e.stacksErr != nil:
		r.Status, r.Message = docker.CheckSkipped, "Could not list stacks: "+e.stacksErr.Error()
		return r
	case len(e.stacks) == 0:
		r.Message = "No stacks with a compose file on the local host."
		return r
	}
	stacks := append([]docker.StackFileAccess(nil), e.stacks...)
	sort.Slice(stacks, func(i, j int) bool { return stacks[i].Project < stacks[j].Project })
	var unreadable, readOnly []string
	for _, a := range stacks {
		switch {
		case !a.Readable:
			unreadable = append(unreadable, a.Project+": "+a.Reason)
		case !a.Writable && e.writeCheck:
			readOnly = append(readOnly, a.Project+" (read-only): "+a.Reason)
		}
	}
	n := len(stacks)
	switch {
	case len(unreadable) > 0:
		r.Status = docker.CheckWarn
		r.Message = fmt.Sprintf("%d of %d stacks have a compose file Docker Commander can't read: they can't be viewed, edited or redeployed here.", len(unreadable), n)
	case len(readOnly) > 0:
		r.Message = fmt.Sprintf("All %d compose files are readable; %d open read-only (no Save, Redeploy still works).", n, len(readOnly))
	case !e.writeCheck:
		r.Message = fmt.Sprintf("All %d compose files are readable. Write access can't be checked on this platform.", n)
	default:
		r.Message = fmt.Sprintf("All %d compose files are readable and writable.", n)
	}
	r.Details = append(unreadable, readOnly...)
	return r
}

func checkTools(e instanceEnv) docker.CheckResult {
	r := docker.CheckResult{ID: "dc_tools", Name: "Command-line tools", Status: docker.CheckOK}
	state := func(ok bool) string {
		if ok {
			return "available"
		}
		return "not found"
	}
	r.Details = []string{
		"docker compose (Projects, stack redeploy): " + state(e.compose),
		"docker buildx (Dockerfile checks): " + state(e.buildx),
		"trivy (image Scan, optional): " + state(e.trivy),
		"Each tool is looked up once per process: restart Docker Commander after installing one.",
	}
	if !e.compose {
		r.Status = docker.CheckWarn
		r.Message = "docker compose isn't available: Projects can't be deployed and stacks can't be redeployed."
		return r
	}
	r.Message = "docker compose is available."
	return r
}

// plural formats n with noun, adding "s" unless n is 1.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
