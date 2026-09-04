package api

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// Deployment revisions: an immutable history of every successful project
// deploy, so a bad change can be rolled back rather than hand-fixed. Each
// revision's metadata lives in the store; the actual files (compose +
// sidecars, exactly as they were) live on disk as a zip — see the schema
// comment in store.go for why. Diff and restore-preview reuse the exact same
// state-diff engine as the deploy preview (internal/docker/preview.go +
// deployfields.go): a revision's files parse to []ServiceSpec the same way a
// live project's do, so BuildDeployPreview/ExtendServiceComparison compare
// two revisions, or a revision against what's running, without new code.

// projectRevisionsDir is where a project's revision file snapshots live.
func (s *Server) projectRevisionsDir(projectID int64) string {
	return filepath.Join(s.cfg.DataDir, "project-revisions", strconv.FormatInt(projectID, 10))
}

func (s *Server) revisionZipPath(projectID int64, revision int) string {
	return filepath.Join(s.projectRevisionsDir(projectID), strconv.Itoa(revision)+".zip")
}

// captureRevision records a successful deploy: metadata in the store, and a
// zip snapshot of the project directory on disk. Errors are logged, never
// surfaced — the deploy itself already succeeded, and a revision-capture
// hiccup shouldn't turn a successful deploy into a user-facing failure (same
// stance as the "last deployed profiles" persistence next to this call).
//
// The row is inserted Valid=false and only flipped to true by
// SetRevisionValid once the snapshot has actually been written to a temp
// file and renamed into its final path — so a reader (in particular restore)
// never sees "valid" revision metadata whose file doesn't exist yet, or
// never ends up existing at all. A capture failure leaves the row in place,
// marked invalid with the reason, rather than silently claiming success or
// disappearing.
func (s *Server) captureRevision(ctx context.Context, p *store.Project, profiles []string, output, reason, author string) {
	rev := &store.ProjectRevision{
		ProjectID: p.ID, HostID: p.HostID, Profiles: profiles,
		Images: s.captureRevisionImages(ctx, p), Valid: false,
		ValidationError: "snapshot capture in progress",
		Output:          output, Author: author, Reason: reason,
	}
	if _, err := s.store.CreateRevision(ctx, rev); err != nil {
		log.Printf("project revision: record deploy for %q: %v", p.Slug, err)
		return
	}
	// A redeploy supersedes whatever drift was previously reviewed and
	// accepted: don't let an old ignore silently re-apply if the exact same
	// drift value coincidentally reappears later without a human looking at
	// it again (see docker.ChangeFingerprint / store.ClearDriftIgnores).
	if err := s.store.ClearDriftIgnores(ctx, p.ID); err != nil {
		log.Printf("project revision: clear drift ignores for %q: %v", p.Slug, err)
	}
	dir := s.projectRevisionsDir(p.ID)
	if err := os.MkdirAll(dir, projectDirMode); err != nil {
		s.failRevisionCapture(ctx, p, rev, "create snapshot dir: "+err.Error())
		return
	}
	data, err := zipDir(s.projectRoot(p.ID))
	if err != nil {
		s.failRevisionCapture(ctx, p, rev, "build snapshot: "+err.Error())
		return
	}
	tmp, err := os.CreateTemp(dir, ".revision-*.zip.tmp")
	if err != nil {
		s.failRevisionCapture(ctx, p, rev, "create snapshot temp file: "+err.Error())
		return
	}
	tmpPath := tmp.Name()
	_, werr := tmp.Write(data)
	if serr := tmp.Sync(); werr == nil {
		werr = serr
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmpPath)
		s.failRevisionCapture(ctx, p, rev, "write snapshot: "+werr.Error())
		return
	}
	if err := os.Rename(tmpPath, s.revisionZipPath(p.ID, rev.Revision)); err != nil {
		_ = os.Remove(tmpPath)
		s.failRevisionCapture(ctx, p, rev, "publish snapshot: "+err.Error())
		return
	}
	if err := s.store.SetRevisionValid(ctx, rev.ID, true, ""); err != nil {
		log.Printf("project revision: mark %q rev %d valid: %v", p.Slug, rev.Revision, err)
	}
}

// failRevisionCapture records why a revision's snapshot could never be
// published — logged for the operator, and persisted on the row itself
// (still Valid=false from the initial insert) so ListRevisions/restore can
// explain the gap instead of silently offering a revision with no file.
func (s *Server) failRevisionCapture(ctx context.Context, p *store.Project, rev *store.ProjectRevision, msg string) {
	log.Printf("project revision: %q rev %d: %s", p.Slug, rev.Revision, msg)
	if err := s.store.SetRevisionValid(ctx, rev.ID, false, msg); err != nil {
		log.Printf("project revision: record capture failure for %q rev %d: %v", p.Slug, rev.Revision, err)
	}
}

// captureRevisionImages resolves, per service currently running for p, the
// image reference and (best-effort) the digest actually running right now —
// what a later restore needs in order to pin back to the exact image that
// ran then, since a mutable tag may have moved on by the time anyone rolls
// back. A service whose digest can't be determined (never pulled from a
// registry, host briefly unreachable) is still recorded by reference alone.
func (s *Server) captureRevisionImages(ctx context.Context, p *store.Project) []store.RevisionImage {
	out := []store.RevisionImage{}
	stacks, err := s.docker.ListStacks(ctx, p.HostID)
	if err != nil {
		return out
	}
	for i := range stacks {
		if stacks[i].Project != p.Slug {
			continue
		}
		seen := map[string]bool{}
		for _, c := range stacks[i].Containers {
			if c.Service == "" || seen[c.Service] {
				continue
			}
			seen[c.Service] = true
			img := store.RevisionImage{Service: c.Service, Image: c.Image}
			if d, derr := s.docker.RunningImageDigest(ctx, p.HostID, c.ID, c.Image); derr == nil {
				img.Digest = d
			}
			out = append(out, img)
		}
		break
	}
	return out
}

// handleListRevisions lists every revision for a project, newest first.
func (s *Server) handleListRevisions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	list, err := s.store.ListRevisions(r.Context(), p.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// revisionNumber parses the {rev} route param.
func revisionNumber(r *http.Request) (int, error) {
	return strconv.Atoi(chi.URLParam(r, "rev"))
}

// handleGetRevision returns one revision's metadata.
func (s *Server) handleGetRevision(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	n, err := revisionNumber(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid revision number")
		return
	}
	rev, err := s.store.RevisionByNumber(r.Context(), p.ID, n)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "revision not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rev)
}

// loadRevisionZip reads and opens a revision's on-disk file snapshot.
func (s *Server) loadRevisionZip(p *store.Project, revision int) (*zip.Reader, error) {
	data, err := os.ReadFile(s.revisionZipPath(p.ID, revision))
	if err != nil {
		return nil, fmt.Errorf("this revision's file snapshot could not be read: %w", err)
	}
	return zip.NewReader(bytes.NewReader(data), int64(len(data)))
}

// revisionServiceSpecs extracts a revision's zip to a scratch dir and
// resolves it with the compose CLI, exactly like validating an unsaved
// editor buffer (overlayProject) — a revision's snapshot is read this way
// rather than storing a second, potentially-stale copy of the resolved
// config (see the project_revisions schema comment in store.go).
func (s *Server) revisionServiceSpecs(ctx context.Context, p *store.Project, revision int) ([]docker.ServiceSpec, error) {
	zr, err := s.loadRevisionZip(p, revision)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "dc-revision-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	extractZipToDir(zr, tmp)
	cfgJSON, err := docker.ComposeConfigJSON(ctx, tmp, p.Slug)
	if err != nil {
		return nil, fmt.Errorf("revision %d no longer resolves as valid compose: %w", revision, err)
	}
	specs, err := docker.ParseComposeServices(cfgJSON)
	if err != nil {
		return nil, err
	}
	// A relative bind mount resolves to an absolute path anchored to tmp — a
	// fresh, different directory every call — so rebase it to where the
	// project's real files live before this is compared against anything
	// (the live container's actual mount source, or another revision's own
	// throwaway extraction dir). See RebaseBindSources.
	docker.RebaseBindSources(specs, tmp, s.projectRoot(p.ID))
	return specs, nil
}

// handleRevisionDiff compares one revision's services against either another
// revision (?against=<n>) or, by default, what's actually running right now
// — the same BuildDeployPreview/ExtendServiceComparison the deploy preview
// uses, so the UI can render either with the identical component.
func (s *Server) handleRevisionDiff(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	n, err := revisionNumber(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid revision number")
		return
	}
	resolved, err := s.revisionServiceSpecs(r.Context(), p, n)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"valid": false, "error": err.Error()})
		return
	}

	var running []docker.ServiceSpec
	against := strings.TrimSpace(r.URL.Query().Get("against"))
	if against == "" || strings.EqualFold(against, "current") {
		var containers []docker.StackContainer
		if stacks, serr := s.docker.ListStacks(r.Context(), p.HostID); serr == nil {
			for i := range stacks {
				if stacks[i].Project == p.Slug {
					containers = stacks[i].Containers
					break
				}
			}
		}
		running = s.docker.LiveServices(r.Context(), p.HostID, containers)
	} else {
		otherN, err := strconv.Atoi(against)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "against must be a revision number or \"current\"")
			return
		}
		running, err = s.revisionServiceSpecs(r.Context(), p, otherN)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"valid": false, "error": err.Error()})
			return
		}
	}

	prev := docker.BuildDeployPreview(resolved, running)
	docker.ExtendServiceComparison(&prev, resolved, running)
	writeJSON(w, http.StatusOK, map[string]any{
		"valid": true, "services": prev.Services, "running": prev.Running,
		"changes": prev.Changes, "unchanged": prev.Unchanged,
	})
}

// buildDigestPinOverride returns a minimal compose override pinning each
// service with a recorded digest to that exact image — so restoring an old
// revision redeploys what actually ran then, not whatever a mutable tag
// resolves to today. "" means nothing to pin (no digests were ever
// recorded, e.g. every image was local-only). Safe to build as a plain
// string: service names and image references are compose/registry
// identifiers ([A-Za-z0-9._/-] plus '@'/':'), none of which need YAML
// quoting.
func buildDigestPinOverride(images []store.RevisionImage) string {
	var b strings.Builder
	for _, img := range images {
		if img.Digest == "" || img.Service == "" || img.Image == "" {
			continue
		}
		repo := img.Image
		if i := strings.IndexByte(repo, '@'); i >= 0 {
			repo = repo[:i]
		}
		if b.Len() == 0 {
			b.WriteString("services:\n")
		}
		fmt.Fprintf(&b, "  %s:\n    image: %s@%s\n", img.Service, repo, img.Digest)
	}
	return b.String()
}

// restoreLocks serializes concurrent restores of the same project (reject,
// not queue — mirrors update_handlers.go's applyMu/TryLock precedent for
// "don't let two of this specific operation race on the same target"). It
// does not serialize restore against other project-mutating operations
// (deploy, editor save, delete) — those already have no such guard anywhere
// in this package, and adding one for all of them is a materially larger
// change than this fix; noted as a known gap, not attempted here.
var restoreLocks sync.Map // map[int64]*sync.Mutex

func projectRestoreLock(projectID int64) *sync.Mutex {
	v, _ := restoreLocks.LoadOrStore(projectID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// handleRestoreRevision rolls a project back to an earlier revision. Every
// validation — the snapshot still resolving as compose, the digest-pin
// override, policy — runs against a sibling staging copy of the project
// directory; the live project directory is only ever touched by a single
// atomic rename once all of that has already passed, with the previous
// contents kept as a backup until the subsequent deploy itself succeeds. Any
// failure at any point — including the deploy call — restores exactly what
// was there before, so a refused or failed restore can never leave the
// project's files disagreeing with what's actually running. Pins any
// service with a recorded digest to that exact image, uses the revision's
// own profiles, and re-uses the project's normal deploy path (so a remote
// host's bind-override machinery still applies unchanged). Never touches
// named volumes: the only Docker operation here is `up`, the same as any
// deploy. The restore itself becomes a new revision — history only grows
// forward.
func (s *Server) handleRestoreRevision(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	if !docker.ComposeAvailable(r.Context()) {
		writeErr(w, http.StatusPreconditionFailed, "the `docker compose` CLI is not available on the host running Docker Commander")
		return
	}
	if err := s.requireHostAccess(r, p.HostID); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	n, err := revisionNumber(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid revision number")
		return
	}
	rev, err := s.store.RevisionByNumber(r.Context(), p.ID, n)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "revision not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !rev.Valid {
		msg := "revision " + strconv.Itoa(n) + " has no usable file snapshot"
		if rev.ValidationError != "" {
			msg += ": " + rev.ValidationError
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": msg})
		return
	}
	var body struct {
		Reason string `json:"reason"`
		// ConfirmPolicyWarnings acknowledges any warn-mode policy violation
		// found on the compose model this revision restores — see
		// policyCheckOrRefuse. It never affects a block-mode violation, which
		// has no per-restore override.
		ConfirmPolicyWarnings bool `json:"confirmPolicyWarnings"`
	}
	_ = decodeJSON(r, &body)

	zr, err := s.loadRevisionZip(p, n)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	lock := projectRestoreLock(p.ID)
	if !lock.TryLock() {
		writeErr(w, http.StatusConflict, "a restore is already in progress for this project")
		return
	}
	defer lock.Unlock()

	root := s.projectRoot(p.ID)
	staging := root + ".restore-staging"
	backup := root + ".restore-backup"
	// Clear any leftovers from a previous attempt that crashed mid-way —
	// both are scratch state, never the live project.
	_ = os.RemoveAll(staging)
	_ = os.RemoveAll(backup)
	if err := os.MkdirAll(staging, projectDirMode); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.RemoveAll(staging) // no-op once it's been renamed away below
	extractZipToDir(zr, staging)
	if out, verr := docker.ComposeConfig(r.Context(), staging, p.Slug); verr != nil {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = verr.Error()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "revision " + strconv.Itoa(n) + " no longer validates: " + msg})
		return
	}

	env, files, note, cleanup, err := s.projectDeployEnv(r.Context(), p, staging)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// A plain `defer cleanup()` would capture today's value of cleanup right
	// here — before the digest-pin block below can reassign it to a wrapper
	// that also removes the pin file — and so run the wrong (stale) cleanup.
	// Deferring a closure that calls whatever `cleanup` holds at return time
	// fixes that.
	defer func() { cleanup() }()
	if pin := buildDigestPinOverride(rev.Images); pin != "" {
		pf, perr := os.CreateTemp("", "dc-rollback-pin-*.yml")
		if perr != nil {
			writeErr(w, http.StatusInternalServerError, "preparing the digest pin override failed: "+perr.Error())
			return
		}
		pinPath := pf.Name()
		_, werr := pf.WriteString(pin)
		cerr := pf.Close()
		if werr != nil || cerr != nil {
			_ = os.Remove(pinPath)
			writeErr(w, http.StatusInternalServerError, "preparing the digest pin override failed")
			return
		}
		// A pin override that can't be built is fatal, not skipped: silently
		// deploying unpinned would let a mutable tag pull today's image
		// instead of the digest this revision recorded, while restore still
		// reports success as though the exact prior state came back.
		prevCleanup := cleanup
		cleanup = func() { _ = os.Remove(pinPath); prevCleanup() }
		if len(files) == 0 {
			files = []string{p.ComposeFile}
		}
		files = append(files, pinPath)
	}

	// MUTATION-TEST-TEMP: policy check moved after swap to prove the
	// regression test catches the pre-fix ordering bug.
	// Everything has validated against the staged copy — only now does the
	// live project directory get touched, via a rename swap with the
	// previous contents kept as `backup` until the deploy below succeeds.
	_, statErr := os.Stat(root)
	rootExisted := statErr == nil
	if rootExisted {
		if err := os.Rename(root, backup); err != nil {
			writeErr(w, http.StatusInternalServerError, "could not stage the project swap: "+err.Error())
			return
		}
	}
	if err := os.Rename(staging, root); err != nil {
		if rootExisted {
			_ = os.Rename(backup, root)
		}
		writeErr(w, http.StatusInternalServerError, "could not activate the restored project: "+err.Error())
		return
	}
	swapCommitted := false
	defer func() {
		if swapCommitted {
			_ = os.RemoveAll(backup)
			return
		}
		// The deploy below failed after the swap — put the previous project
		// files back exactly as they were.
		_ = os.RemoveAll(root)
		if rootExisted {
			_ = os.Rename(backup, root)
		}
	}()

	if resp, refused := s.policyCheckOrRefuse(r, p, root, rev.Profiles, env, files, body.ConfirmPolicyWarnings, policyKindRestore); refused {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Restoring means going back to what ran then — rebuilding a `build:`
	// service against today's source would silently defeat that, so this
	// deploys whatever image is already available rather than rebuilding.
	out, err := docker.ComposeUpFiles(r.Context(), root, p.Slug, rev.Profiles, env, files, false)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "output": out})
		return
	}
	swapCommitted = true
	if err := s.store.SetLastDeployedProfiles(r.Context(), p.ID, rev.Profiles); err != nil {
		log.Printf("project restore: persist last deployed profiles for %q: %v", p.Slug, err)
	}
	reason := body.Reason
	if reason == "" {
		reason = "restored from revision " + strconv.Itoa(n)
	}
	s.captureRevision(r.Context(), p, rev.Profiles, out, reason, currentUsername(r))
	s.audit(r, "project.revision.restore", p.Slug, strconv.Itoa(n))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out, "note": note})
}
