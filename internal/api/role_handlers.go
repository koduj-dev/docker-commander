package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/koduj-dev/docker-commander/internal/store"
)

// Role management. Every route here is admin-only — `sectionForPath` maps
// "roles" to "__admin" — because a role is a grant of authority: anyone who can
// edit roles can widen their own access, so this must never be reachable by a
// non-admin, no matter which sections they hold.

type roleBody struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Sections    []store.RoleSection `json:"sections"`
}

func (s *Server) handleListRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := s.store.ListRoles(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list roles")
		return
	}
	if roles == nil {
		roles = []store.Role{}
	}
	// Also report how many users hold each role, so the UI can warn before a
	// delete that would strip access from live accounts.
	counts, err := s.roleUserCounts(r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not count role assignments")
		return
	}
	out := make([]map[string]any, 0, len(roles))
	for _, role := range roles {
		out = append(out, map[string]any{
			"id": role.ID, "name": role.Name, "description": role.Description,
			"builtin": role.Builtin, "sections": role.Sections, "users": counts[role.ID],
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// roleUserCounts maps role id → number of users holding it.
func (s *Server) roleUserCounts(r *http.Request) (map[int64]int, error) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		return nil, err
	}
	counts := map[int64]int{}
	for _, u := range users {
		ids, err := s.store.RoleIDsForUser(r.Context(), u.ID)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			counts[id]++
		}
	}
	return counts, nil
}

func (s *Server) handleCreateRole(w http.ResponseWriter, r *http.Request) {
	var b roleBody
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	id, err := s.store.CreateRole(r.Context(), &store.Role{
		Name: b.Name, Description: b.Description, Sections: b.Sections,
	})
	if errors.Is(err, store.ErrDuplicate) {
		writeErr(w, http.StatusConflict, "a role with that name already exists")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "role.create", b.Name, sectionSummary(b.Sections))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

func (s *Server) handleUpdateRole(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid role id")
		return
	}
	var b roleBody
	if err := decodeJSON(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	err = s.store.UpdateRole(r.Context(), id, b.Name, b.Description, b.Sections)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "role not found")
		return
	case errors.Is(err, store.ErrBuiltinRole):
		// Built-ins are the known-good baseline; the UI offers Duplicate.
		writeErr(w, http.StatusForbidden, "built-in roles cannot be edited — duplicate it instead")
		return
	case errors.Is(err, store.ErrDuplicate):
		writeErr(w, http.StatusConflict, "a role with that name already exists")
		return
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "role.update", b.Name, sectionSummary(b.Sections))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid role id")
		return
	}
	role, err := s.store.RoleByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "role not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	err = s.store.DeleteRole(r.Context(), id)
	if errors.Is(err, store.ErrBuiltinRole) {
		writeErr(w, http.StatusForbidden, "built-in roles cannot be deleted")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "role.delete", role.Name, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDuplicateRole copies a role (built-in or not) into a new editable one —
// the same affordance Templates uses for built-in presets.
func (s *Server) handleDuplicateRole(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid role id")
		return
	}
	src, err := s.store.RoleByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "role not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Find a free name rather than failing on the first collision.
	name := src.Name + " copy"
	for i := 2; ; i++ {
		if _, err := s.roleByName(r, name); errors.Is(err, store.ErrNotFound) {
			break
		}
		name = src.Name + " copy " + strconv.Itoa(i)
		if i > 50 {
			writeErr(w, http.StatusConflict, "too many copies of that role")
			return
		}
	}
	newID, err := s.store.CreateRole(r.Context(), &store.Role{
		Name: name, Description: src.Description, Sections: src.Sections,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "role.duplicate", src.Name, name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": newID, "name": name})
}

// roleByName looks a role up by name, returning ErrNotFound when absent.
func (s *Server) roleByName(r *http.Request, name string) (*store.Role, error) {
	roles, err := s.store.ListRoles(r.Context())
	if err != nil {
		return nil, err
	}
	for i := range roles {
		if roles[i].Name == name {
			return &roles[i], nil
		}
	}
	return nil, store.ErrNotFound
}

// sectionSummary renders a role's grants for the audit detail column, e.g.
// "containers:rw, images:r".
func sectionSummary(sections []store.RoleSection) string {
	out := ""
	for i, rs := range sections {
		if i > 0 {
			out += ", "
		}
		out += rs.Section
		if rs.Write {
			out += ":rw"
		} else {
			out += ":r"
		}
	}
	return out
}
