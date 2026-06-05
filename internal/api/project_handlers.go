package api

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

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

// handleCreateProject creates a project folder seeded with a starter compose
// file. The slug is derived from the name (compose-legal); a collision is 409.
func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
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
	slug := slugify(name)

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
	_ = os.WriteFile(filepath.Join(root, "compose.yml"), []byte(starterCompose(slug)), 0o600)

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
	root := s.projectRoot(p.ID)
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
		out = append(out, projectFileJSON{Name: name, Size: info.Size(), Content: string(data)})
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []projectFileJSON{}
	}
	writeJSON(w, http.StatusOK, out)
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
	root := s.projectRoot(p.ID)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+p.Slug+`.zip"`)
	zw := zip.NewWriter(w)
	defer zw.Close()
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
	if fi, err := os.Lstat(full); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("symlinks are not allowed")
	}
	return full, nil
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
