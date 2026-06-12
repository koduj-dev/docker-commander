package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"golang.org/x/text/unicode/norm"

	"github.com/koduj-dev/docker-commander/internal/auth"
	"github.com/koduj-dev/docker-commander/internal/docker"
	"github.com/koduj-dev/docker-commander/internal/store"
)

// Compose Projects are managed folders (compose file + sidecar config/script
// files) deployed via the `docker compose` CLI on the host running Docker
// Commander. They are LOCAL-ONLY: the CLI follows its own Docker context, which
// is independent of the per-host SDK selector, so these routes ignore `?host=`.

const (
	maxProjectFiles     = 100
	maxProjectFileBytes = 1 << 20  // 1 MiB per file
	maxImportBytes      = 32 << 20 // 32 MiB for an imported .zip
)

// starterCompose seeds a new project so the editor isn't empty. The top-level
// name matches the slug (the -p flag we deploy with still wins, but it reads
// nicely and documents the project name).
func starterCompose(slug string) string {
	return "name: " + slug + `

services:
  app:
    image: nginx:alpine
    ports:
      - "8080:80"
`
}

type projectJSON struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	ComposeFile string `json:"composeFile"`
	CreatedBy   string `json:"createdBy"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

func toProjectJSON(p store.Project) projectJSON {
	return projectJSON{
		ID: p.ID, Name: p.Name, Slug: p.Slug, ComposeFile: p.ComposeFile,
		CreatedBy: p.CreatedBy,
		CreatedAt: p.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt: p.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

type projectFileJSON struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Content  string `json:"content"`
	IsDir    bool   `json:"isDir,omitempty"`
	TooLarge bool   `json:"tooLarge,omitempty"`
	Binary   bool   `json:"binary,omitempty"`
}

// looksBinary reports whether data is non-text (invalid UTF-8 or contains a NUL
// byte) — such files are surfaced as download-only instead of editable text.
func looksBinary(data []byte) bool {
	return !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0
}

// projectRoot derives a project's folder from its ID (never stored, so renames
// never move files).
func (s *Server) projectRoot(id int64) string {
	return filepath.Join(s.cfg.DataDir, "projects", strconv.FormatInt(id, 10))
}

func projectID(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
}

// handleListProjects returns the managed projects plus whether the compose CLI
// is available (the page disables Deploy/Down when it isn't).
func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.store.ListProjects(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]projectJSON, 0, len(projects))
	for _, p := range projects {
		out = append(out, toProjectJSON(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"projects":         out,
		"composeAvailable": docker.ComposeAvailable(r.Context()),
	})
}

// handleCreateProject creates a project folder seeded from a template, a builder
// block selection, or (by default) a starter compose file. The slug is derived
// from the name (compose-legal); a collision is 409.
func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string            `json:"name"`
		Template  *templateRef      `json:"template"`
		Blocks    []templateRef     `json:"blocks"`
		Variables map[string]string `json:"variables"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	slug := slugify(name)

	// Resolve the scaffold before creating anything, so a bad template/block
	// reference fails cleanly without leaving a half-created project behind.
	files, err := s.resolveSeedFiles(r.Context(), slug, name, body.Template, body.Blocks, body.Variables)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "template or block not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	id, err := s.store.CreateProject(r.Context(), &store.Project{
		Name: name, Slug: slug, ComposeFile: "compose.yml",
		CreatedBy: currentUsername(r),
	})
	if errors.Is(err, store.ErrDuplicate) {
		writeErr(w, http.StatusConflict, "a project with the name \""+slug+"\" already exists")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	root := s.projectRoot(id)
	if err := os.MkdirAll(root, 0o700); err != nil {
		_ = s.store.DeleteProject(r.Context(), id)
		writeErr(w, http.StatusInternalServerError, "could not create project folder: "+err.Error())
		return
	}
	// Seed the resolved files; roll back the row + folder on any write error so we
	// never return 200 over a half-created project.
	if err := seedProjectFiles(root, files); err != nil {
		_ = os.RemoveAll(root)
		_ = s.store.DeleteProject(r.Context(), id)
		writeErr(w, http.StatusInternalServerError, "could not seed project: "+err.Error())
		return
	}

	s.audit(r, "project.create", slug, "")
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "slug": slug})
}

// handleImportProject creates a project from an uploaded .zip (the request body
// is the zip; ?name= sets the display name). Entries are written through the
// same path sandbox as normal file writes.
func (s *Server) handleImportProject(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxImportBytes))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read upload")
		return
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "not a valid .zip archive")
		return
	}

	slug := slugify(name)
	id, err := s.store.CreateProject(r.Context(), &store.Project{Name: name, Slug: slug, CreatedBy: currentUsername(r)})
	if errors.Is(err, store.ErrDuplicate) {
		writeErr(w, http.StatusConflict, "a project with the name \""+slug+"\" already exists")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	root := s.projectRoot(id)
	if err := os.MkdirAll(root, 0o700); err != nil {
		_ = s.store.DeleteProject(r.Context(), id)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	count := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || count >= maxProjectFiles {
			continue
		}
		full, err := safeJoin(root, f.Name) // rejects traversal/absolute
		if err != nil || f.UncompressedSize64 > maxProjectFileBytes {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		content, _ := io.ReadAll(io.LimitReader(rc, maxProjectFileBytes))
		rc.Close()
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			continue
		}
		if os.WriteFile(full, content, 0o600) == nil {
			count++
		}
	}

	s.audit(r, "project.import", slug, "")
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "slug": slug, "files": count})
}

// handleGetProject returns a single project's metadata.
func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toProjectJSON(*p))
}

// handleDeleteProject removes a project. Refuses if it's currently deployed,
// unless ?force=1, in which case it runs `docker compose down` first.
func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	force := r.URL.Query().Get("force") == "1"

	if s.projectDeployed(r, p.Slug) {
		if !force {
			writeErr(w, http.StatusConflict, "project is deployed — bring it down first (or force)")
			return
		}
		if out, err := docker.ComposeDown(r.Context(), s.projectRoot(p.ID), p.Slug); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "compose down failed: " + err.Error(), "output": out})
			return
		}
		s.audit(r, "project.down", p.Slug, "force-delete")
	}

	if err := os.RemoveAll(s.projectRoot(p.ID)); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not remove files: "+err.Error())
		return
	}
	if err := s.store.DeleteProject(r.Context(), p.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "project.delete", p.Slug, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleListProjectFiles returns every file in the project folder with content
// (projects are small and bounded by maxProjectFiles/maxProjectFileBytes).
func (s *Server) handleListProjectFiles(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	out, err := listFilesInRoot(s.projectRoot(p.ID))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// listFilesInRoot walks a project/template folder and returns every file with
// its content (binary/oversized files are flagged, not inlined). Shared by the
// project and template file listings — both fold to the same caps and sandboxing.
func listFilesInRoot(root string) ([]projectFileJSON, error) {
	var out []projectFileJSON
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == root || d.Type()&fs.ModeSymlink != 0 {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		name := filepath.ToSlash(rel)
		if d.IsDir() {
			out = append(out, projectFileJSON{Name: name, IsDir: true})
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxProjectFileBytes {
			out = append(out, projectFileJSON{Name: name, Size: info.Size(), TooLarge: true})
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if looksBinary(data) {
			// Binary/data files (datasets, images, archives) live alongside the
			// compose file but can't be edited as text — surface them as
			// download-only with no content payload.
			out = append(out, projectFileJSON{Name: name, Size: info.Size(), Binary: true})
			return nil
		}
		out = append(out, projectFileJSON{Name: name, Size: info.Size(), Content: string(data)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []projectFileJSON{}
	}
	return out, nil
}

// handleWriteProjectFile creates or overwrites one file in the project folder.
func (s *Server) handleWriteProjectFile(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	var body struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if len(body.Content) > maxProjectFileBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "file too large")
		return
	}
	root := s.projectRoot(p.ID)
	full, err := safeJoin(root, body.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Enforce a file-count cap when adding a new file.
	if _, statErr := os.Stat(full); errors.Is(statErr, os.ErrNotExist) {
		if n, _ := countFiles(root); n >= maxProjectFiles {
			writeErr(w, http.StatusBadRequest, "too many files in this project")
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(full, []byte(body.Content), 0o600); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.TouchProject(r.Context(), p.ID)
	s.audit(r, "project.file.write", p.Slug, body.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleUploadProjectFileRaw stores a raw (possibly binary) file in the project
// folder from an octet-stream body — ?path=<name>. Lets datasets/binaries live
// alongside the compose file without passing through the JSON text editor.
func (s *Server) handleUploadProjectFileRaw(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	root := s.projectRoot(p.ID)
	name := r.URL.Query().Get("path")
	full, err := safeJoin(root, name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Enforce the file-count cap when adding a new file.
	if _, statErr := os.Stat(full); errors.Is(statErr, os.ErrNotExist) {
		if n, _ := countFiles(root); n >= maxProjectFiles {
			writeErr(w, http.StatusBadRequest, "too many files in this project")
			return
		}
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxProjectFileBytes+1))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(data) > maxProjectFileBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "file too large")
		return
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(full, data, 0o600); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.TouchProject(r.Context(), p.ID)
	s.audit(r, "project.file.upload", p.Slug, name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bytes": len(data)})
}

// handleDownloadProjectFile streams one file from the project folder (?path=)
// as an attachment — works for both text and binary files.
func (s *Server) handleDownloadProjectFile(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	name := r.URL.Query().Get("path")
	full, err := safeJoin(s.projectRoot(p.ID), name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	f, err := os.Open(full)
	if err != nil {
		writeErr(w, http.StatusNotFound, "file not found")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		writeErr(w, http.StatusBadRequest, "not a file")
		return
	}
	s.audit(r, "project.file.download", p.Slug, name)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+headerFilename(filepath.Base(full))+"\"")
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

// headerFilename replaces characters that aren't safe in a Content-Disposition
// value (control chars, quotes, backslashes). Project filenames are
// user-controlled and net/http panics when written into an invalid header.
func headerFilename(name string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
}

// handleDeleteProjectFile removes one file (?path=).
func (s *Server) handleDeleteProjectFile(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	full, err := safeJoin(s.projectRoot(p.ID), r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.TouchProject(r.Context(), p.ID)
	s.audit(r, "project.file.delete", p.Slug, r.URL.Query().Get("path"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleMakeProjectDir creates an (empty) folder in the project.
func (s *Server) handleMakeProjectDir(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	full, err := safeJoin(s.projectRoot(p.ID), body.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(full, 0o700); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "project.dir.create", p.Slug, body.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleRenameProject changes the display name. The slug (compose project name)
// is immutable, so deployments stay stable.
func (s *Server) handleRenameProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := s.store.UpdateProjectName(r.Context(), p.ID, name); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "project.rename", p.Slug, name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDeployProject runs `docker compose up -d` (with any selected profiles).
func (s *Server) handleDeployProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	if !docker.ComposeAvailable(r.Context()) {
		writeErr(w, http.StatusPreconditionFailed, "the `docker compose` CLI is not available on the host running Docker Commander")
		return
	}
	var body struct {
		Profiles []string `json:"profiles"`
	}
	_ = decodeJSON(r, &body) // body is optional (empty → no profiles)
	out, err := docker.ComposeUp(r.Context(), s.projectRoot(p.ID), p.Slug, body.Profiles)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "output": out})
		return
	}
	s.audit(r, "project.deploy", p.Slug, strings.Join(body.Profiles, ","))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out})
}

// handleValidateProject runs `docker compose config` over the project to catch
// schema / anchor / interpolation errors before deploying. It returns 200 even
// for an invalid file — the check ran, it just found problems — so the UI can
// show the error message inline.
//
// With an optional {name, content} body it validates the *unsaved* editor
// buffer: the project is copied to a temp dir, the named file overlaid with the
// posted content, and compose config run there. This powers live validation in
// the editor without forcing a save first.
func (s *Server) handleValidateProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	if !docker.ComposeAvailable(r.Context()) {
		writeJSON(w, http.StatusOK, map[string]any{"valid": false, "unavailable": true,
			"error": "the `docker compose` CLI is not available on the host running Docker Commander"})
		return
	}

	dir := s.projectRoot(p.ID)
	// Optional unsaved-overlay body (decodeJSON tolerates an empty body → the
	// button validates the on-disk files).
	var body struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &body); err == nil && body.Name != "" {
		tmp, err := s.overlayProject(p.ID, body.Name, body.Content)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer os.RemoveAll(tmp)
		dir = tmp
	}

	out, err := docker.ComposeConfig(r.Context(), dir, p.Slug)
	if err != nil {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = err.Error()
		}
		writeJSON(w, http.StatusOK, map[string]any{"valid": false, "error": msg})
		return
	}
	resp := map[string]any{"valid": true}
	// Surface non-fatal warnings (unset ${VAR}, deprecated keys) even on success.
	if ws := docker.ComposeWarnings(out); len(ws) > 0 {
		resp["warnings"] = ws
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleResolveProject returns the fully-resolved compose config (anchors,
// merge keys, interpolation and extends flattened) — what `up` actually
// deploys. Accepts the same optional {name, content} overlay as validation.
func (s *Server) handleResolveProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	if !docker.ComposeAvailable(r.Context()) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false,
			"error": "the `docker compose` CLI is not available on the host running Docker Commander"})
		return
	}
	dir := s.projectRoot(p.ID)
	var body struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &body); err == nil && body.Name != "" {
		tmp, err := s.overlayProject(p.ID, body.Name, body.Content)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer os.RemoveAll(tmp)
		dir = tmp
	}
	out, err := docker.ComposeResolvedConfig(r.Context(), dir, p.Slug)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": out})
}

// overlayProject copies the project folder to a fresh temp dir and overlays the
// named file with content, returning the temp dir (caller removes it). Used to
// validate unsaved editor buffers against the real (multi-file) project.
func (s *Server) overlayProject(id int64, name, content string) (string, error) {
	root := s.projectRoot(id)
	tmp, err := os.MkdirTemp("", "dc-validate-*")
	if err != nil {
		return "", err
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root || d.Type()&fs.ModeSymlink != 0 {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		dst := filepath.Join(tmp, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o700)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o600)
	})
	if err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	full, err := safeJoin(tmp, name)
	if err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	return tmp, nil
}

// handleProjectSummary returns the resolved compose model as JSON so the UI can
// render a services/ports/volumes overview and flag duplicate host ports.
// Accepts the same optional {name, content} overlay as validation.
func (s *Server) handleProjectSummary(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	if !docker.ComposeAvailable(r.Context()) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false,
			"error": "the `docker compose` CLI is not available on the host running Docker Commander"})
		return
	}
	dir := s.projectRoot(p.ID)
	var body struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &body); err == nil && body.Name != "" {
		tmp, err := s.overlayProject(p.ID, body.Name, body.Content)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer os.RemoveAll(tmp)
		dir = tmp
	}
	raw, err := docker.ComposeConfigJSON(r.Context(), dir, p.Slug)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model": json.RawMessage(raw)})
}

// handleCheckDockerfile lints a Dockerfile with `docker build --check` (no build
// steps run). The {content} body is the unsaved editor buffer. Returns 200 even
// when the check finds problems so the UI can show the message.
func (s *Server) handleCheckDockerfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.loadProject(w, r); !ok {
		return
	}
	if !docker.BuildCheckAvailable(r.Context()) {
		// Same {level, output} shape as the success/error paths below.
		writeJSON(w, http.StatusOK, map[string]any{"level": "error", "unavailable": true,
			"output": "`docker build --check` (BuildKit/buildx) is not available on the host running Docker Commander"})
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	out, err := docker.DockerfileCheck(r.Context(), body.Content)
	out = strings.TrimSpace(out)
	// Classify by the check output: a parse error is hard ("error"), lint findings
	// are "warning", a clean check is "ok". (Exit code can't tell them apart — both
	// lint warnings and parse errors are non-zero.)
	level := "ok"
	switch {
	case strings.Contains(out, "ERROR:"):
		level = "error"
	case strings.Contains(out, "WARNING:"):
		level = "warning"
	case out == "" && err != nil:
		level = "error"
		out = err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{"level": level, "output": out})
}

// handleProjectProfiles lists the compose profiles defined in the project.
func (s *Server) handleProjectProfiles(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	if !docker.ComposeAvailable(r.Context()) {
		writeJSON(w, http.StatusOK, map[string]any{"profiles": []string{}})
		return
	}
	profiles, err := docker.ComposeProfiles(r.Context(), s.projectRoot(p.ID), p.Slug)
	if err != nil {
		// Best-effort: an invalid compose file just means no profiles to offer.
		writeJSON(w, http.StatusOK, map[string]any{"profiles": []string{}, "error": err.Error()})
		return
	}
	if profiles == nil {
		profiles = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": profiles})
}

// handleDownProject runs `docker compose down`.
func (s *Server) handleDownProject(w http.ResponseWriter, r *http.Request) {
	s.runProjectCompose(w, r, docker.ComposeDown, "down")
}

// handleRestartProject runs `docker compose restart`.
func (s *Server) handleRestartProject(w http.ResponseWriter, r *http.Request) {
	s.runProjectCompose(w, r, docker.ComposeRestart, "restart")
}

// handleDownloadProject streams the project folder as a zip archive.
func (s *Server) handleDownloadProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	data, err := zipDir(s.projectRoot(p.ID))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not build project archive: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+p.Slug+`.zip"`)
	_, _ = w.Write(data)
}

// zipDir builds an in-memory zip of every file under root (folders are small and
// bounded), so a mid-walk read error becomes a clean error instead of a silently
// truncated archive. Symlinks are skipped. Shared by project/template downloads.
func zipDir(root string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		fw, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = fw.Write(data)
		return err
	})
	if err == nil {
		err = zw.Close()
	} else {
		_ = zw.Close()
	}
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *Server) runProjectCompose(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, dir, slug string) (string, error), action string) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	if !docker.ComposeAvailable(r.Context()) {
		writeErr(w, http.StatusPreconditionFailed, "the `docker compose` CLI is not available on the host running Docker Commander")
		return
	}
	out, err := fn(r.Context(), s.projectRoot(p.ID), p.Slug)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "output": out})
		return
	}
	s.audit(r, "project."+action, p.Slug, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out})
}

// loadProject resolves {id} to a project, writing the error response on failure.
func (s *Server) loadProject(w http.ResponseWriter, r *http.Request) (*store.Project, bool) {
	id, err := projectID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid project id")
		return nil, false
	}
	p, err := s.store.ProjectByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "project not found")
		return nil, false
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return p, true
}

// projectDeployed reports whether a stack with this slug currently exists on the
// local daemon.
func (s *Server) projectDeployed(r *http.Request, slug string) bool {
	stacks, err := s.docker.ListStacks(r.Context(), 0)
	if err != nil {
		return false
	}
	for _, st := range stacks {
		if st.Project == slug {
			return true
		}
	}
	return false
}

// safeJoin resolves a user-supplied relative file name to a path inside root,
// rejecting traversal, absolute paths and symlink escapes.
func safeJoin(root, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("missing file name")
	}
	if filepath.IsAbs(name) {
		return "", errors.New("absolute paths are not allowed")
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", errors.New("path traversal is not allowed")
	}
	full := filepath.Join(root, clean)
	if full != root && !strings.HasPrefix(full, root+string(os.PathSeparator)) {
		return "", errors.New("invalid file path")
	}
	// Reject a symlink anywhere along the path escaping the sandbox — not just at
	// the final component, but any parent dir too (e.g. config -> /etc). Resolve
	// the deepest part that already exists and confirm it still lives under root.
	if err := assertWithinRoot(root, full); err != nil {
		return "", err
	}
	return full, nil
}

// assertWithinRoot resolves symlinks in the existing portion of p and verifies
// the result stays inside root. The non-existent tail (yet-to-be-created dirs/
// files) is composed of literal, traversal-free names, so once the deepest
// existing ancestor resolves within root the full path is safe.
func assertWithinRoot(root, p string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	for cur := p; ; {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if resolved != realRoot && !strings.HasPrefix(resolved, realRoot+string(os.PathSeparator)) {
				return errors.New("path escapes the project directory")
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return errors.New("path escapes the project directory")
		}
		cur = parent
	}
}

// countFiles counts regular files under root.
func countFiles(root string) (int, error) {
	n := 0
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return err
	})
	return n, err
}

// slugify turns a display name into a compose-legal project name
// (^[a-z0-9][a-z0-9_-]*$). Diacritics are transliterated to ASCII (NFD then drop
// the combining marks) so accented letters become their base form (š→s, í→i,
// ů→u) instead of being dropped — e.g. "Další projekt" → "dalsi-projekt".
func slugify(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range norm.NFD.String(strings.ToLower(strings.TrimSpace(name))) {
		switch {
		case unicode.Is(unicode.Mn, r):
			// combining mark left over from NFD — drop it, keep the base letter
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case r == ' ' || r == '-' || r == '_' || r == '.':
			if b.Len() > 0 && !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "project"
	}
	return s
}

func currentUsername(r *http.Request) string {
	if c, ok := auth.ClaimsFrom(r.Context()); ok {
		return c.Username
	}
	return ""
}
