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

// StackEditable reports whether the app can write this stack's compose file back
// to its host, and why not when it can't. The UI asks before offering an editor,
// so the answer has to explain itself rather than just being false.
func (m *Manager) StackEditable(ctx context.Context, hostID int64, project string) (bool, string, error) {
	t, err := m.resolveStack(ctx, hostID, project)
	if err != nil {
		return false, "", err
	}
	if reason := t.editableReason(); reason != "" {
		return false, reason, nil
	}
	return true, "", nil
}

// editableReason returns why this stack can't be edited in place, or "".
func (t *stackTarget) editableReason() string {
	switch t.host.Kind {
	case "local", "", "ssh":
	default:
		return fmt.Sprintf("the compose file lives on the host and a %s connection exposes no filesystem — edit it on the host itself", t.host.Kind)
	}
	if t.workDir == "" {
		return "this stack records no working directory (no com.docker.compose.project.working_dir label), so there is nothing to scope an edit to"
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
	if err := t.checkContained(); err != nil {
		return "", err
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
// The path comes from a container LABEL, which is set by whoever started the
// container. Without this, anyone able to run a container could name
// /etc/cron.d/anything.yml as their stack's compose file and then use the editor
// to write attacker-chosen bytes there as the app's user — turning a "stacks"
// grant into arbitrary file write. Requiring the file to sit under the project's
// own working directory keeps an edit inside the project it belongs to.
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
	if fi, err := os.Stat(t.path); err == nil {
		_ = os.Chmod(tmpName, fi.Mode().Perm())
	}

	if out, err := runComposeFiles(ctx, t.workDir, t.stack.Project, nil, []string{tmpName}, "config", "--quiet"); err != nil {
		return fmt.Errorf("compose rejected the edited file, so nothing was changed:\n%s", strings.TrimSpace(out))
	}
	if err := os.WriteFile(t.path+dcBackupSuffix, []byte(t.content), 0o600); err != nil {
		return fmt.Errorf("could not save the previous version, so the edit was not applied: %w", err)
	}
	return os.Rename(tmpName, t.path)
}

// writeOverSSH does the same on an ssh host, using a shell one-liner per step so
// each failure can be reported on its own.
func (m *Manager) writeOverSSH(ctx context.Context, t *stackTarget, content string) error {
	tmp := t.path + ".dc-new"
	q := shellQuote

	// Write via stdin rather than embedding the content in the command line:
	// compose files are far larger than ARG_MAX-safe and contain arbitrary bytes.
	if out, err := m.sshRunStdin(ctx, t, fmt.Sprintf("cat > %s", q(tmp)), content); err != nil {
		return fmt.Errorf("could not write to %s on the host: %w\n%s", tmp, err, strings.TrimSpace(out))
	}
	if out, err := m.sshRun(ctx, t, fmt.Sprintf(
		"cd %s && docker compose -p %s -f %s config --quiet",
		q(t.workDir), q(t.stack.Project), q(tmp))); err != nil {
		_, _ = m.sshRun(ctx, t, fmt.Sprintf("rm -f %s", q(tmp)))
		return fmt.Errorf("compose on the host rejected the edited file, so nothing was changed:\n%s", strings.TrimSpace(out))
	}
	if out, err := m.sshRun(ctx, t, fmt.Sprintf(
		"cp -p %s %s && mv %s %s",
		q(t.path), q(t.path+dcBackupSuffix), q(tmp), q(t.path))); err != nil {
		_, _ = m.sshRun(ctx, t, fmt.Sprintf("rm -f %s", q(tmp)))
		return fmt.Errorf("could not replace the compose file on the host: %w\n%s", err, strings.TrimSpace(out))
	}
	return nil
}

// StackRedeploy runs `docker compose up -d` for a CLI-discovered stack, in the
// working directory it was originally deployed from, so relative paths in the
// file resolve exactly as they did the first time. It returns the CLI output.
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
			[]string{t.path}, "up", "-d")
		return out, err
	case "ssh":
		q := shellQuote
		return m.sshRun(ctx, t, fmt.Sprintf(
			"cd %s && docker compose -p %s -f %s up -d",
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
