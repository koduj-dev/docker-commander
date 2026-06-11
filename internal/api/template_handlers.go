package api

import (
	"context"
	"errors"
	"fmt"
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
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(dst, []byte(f.Content), 0o600); err != nil {
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

// --- save as template / block ------------------------------------------------

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

	slug := slugify(name)
	id, err := s.store.CreateProjectTemplate(r.Context(), &store.ProjectTemplate{
		Name: name, Slug: slug, Description: strings.TrimSpace(body.Description), CreatedBy: currentUsername(r),
	})
	if errors.Is(err, store.ErrDuplicate) {
		writeErr(w, http.StatusConflict, "a template named \""+slug+"\" already exists")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	root := s.templateRoot(id)
	if err := os.MkdirAll(root, 0o700); err == nil {
		err = seedProjectFiles(root, files)
	}
	if err != nil {
		_ = os.RemoveAll(root)
		_ = s.store.DeleteProjectTemplate(r.Context(), id)
		writeErr(w, http.StatusInternalServerError, "could not store template files: "+err.Error())
		return
	}
	s.audit(r, "project_template.create", slug, "")
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
