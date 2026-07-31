package docker

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Editing and redeploying a CLI-discovered stack.
//
// A stack the app didn't create has its compose file on the HOST — the app only
// learns the path from the container labels. So "edit and redeploy" here means
// editing the file where it already lives and running compose against it in its
// original working directory, NOT copying it into a managed project.
//
// That distinction is the whole design. A compose file's relative paths — bind
// mounts, `env_file`, `build.context`, `include` — resolve against the project's
// working directory. Move the file and every one of them silently means something
// else: `./nginx.conf` would stop being the operator's config and start being
// whatever happens to sit next to the copy (usually nothing, which deploys an
// empty file rather than failing). Editing in place keeps one source of truth and
// keeps every relative path pointing where the operator meant it to.
//
// The cost is that compose has to run where the file is: locally for the local
// daemon, and over SSH on the host itself for ssh hosts — unlike managed
// projects, where the CLI runs here and only tunnels the API. TCP hosts expose no
// filesystem at all, so they stay read-only, as they already were.

// dcBackupSuffix is appended to the compose file's name to keep the previous
// contents next to it. Deliberately not ".bak": that is a name operators use
// themselves, and clobbering their backup while claiming to make one would be
// worse than making none.
const dcBackupSuffix = ".dc-prev"

// StackCompose reads a stack's compose file and reports whether the app can write
// it back, with the reason when it can't — the UI needs the reason to explain why
// an editor isn't on offer rather than silently omitting it.
//
// Read and editability come from one call because resolving a stack costs a
// container list plus a file read, and over SSH that is a network round trip
// each. Answering both from a single resolution keeps opening the viewer to one.
func (m *Manager) StackCompose(ctx context.Context, hostID int64, project string) (path, content string, editable bool, reason string, err error) {
	t, err := m.resolveStack(ctx, hostID, project)
	if err != nil {
		return "", "", false, "", err
	}
	reason = t.editableReason()
	return t.path, t.content, reason == "", reason, nil
}

// editableReason returns why this stack can't be edited in place, or "".
//
// It answers for the transport (can we reach the host's filesystem at all?) AND
// for the destination (is the compose file somewhere we are willing to act on?).
// Keeping both here is deliberate: every entry point calls this, so the
// containment rule can't be enforced on one operation and forgotten on another,
// and StackEditable can't advertise an editor whose save would be refused.
func (t *stackTarget) editableReason() string {
	switch t.host.Kind {
	case "local", "", "ssh":
	default:
		return fmt.Sprintf("the compose file lives on the host and a %s connection exposes no filesystem — edit it on the host itself", t.host.Kind)
	}
	if t.workDir == "" {
		return "this stack records no working directory (no com.docker.compose.project.working_dir label), so there is nothing to scope an edit to"
	}
	if err := t.checkContained(); err != nil {
		return err.Error()
	}
	return ""
}

// StackWriteComposeFile replaces a CLI-discovered stack's compose file with
// content, after checking that the new file is one compose will accept. It
// returns the path written.
//
// The previous contents are kept alongside as <name><dcBackupSuffix>. Nothing is
// written until the replacement validates, and the replacement is moved into
// place with a rename, so an interrupted write cannot leave a half-file where a
// running stack's definition used to be.
func (m *Manager) StackWriteComposeFile(ctx context.Context, hostID int64, project, content string) (string, error) {
	t, err := m.resolveStack(ctx, hostID, project)
	if err != nil {
		return "", err
	}
	if reason := t.editableReason(); reason != "" {
		return "", fmt.Errorf("%s", reason)
	}
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("refusing to write an empty compose file — use Remove if you want the stack gone")
	}
	if len(content) > maxComposeBytes {
		return "", fmt.Errorf("compose file is too large (%d bytes, limit %d)", len(content), maxComposeBytes)
	}

	switch t.host.Kind {
	case "local", "":
		return t.path, t.writeLocal(ctx, content)
	case "ssh":
		return t.path, m.writeOverSSH(ctx, t, content)
	}
	return "", fmt.Errorf("unsupported host kind %q", t.host.Kind)
}

// checkContained refuses a compose path that isn't inside the stack's working
// directory.
//
// The path comes from a container LABEL, so it is chosen by whoever started the
// container rather than by the caller. Without this rule, a label naming
// /etc/cron.d/anything.yml would let the editor write there, and a redeploy would
// `compose up` whatever it found — both as the account the app runs under.
//
// Scope it honestly: setting those labels needs direct Docker API access, since
// the app's own container-create surface exposes no Labels field, and Docker API
// access is already root-equivalent on that host. So this is defence in depth
// bounding what a label can steer, not a barrier against a privilege escalation
// reachable through Docker Commander itself. It is cheap, it makes the blast
// radius of a compromised daemon smaller, and it keeps an edit inside the project
// it belongs to — which is worth having on its own.
func (t *stackTarget) checkContained() error {
	switch t.host.Kind {
	case "local", "":
		root, err := canonicalDir(t.workDir)
		if err != nil {
			return fmt.Errorf("working directory %q is not readable: %w", t.workDir, err)
		}
		if _, ok := relWithin(root, t.path); !ok {
			return fmt.Errorf("the compose file (%s) is outside the stack's working directory (%s), so editing it here is refused", t.path, t.workDir)
		}
		return nil
	default:
		// Remote paths can't be canonicalised from here, so this is a lexical
		// check only: it stops `../` traversal but cannot see a symlink on the
		// host. Combined with the .yml/.yaml suffix rule and the fact that the
		// path had to be readable to get here, that is the containment available
		// without shipping a helper binary to the host.
		if !remoteWithin(t.workDir, t.path) {
			return fmt.Errorf("the compose file (%s) is outside the stack's working directory (%s), so editing it here is refused", t.path, t.workDir)
		}
		return nil
	}
}

// remoteWithin reports whether p is root or sits under it, comparing cleaned
// slash paths without touching the local filesystem (the paths are on another
// machine). Remote hosts are POSIX — an ssh:// Docker host runs a Unix daemon —
// so slash separators are the right assumption here.
func remoteWithin(root, p string) bool {
	if root == "" || p == "" {
		return false
	}
	cleanRoot := path.Clean(root)
	cleanP := path.Clean(p)
	if !strings.HasPrefix(cleanRoot, "/") || !strings.HasPrefix(cleanP, "/") {
		return false // both must be absolute for the comparison to mean anything
	}
	if cleanP == cleanRoot {
		return true
	}
	return strings.HasPrefix(cleanP, strings.TrimSuffix(cleanRoot, "/")+"/")
}

// writeLocal validates then atomically replaces the compose file on this machine.
func (t *stackTarget) writeLocal(ctx context.Context, content string) error {
	dir := filepath.Dir(t.path)
	// The temp file is a sibling so that (a) rename is atomic — same filesystem —
	// and (b) compose resolves the same relative paths while validating it.
	tmp, err := os.CreateTemp(dir, ".dc-compose-*.yml")
	if err != nil {
		return fmt.Errorf("cannot write next to the compose file (%s): %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed away

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Match the file we're replacing rather than the temp file's 0600, so an
	// edit doesn't quietly change who can read the stack's definition.
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(t.path); err == nil {
		mode = fi.Mode().Perm()
		_ = os.Chmod(tmpName, mode)
	}

	if out, err := runComposeFiles(ctx, t.workDir, t.stack.Project, nil, []string{tmpName}, "config", "--quiet"); err != nil {
		return fmt.Errorf("compose rejected the edited file, so nothing was changed:\n%s", strings.TrimSpace(out))
	}
	if err := writeSiblingAtomic(t.path+dcBackupSuffix, t.content, mode); err != nil {
		return fmt.Errorf("could not save the previous version, so the edit was not applied: %w", err)
	}
	// Rename rather than write: it replaces a symlink instead of following it,
	// and it is atomic, so a reader never sees a half-written definition.
	return os.Rename(tmpName, t.path)
}

// writeSiblingAtomic writes content to path via a temp file in the same
// directory plus a rename.
//
// The rename is the security-relevant part. os.WriteFile opens the destination
// with O_CREAT|O_WRONLY|O_TRUNC and therefore FOLLOWS a symlink sitting at that
// path: anyone who can create files in the stack's directory could pre-place
// `compose.yml.dc-prev` as a link to, say, /etc/cron.d/x and have this process —
// often root — write through it. They control the content too, since the backup
// is the previous compose file and they can write that as well. Rename replaces
// the link itself, so the write lands where we intended and nowhere else.
func writeSiblingAtomic(path, content string, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".dc-tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once renamed away

	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// writeOverSSH does the same on an ssh host, in a single shell script fed the new
// contents on stdin.
//
// One script rather than a command per step, because the safe version needs state
// (the temp file names) to survive between steps, and each SSH session is
// independent. It also cuts three round trips to one.
//
// The shape mirrors writeLocal for the same reason: `cat > file` and `cp src dst`
// both FOLLOW a symlink at the destination, so writing straight to a predictable
// name like `compose.yml.dc-new` would let anyone who can create files in that
// directory redirect the write. `mktemp` creates with O_EXCL — it cannot open a
// pre-placed link — and `mv` replaces a symlink instead of following it.
//
// `cp -p` onto the temp file before `cat` is a portable way to carry the
// original's permissions across: the copy brings the mode, then stdin replaces
// the contents without touching it. Avoids `chmod --reference`, which busybox
// hosts don't have.
func (m *Manager) writeOverSSH(ctx context.Context, t *stackTarget, content string) error {
	q := shellQuote
	dir := path.Dir(t.path)

	script := strings.Join([]string{
		"set -e",
		fmt.Sprintf("tmp=$(mktemp %s)", q(dir+"/.dc-compose-XXXXXX")),
		fmt.Sprintf("bak=$(mktemp %s)", q(dir+"/.dc-prev-XXXXXX")),
		`trap 'rm -f "$tmp" "$bak"' EXIT`,
		fmt.Sprintf("cp -p %s \"$tmp\"", q(t.path)),
		fmt.Sprintf("cp -p %s \"$bak\"", q(t.path)),
		`cat > "$tmp"`,
		fmt.Sprintf("cd %s", q(t.workDir)),
		fmt.Sprintf("docker compose -p %s -f \"$tmp\" config --quiet", q(t.stack.Project)),
		fmt.Sprintf("mv \"$bak\" %s", q(t.path+dcBackupSuffix)),
		fmt.Sprintf("mv \"$tmp\" %s", q(t.path)),
	}, "\n")

	if out, err := m.sshRunStdin(ctx, t, script, content); err != nil {
		return fmt.Errorf("the edit was not applied on the host: %w\n%s", err, strings.TrimSpace(out))
	}
	return nil
}

// StackRedeploy runs `docker compose up -d --build` for a CLI-discovered stack,
// in the working directory it was originally deployed from, so relative paths in
// the file resolve exactly as they did the first time. It returns the CLI output.
//
// `--build` for the same reason project deploys pass it: `up` builds a service
// only when its image is MISSING, so a stack declaring `build:` would keep
// running the image from its first deploy no matter what changed in the
// Dockerfile or its context, while the CLI reported "Container Running". It is a
// no-op for services that only pull an image.
//
// `--remove-orphans` is deliberately NOT passed: a redeploy after an edit should
// not silently delete containers, and compose warns about orphans in the output
// the caller displays, so removing one stays an explicit choice.
func (m *Manager) StackRedeploy(ctx context.Context, hostID int64, project string) (string, error) {
	t, err := m.resolveStack(ctx, hostID, project)
	if err != nil {
		return "", err
	}
	if reason := t.editableReason(); reason != "" {
		return "", fmt.Errorf("%s", reason)
	}

	switch t.host.Kind {
	case "local", "":
		out, err := runComposeFiles(ctx, t.workDir, t.stack.Project, nil,
			[]string{t.path}, "up", "-d", "--build")
		return out, err
	case "ssh":
		q := shellQuote
		return m.sshRun(ctx, t, fmt.Sprintf(
			"cd %s && docker compose -p %s -f %s up -d --build",
			q(t.workDir), q(t.stack.Project), q(t.path)))
	}
	return "", fmt.Errorf("unsupported host kind %q", t.host.Kind)
}

// sshRun runs a command on the stack's host and returns its combined output.
func (m *Manager) sshRun(ctx context.Context, t *stackTarget, cmd string) (string, error) {
	return m.sshRunStdin(ctx, t, cmd, "")
}

// sshRunStdin runs a command on the stack's host, optionally feeding it stdin.
//
// It is bounded by composeTimeout — the same ceiling the local CLI path gets —
// because `up -d` on the host legitimately waits for image pulls, builds and
// healthchecks, but must not wait forever.
func (m *Manager) sshRunStdin(ctx context.Context, t *stackTarget, cmd, stdin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, composeTimeout)
	defer cancel()

	cli, err := m.sshClientFor(t.hostID, t.host)
	if err != nil {
		return "", err
	}
	sess, err := cli.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	if stdin != "" {
		sess.Stdin = strings.NewReader(stdin)
	}

	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := sess.CombinedOutput(cmd)
		done <- result{out, err}
	}()

	select {
	case r := <-done:
		return string(r.out), r.err
	case <-ctx.Done():
		// Closing the session unblocks the goroutine's read; without this a
		// compose pull that outlives the request would leak it for its lifetime.
		_ = sess.Close()
		return "", ctx.Err()
	}
}
