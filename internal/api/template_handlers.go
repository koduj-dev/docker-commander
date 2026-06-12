package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/store"
	"github.com/koduj-dev/docker-commander/internal/templates"
)

// Project templates & builder service blocks. Both come in two flavours that the
// API merges: "builtin" (embedded, read-only) and "user" (saved in the store).
// A new project is seeded server-side from either a template or a set of blocks.

// templateRef identifies a builtin (ID = catalog dir name) or user (ID = numeric
// DB id) preset/block.
type templateRef struct {
	ID     string `json:"id"`
	Source string `json:"source"`
}

type templateJSON struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Source      string               `json:"source"`
	Variables   []templates.Variable `json:"variables,omitempty"`
	Deletable   bool                 `json:"deletable"`
}

type blockJSON struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Source      string               `json:"source"`
	Service     string               `json:"service"`
	Variables   []templates.Variable `json:"variables,omitempty"`
	Deletable   bool                 `json:"deletable"`
}

// templateDetailJSON / blockDetailJSON carry the full payload (files / YAML) the
// management page needs to view or edit one item — the list responses omit these.
type templateDetailJSON struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Source      string               `json:"source"`
	Variables   []templates.Variable `json:"variables,omitempty"`
	Files       []templates.File     `json:"files"`
	Deletable   bool                 `json:"deletable"`
}

type blockDetailJSON struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Source      string               `json:"source"`
	Service     string               `json:"service"`
	ServiceYAML string               `json:"serviceYaml"`
	Volumes     []string             `json:"volumes"`
	Variables   []templates.Variable `json:"variables,omitempty"`
	Deletable   bool                 `json:"deletable"`
}

func (s *Server) templateRoot(id int64) string {
	return filepath.Join(s.cfg.DataDir, "project-templates", strconv.FormatInt(id, 10))
}

// --- listing -----------------------------------------------------------------

func (s *Server) handleListProjectTemplates(w http.ResponseWriter, r *http.Request) {
	out := []templateJSON{}
	builtins, err := templates.BuiltinPresets()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not load built-in templates")
		return
	}
	for _, p := range builtins {
		out = append(out, templateJSON{
			ID: p.ID, Name: p.Name, Description: p.Description,
			Source: templates.SourceBuiltin, Variables: p.Variables, Deletable: false,
		})
	}
	user, err := s.store.ListProjectTemplates(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list templates")
		return
	}
	for _, t := range user {
		out = append(out, templateJSON{
			ID: strconv.FormatInt(t.ID, 10), Name: t.Name, Description: t.Description,
			Source: templates.SourceUser, Deletable: true,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListServiceBlocks(w http.ResponseWriter, r *http.Request) {
	out := []blockJSON{}
	builtins, err := templates.BuiltinBlocks()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not load built-in blocks")
		return
	}
	for _, b := range builtins {
		out = append(out, blockJSON{
			ID: b.ID, Name: b.Name, Description: b.Description, Source: templates.SourceBuiltin,
			Service: b.Service, Variables: b.Variables, Deletable: false,
		})
	}
	user, err := s.store.ListServiceBlocks(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list service blocks")
		return
	}
	for _, b := range user {
		out = append(out, blockJSON{
			ID: strconv.FormatInt(b.ID, 10), Name: b.Name, Description: b.Description,
			Source: templates.SourceUser, Service: b.Service, Deletable: true,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// --- seeding (used by handleCreateProject) -----------------------------------

// errBadTemplateRef is returned for a malformed or unknown builtin reference.
var errBadTemplateRef = errors.New("unknown template")

// resolveSeedFiles produces the (rendered) files a new project should be created
// with, from either a block selection (builder), a template, or — when neither
// is given — the plain starter compose. Pure read: it never mutates state, so
// the caller can resolve before creating the project row.
func (s *Server) resolveSeedFiles(ctx context.Context, slug, name string, tpl *templateRef, blocks []templateRef, vars map[string]string) ([]templates.File, error) {
	if len(blocks) > 0 {
		var blks []templates.Block
		var decl []templates.Variable
		for _, ref := range blocks {
			b, err := s.resolveBlock(ctx, ref)
			if err != nil {
				return nil, err
			}
			blks = append(blks, b)
			decl = append(decl, b.Variables...)
		}
		rv, err := templates.ResolveVars(decl, vars)
		if err != nil {
			return nil, err
		}
		rv["Slug"], rv["Name"] = slug, name
		return templates.AssembleCompose(slug, blks, rv)
	}

	if tpl != nil {
		switch tpl.Source {
		case templates.SourceBuiltin:
			p, err := findBuiltinPreset(tpl.ID)
			if err != nil {
				return nil, err
			}
			rv, err := templates.ResolveVars(p.Variables, vars)
			if err != nil {
				return nil, err
			}
			rv["Slug"], rv["Name"] = slug, name
			return templates.Render(p.Files, rv)
		case templates.SourceUser:
			id, err := strconv.ParseInt(tpl.ID, 10, 64)
			if err != nil {
				return nil, errBadTemplateRef
			}
			t, err := s.store.ProjectTemplateByID(ctx, id)
			if err != nil {
				return nil, err
			}
			// User templates are literal snapshots — copied as-is, not rendered.
			return readProjectFilesFromDisk(s.templateRoot(t.ID))
		default:
			return nil, errBadTemplateRef
		}
	}

	return []templates.File{{Path: "compose.yml", Content: starterCompose(slug)}}, nil
}

func (s *Server) resolveBlock(ctx context.Context, ref templateRef) (templates.Block, error) {
	switch ref.Source {
	case templates.SourceBuiltin:
		return findBuiltinBlock(ref.ID)
	case templates.SourceUser:
		id, err := strconv.ParseInt(ref.ID, 10, 64)
		if err != nil {
			return templates.Block{}, errBadTemplateRef
		}
		b, err := s.store.ServiceBlockByID(ctx, id)
		if err != nil {
			return templates.Block{}, err
		}
		return templates.Block{
			ID: ref.ID, Name: b.Name, Source: templates.SourceUser, Service: b.Service,
			Volumes: b.Volumes, ServiceYAML: strings.TrimRight(b.ServiceYAML, "\n"),
		}, nil
	default:
		return templates.Block{}, errBadTemplateRef
	}
}

func findBuiltinPreset(id string) (templates.Preset, error) {
	list, err := templates.BuiltinPresets()
	if err != nil {
		return templates.Preset{}, err
	}
	for _, p := range list {
		if p.ID == id {
			return p, nil
		}
	}
	return templates.Preset{}, errBadTemplateRef
}

func findBuiltinBlock(id string) (templates.Block, error) {
	list, err := templates.BuiltinBlocks()
	if err != nil {
		return templates.Block{}, err
	}
	for _, b := range list {
		if b.ID == id {
			return b, nil
		}
	}
	return templates.Block{}, errBadTemplateRef
}

// seedProjectFiles writes a resolved file set into a project/template root,
// enforcing the same count/size caps as the editor and sandboxing every path.
func seedProjectFiles(root string, files []templates.File) error {
	if len(files) > maxProjectFiles {
		return fmt.Errorf("too many files (%d > %d)", len(files), maxProjectFiles)
	}
	for _, f := range files {
		if len(f.Content) > maxProjectFileBytes {
			return fmt.Errorf("file %q is too large", f.Path)
		}
		dst, err := safeJoin(root, f.Path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), projectDirMode); err != nil {
			return err
		}
		if err := os.WriteFile(dst, []byte(f.Content), projectFileMode); err != nil {
			return err
		}
	}
	return nil
}

// readProjectFilesFromDisk snapshots every file under root into a file set.
func readProjectFilesFromDisk(root string) ([]templates.File, error) {
	var out []templates.File
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // don't follow symlinks (matches the other project walkers)
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if len(data) > maxProjectFileBytes {
			return fmt.Errorf("file %q is too large to snapshot", rel)
		}
		out = append(out, templates.File{Path: filepath.ToSlash(rel), Content: string(data)})
		return nil
	})
	return out, err
}

// --- save as template / duplicate --------------------------------------------

// storeUserTemplate creates a user preset row and seeds its files on disk,
// rolling back the row + folder on any write error. Shared by save-as-template
// and duplicate. Returns store.ErrDuplicate on a slug collision (caller maps it
// to 409).
func (s *Server) storeUserTemplate(ctx context.Context, name, description, createdBy string, files []templates.File) (int64, string, error) {
	slug := slugify(name)
	id, err := s.store.CreateProjectTemplate(ctx, &store.ProjectTemplate{
		Name: name, Slug: slug, Description: description, CreatedBy: createdBy,
	})
	if err != nil {
		return 0, slug, err
	}
	root := s.templateRoot(id)
	if err := os.MkdirAll(root, 0o700); err == nil {
		err = seedProjectFiles(root, files)
	}
	if err != nil {
		_ = os.RemoveAll(root)
		_ = s.store.DeleteProjectTemplate(ctx, id)
		return 0, slug, err
	}
	return id, slug, nil
}

func (s *Server) handleCreateProjectTemplate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		FromProjectID int64  `json:"fromProjectId"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || body.FromProjectID == 0 {
		writeErr(w, http.StatusBadRequest, "name and fromProjectId are required")
		return
	}
	if _, err := s.store.ProjectByID(r.Context(), body.FromProjectID); err != nil {
		writeErr(w, http.StatusNotFound, "source project not found")
		return
	}
	files, err := readProjectFilesFromDisk(s.projectRoot(body.FromProjectID))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not read project files: "+err.Error())
		return
	}
	id, slug, err := s.storeUserTemplate(r.Context(), name, strings.TrimSpace(body.Description), currentUsername(r), files)
	if errors.Is(err, store.ErrDuplicate) {
		writeErr(w, http.StatusConflict, "a template named \""+slug+"\" already exists")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not store template files: "+err.Error())
		return
	}
	s.audit(r, "project_template.create", slug, "")
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "slug": slug})
}

// handleDuplicateProjectTemplate copies any preset — built-in or user — into a
// new, editable user preset. A built-in source is rendered with its default
// variable values first (user presets are literal snapshots with no variables),
// so the copy has concrete files instead of unresolved {{.Var}} markers; a user
// source is copied as-is.
func (s *Server) handleDuplicateProjectTemplate(w http.ResponseWriter, r *http.Request) {
	srcID := chi.URLParam(r, "id")
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
	src := &templateRef{ID: srcID, Source: templates.SourceBuiltin}
	if _, err := strconv.ParseInt(srcID, 10, 64); err == nil {
		src.Source = templates.SourceUser
	}
	files, err := s.resolveSeedFiles(r.Context(), slugify(name), name, src, nil, nil)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, errBadTemplateRef) {
		writeErr(w, http.StatusNotFound, "source template not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id, slug, err := s.storeUserTemplate(r.Context(), name, "", currentUsername(r), files)
	if errors.Is(err, store.ErrDuplicate) {
		writeErr(w, http.StatusConflict, "a template named \""+slug+"\" already exists")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not store template files: "+err.Error())
		return
	}
	s.audit(r, "project_template.duplicate", slug, srcID)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "slug": slug})
}

func (s *Server) handleDeleteProjectTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid template id")
		return
	}
	if err := s.store.DeleteProjectTemplate(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not delete template")
		return
	}
	_ = os.RemoveAll(s.templateRoot(id))
	s.audit(r, "project_template.delete", chi.URLParam(r, "id"), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleCreateServiceBlock(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Service     string   `json:"service"`
		ServiceYAML string   `json:"serviceYaml"`
		Volumes     []string `json:"volumes"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || strings.TrimSpace(body.Service) == "" || strings.TrimSpace(body.ServiceYAML) == "" {
		writeErr(w, http.StatusBadRequest, "name, service and serviceYaml are required")
		return
	}
	slug := slugify(name)
	id, err := s.store.CreateServiceBlock(r.Context(), &store.ServiceBlock{
		Name: name, Slug: slug, Description: strings.TrimSpace(body.Description),
		Service: strings.TrimSpace(body.Service), ServiceYAML: body.ServiceYAML,
		Volumes: body.Volumes, CreatedBy: currentUsername(r),
	})
	if errors.Is(err, store.ErrDuplicate) {
		writeErr(w, http.StatusConflict, "a block named \""+slug+"\" already exists")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "service_block.create", slug, "")
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "slug": slug})
}

func (s *Server) handleDeleteServiceBlock(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid block id")
		return
	}
	if err := s.store.DeleteServiceBlock(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not delete service block")
		return
	}
	s.audit(r, "service_block.delete", chi.URLParam(r, "id"), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- preview -----------------------------------------------------------------

// handlePreviewTemplate assembles the compose.yml (and any sidecar files) a given
// template or block selection would produce, WITHOUT creating a project — it
// powers the live read-only preview in the New project dialog. Pure read.
func (s *Server) handlePreviewTemplate(w http.ResponseWriter, r *http.Request) {
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
		name = "preview"
	}
	files, err := s.resolveSeedFiles(r.Context(), slugify(name), name, body.Template, body.Blocks, body.Variables)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "template or block not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

// --- detail / update ---------------------------------------------------------

// loadUserTemplate resolves {id} to a user-saved template (builtins have a
// non-numeric id and are read-only, so they 404 here), writing the error itself.
func (s *Server) loadUserTemplate(w http.ResponseWriter, r *http.Request) (*store.ProjectTemplate, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "template not found")
		return nil, false
	}
	t, err := s.store.ProjectTemplateByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "template not found")
		return nil, false
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return t, true
}

// handleGetProjectTemplate returns one preset with its files. Built-in presets
// (non-numeric id) return their embedded, *unrendered* source; user presets
// return the on-disk snapshot.
func (s *Server) handleGetProjectTemplate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if nid, err := strconv.ParseInt(id, 10, 64); err == nil {
		t, err := s.store.ProjectTemplateByID(r.Context(), nid)
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "template not found")
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		files, err := readProjectFilesFromDisk(s.templateRoot(t.ID))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not read template files: "+err.Error())
			return
		}
		if files == nil {
			files = []templates.File{}
		}
		writeJSON(w, http.StatusOK, templateDetailJSON{
			ID: id, Name: t.Name, Description: t.Description, Source: templates.SourceUser,
			Files: files, Deletable: true,
		})
		return
	}
	p, err := findBuiltinPreset(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "template not found")
		return
	}
	writeJSON(w, http.StatusOK, templateDetailJSON{
		ID: p.ID, Name: p.Name, Description: p.Description, Source: templates.SourceBuiltin,
		Variables: p.Variables, Files: p.Files, Deletable: false,
	})
}

// handleUpdateProjectTemplate renames a user preset / edits its description (the
// slug stays put). Files are edited through the file endpoints below.
func (s *Server) handleUpdateProjectTemplate(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadUserTemplate(w, r)
	if !ok {
		return
	}
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
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
	if err := s.store.UpdateProjectTemplate(r.Context(), t.ID, name, strings.TrimSpace(body.Description)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "project_template.update", t.Slug, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleGetServiceBlock returns one block's full body (YAML + volumes) so the
// management page can view a built-in or edit a user block.
func (s *Server) handleGetServiceBlock(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if nid, err := strconv.ParseInt(id, 10, 64); err == nil {
		b, err := s.store.ServiceBlockByID(r.Context(), nid)
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "service block not found")
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, blockDetailJSON{
			ID: id, Name: b.Name, Description: b.Description, Source: templates.SourceUser,
			Service: b.Service, ServiceYAML: b.ServiceYAML, Volumes: b.Volumes, Deletable: true,
		})
		return
	}
	b, err := findBuiltinBlock(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "service block not found")
		return
	}
	writeJSON(w, http.StatusOK, blockDetailJSON{
		ID: b.ID, Name: b.Name, Description: b.Description, Source: templates.SourceBuiltin,
		Service: b.Service, ServiceYAML: b.ServiceYAML, Volumes: b.Volumes, Variables: b.Variables, Deletable: false,
	})
}

// handleUpdateServiceBlock edits a user block (built-ins have a non-numeric id
// and 400 here, so they can't be modified).
func (s *Server) handleUpdateServiceBlock(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "service block not found")
		return
	}
	existing, err := s.store.ServiceBlockByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "service block not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var body struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Service     string   `json:"service"`
		ServiceYAML string   `json:"serviceYaml"`
		Volumes     []string `json:"volumes"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.Service) == "" || strings.TrimSpace(body.ServiceYAML) == "" {
		writeErr(w, http.StatusBadRequest, "name, service and serviceYaml are required")
		return
	}
	existing.Name = strings.TrimSpace(body.Name)
	existing.Description = strings.TrimSpace(body.Description)
	existing.Service = strings.TrimSpace(body.Service)
	existing.ServiceYAML = body.ServiceYAML
	existing.Volumes = body.Volumes
	if err := s.store.UpdateServiceBlock(r.Context(), existing); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "service_block.update", existing.Slug, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- template files (user presets only) --------------------------------------
// These mirror the project file endpoints but operate on the template root and
// reject built-ins (loadUserTemplate 404s on a non-numeric id). Templates have no
// updated_at to touch and aren't deployable, so there's no compose tooling here.

func (s *Server) handleListTemplateFiles(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadUserTemplate(w, r)
	if !ok {
		return
	}
	out, err := listFilesInRoot(s.templateRoot(t.ID))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleWriteTemplateFile(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadUserTemplate(w, r)
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
	root := s.templateRoot(t.ID)
	full, err := safeJoin(root, body.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, statErr := os.Stat(full); errors.Is(statErr, os.ErrNotExist) {
		if n, _ := countFiles(root); n >= maxProjectFiles {
			writeErr(w, http.StatusBadRequest, "too many files in this template")
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(full), projectDirMode); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(full, []byte(body.Content), projectFileMode); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "project_template.file.write", t.Slug, body.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleUploadTemplateFileRaw(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadUserTemplate(w, r)
	if !ok {
		return
	}
	root := s.templateRoot(t.ID)
	name := r.URL.Query().Get("path")
	full, err := safeJoin(root, name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, statErr := os.Stat(full); errors.Is(statErr, os.ErrNotExist) {
		if n, _ := countFiles(root); n >= maxProjectFiles {
			writeErr(w, http.StatusBadRequest, "too many files in this template")
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
	if err := os.MkdirAll(filepath.Dir(full), projectDirMode); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(full, data, projectFileMode); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "project_template.file.upload", t.Slug, name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bytes": len(data)})
}

func (s *Server) handleDownloadTemplateFile(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadUserTemplate(w, r)
	if !ok {
		return
	}
	name := r.URL.Query().Get("path")
	full, err := safeJoin(s.templateRoot(t.ID), name)
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
	s.audit(r, "project_template.file.download", t.Slug, name)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+headerFilename(filepath.Base(full))+"\"")
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

func (s *Server) handleDeleteTemplateFile(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadUserTemplate(w, r)
	if !ok {
		return
	}
	full, err := safeJoin(s.templateRoot(t.ID), r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "project_template.file.delete", t.Slug, r.URL.Query().Get("path"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMakeTemplateDir(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadUserTemplate(w, r)
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
	full, err := safeJoin(s.templateRoot(t.ID), body.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(full, projectDirMode); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "project_template.dir.create", t.Slug, body.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDownloadTemplate(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadUserTemplate(w, r)
	if !ok {
		return
	}
	data, err := zipDir(s.templateRoot(t.ID))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not build template archive: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+t.Slug+`.zip"`)
	_, _ = w.Write(data)
}
