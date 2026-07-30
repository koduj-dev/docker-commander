package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
)

// Roles are assignable bundles of section grants, so an admin doesn't tick 13
// checkboxes per user. A role grants a set of sections, each either read-only or
// writable; a user's effective access is the union of their roles and their own
// per-user section list, capped by their read-only flag and by the app-wide
// disabled sections.
//
// The two built-in roles are read-only in the UI (Duplicate to customise), the
// same model Templates uses for built-in presets. The `admin` role remains a
// string on the user rather than a row here: it is the lockout safety valve, and
// making it data would invite a migration that locks the operator out.
const (
	// BuiltinViewer grants every section, read-only.
	BuiltinViewer = "Viewer"
	// BuiltinOperator grants day-to-day operation but not the sections that
	// confer authority over the installation itself.
	BuiltinOperator = "Operator"
)

// RoleSection is one section grant inside a role.
type RoleSection struct {
	Section string `json:"section"`
	Write   bool   `json:"write"`
}

// Role is a named bundle of section grants, optionally limited to a set of
// Docker hosts.
type Role struct {
	ID          int64         `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Builtin     bool          `json:"builtin"`
	Sections    []RoleSection `json:"sections"`
	// HostIDs limits the role to those hosts. EMPTY MEANS EVERY HOST, so an
	// existing role keeps its reach and a newly created one isn't accidentally
	// scoped to nothing. The local daemon (0) is always reachable and is never
	// stored here.
	HostIDs []int64 `json:"hostIds"`
}

// Grant is the effective access to one section: whether it's granted at all,
// whether writes are permitted, and on which hosts.
type Grant struct {
	Granted bool
	Write   bool
	// AllHosts is set when at least one grant of this section came from an
	// unscoped source (an unscoped role, or the account's own section list).
	// Then Hosts is irrelevant.
	AllHosts bool
	// Hosts are the additional in-scope host ids when AllHosts is false. The
	// local daemon (0) is always in scope and is not listed.
	Hosts map[int64]bool
}

// HasHost reports whether the grant reaches hostID. Host 0 (the local daemon) is
// always in scope: it is the one host a single-host install cannot afford to
// lock itself out of.
func (g Grant) HasHost(hostID int64) bool {
	return hostID == 0 || g.AllHosts || g.Hosts[hostID]
}

// operatorSections are the sections BuiltinOperator grants (writable). The
// omitted ones — hosts, registries, audit — are authority over the installation
// rather than day-to-day work, so an Operator shouldn't get them by default.
var operatorSections = []string{
	"dashboard", "containers", "projects", "images", "volumes", "networks",
	"topology", "logs", "events", "alerts",
}

// seedBuiltinRoles creates the built-in roles if absent. It never overwrites an
// existing row: an operator may have edited the description, and silently
// resetting grants on every startup would be a surprising (and security-relevant)
// change. Called from migrate.
func (s *Store) seedBuiltinRoles(ctx context.Context) error {
	builtins := []struct {
		name, desc string
		sections   []RoleSection
	}{
		{BuiltinViewer, "Read-only access to every section.", allSectionsRO()},
		{BuiltinOperator, "Day-to-day operation: workloads, storage, networking and alerts. No hosts, registries or audit log.", writable(operatorSections)},
	}
	for _, b := range builtins {
		var id int64
		err := s.db.QueryRowContext(ctx, `SELECT id FROM roles WHERE name = ?`, b.name).Scan(&id)
		if err == nil {
			continue // already present, leave it alone
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO roles (name, description, builtin) VALUES (?, ?, 1)`, b.name, b.desc)
		if err != nil {
			return err
		}
		newID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if err := s.replaceRoleSections(ctx, newID, b.sections); err != nil {
			return err
		}
	}
	return nil
}

func allSectionsRO() []RoleSection {
	out := make([]RoleSection, 0, len(Sections))
	for _, sec := range Sections {
		out = append(out, RoleSection{Section: sec, Write: false})
	}
	return out
}

func writable(sections []string) []RoleSection {
	out := make([]RoleSection, 0, len(sections))
	for _, sec := range sections {
		out = append(out, RoleSection{Section: sec, Write: true})
	}
	return out
}

// replaceRoleSections rewrites a role's grants wholesale, dropping unknown
// section keys so a bad payload can't grant something that doesn't exist.
//
// Transactional on purpose: the delete-then-insert would otherwise be visible
// half-applied to a concurrent request, and a failure partway would leave the
// role holding an arbitrary subset of its grants.
func (s *Store) replaceRoleSections(ctx context.Context, roleID int64, sections []RoleSection) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed
	if _, err := tx.ExecContext(ctx, `DELETE FROM role_sections WHERE role_id = ?`, roleID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, rs := range sections {
		key := strings.TrimSpace(rs.Section)
		if key == "" || seen[key] || !ValidSection(key) {
			continue
		}
		seen[key] = true
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO role_sections (role_id, section, write) VALUES (?, ?, ?)`,
			roleID, key, boolToInt(rs.Write)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// replaceRoleHosts rewrites a role's host scope. Host 0 is dropped: the local
// daemon is always in scope, and storing it would make an "only local" scope
// indistinguishable from the unscoped case that means every host.
func (s *Store) replaceRoleHosts(ctx context.Context, roleID int64, hostIDs []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM role_hosts WHERE role_id = ?`, roleID); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, id := range hostIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO role_hosts (role_id, host_id) VALUES (?, ?)`, roleID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// roleHosts returns a role's host scope. An empty slice means every host.
func (s *Store) roleHosts(ctx context.Context, roleID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT host_id FROM role_hosts WHERE role_id = ? ORDER BY host_id`, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListRoles returns every role with its grants, ordered built-ins first then by
// name (mirroring how Templates lists built-in presets ahead of user ones).
func (s *Store) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, builtin FROM roles ORDER BY builtin DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Role
	for rows.Next() {
		var r Role
		var builtin int
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &builtin); err != nil {
			return nil, err
		}
		r.Builtin = builtin != 0
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		secs, err := s.roleSections(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Sections = secs
		hosts, err := s.roleHosts(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].HostIDs = hosts
	}
	return out, nil
}

// RoleByID looks up one role with its grants.
func (s *Store) RoleByID(ctx context.Context, id int64) (*Role, error) {
	var r Role
	var builtin int
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, builtin FROM roles WHERE id = ?`, id).
		Scan(&r.ID, &r.Name, &r.Description, &builtin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Builtin = builtin != 0
	secs, err := s.roleSections(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	r.Sections = secs
	hosts, err := s.roleHosts(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	r.HostIDs = hosts
	return &r, nil
}

func (s *Store) roleSections(ctx context.Context, roleID int64) ([]RoleSection, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT section, write FROM role_sections WHERE role_id = ? ORDER BY section`, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RoleSection{}
	for rows.Next() {
		var rs RoleSection
		var write int
		if err := rows.Scan(&rs.Section, &write); err != nil {
			return nil, err
		}
		rs.Write = write != 0
		out = append(out, rs)
	}
	return out, rows.Err()
}

// CreateRole inserts a user-defined role. A duplicate name yields ErrDuplicate.
func (s *Store) CreateRole(ctx context.Context, r *Role) (int64, error) {
	name := strings.TrimSpace(r.Name)
	if name == "" {
		return 0, errors.New("role name is required")
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO roles (name, description, builtin) VALUES (?, ?, 0)`, name, r.Description)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrDuplicate
		}
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := s.replaceRoleSections(ctx, id, r.Sections); err != nil {
		return 0, err
	}
	if err := s.replaceRoleHosts(ctx, id, r.HostIDs); err != nil {
		return 0, err
	}
	return id, nil
}

// UpdateRole renames a role and replaces its grants. Built-in roles are refused:
// they are the known-good baseline, and the UI offers Duplicate instead.
func (s *Store) UpdateRole(ctx context.Context, id int64, name, description string, sections []RoleSection, hostIDs []int64) error {
	existing, err := s.RoleByID(ctx, id)
	if err != nil {
		return err
	}
	if existing.Builtin {
		return ErrBuiltinRole
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("role name is required")
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE roles SET name = ?, description = ? WHERE id = ?`, name, description, id); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrDuplicate
		}
		return err
	}
	if err := s.replaceRoleSections(ctx, id, sections); err != nil {
		return err
	}
	return s.replaceRoleHosts(ctx, id, hostIDs)
}

// DeleteRole removes a user-defined role and any assignments of it. Built-ins are
// refused.
func (s *Store) DeleteRole(ctx context.Context, id int64) error {
	existing, err := s.RoleByID(ctx, id)
	if err != nil {
		return err
	}
	if existing.Builtin {
		return ErrBuiltinRole
	}
	// The LDAP fallback is what a login falls back to when a mapping names a role
	// that no longer exists. Deleting the fallback would recreate that hole one
	// level up, so it has to be pointed elsewhere first.
	if cfg, err := s.GetLDAP(ctx); err == nil && cfg.FallbackRoleID == id {
		return ErrRoleInUseAsFallback
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM user_roles WHERE role_id = ?`, id); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM role_hosts WHERE role_id = ?`, id); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM roles WHERE id = ?`, id)
	return err
}

// ExistingRoleIDs filters ids down to the ones that still name a role, keeping
// the caller's order. Used when applying LDAP mappings, where an id can outlive
// the role it referred to.
func (s *Store) ExistingRoleIDs(ctx context.Context, ids []int64) ([]int64, error) {
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		switch _, err := s.RoleByID(ctx, id); {
		case errors.Is(err, ErrNotFound):
			continue
		case err != nil:
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// RoleIDsForUser returns the ids of the roles assigned to a user.
func (s *Store) RoleIDsForUser(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT role_id FROM user_roles WHERE user_id = ? ORDER BY role_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetUserRoles replaces a user's role assignments. Unknown role ids are dropped
// rather than erroring, so a stale UI can't wedge the form.
func (s *Store) SetUserRoles(ctx context.Context, userID int64, roleIDs []int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM user_roles WHERE user_id = ?`, userID); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, id := range roleIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		if _, err := s.RoleByID(ctx, id); err != nil {
			continue // unknown role: ignore
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO user_roles (user_id, role_id) VALUES (?, ?)`, userID, id); err != nil {
			return err
		}
	}
	return nil
}

// RolesForUser returns the roles assigned to a user, with their grants.
func (s *Store) RolesForUser(ctx context.Context, userID int64) ([]Role, error) {
	ids, err := s.RoleIDsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Role, 0, len(ids))
	for _, id := range ids {
		r, err := s.RoleByID(ctx, id)
		if err != nil {
			continue
		}
		out = append(out, *r)
	}
	return out, nil
}

// roleScopesForUser returns the host scope of every role the user holds, keyed by
// role id. A role with no rows is absent from the map, which callers read as
// "unscoped" — every host.
func (s *Store) roleScopesForUser(ctx context.Context, userID int64) (map[int64][]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT rh.role_id, rh.host_id
		FROM role_hosts rh
		JOIN user_roles ur ON ur.role_id = rh.role_id
		WHERE ur.user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var roleID, hostID int64
		if err := rows.Scan(&roleID, &hostID); err != nil {
			return nil, err
		}
		out[roleID] = append(out[roleID], hostID)
	}
	return out, rows.Err()
}

// EffectiveGrants computes a user's access per section: the union of their roles'
// grants and their own per-user section list, then capped.
//
//   - A per-user section (the pre-roles model) grants write unless the account is
//     read-only, which is exactly how it behaved before roles existed.
//   - A role section grants write only if the role says so.
//   - The user-level read-only flag caps everything to reads, so it keeps meaning
//     "this account cannot change anything" regardless of role.
//   - App-wide disabled sections are removed last: a feature turned off is off for
//     everyone (admins bypass this elsewhere, in checkAccess).
//
// Admins are not special-cased here; checkAccess short-circuits for them, and
// keeping this function purely about grants makes it testable on its own.
func (s *Store) EffectiveGrants(ctx context.Context, u *User) (map[string]Grant, error) {
	out := map[string]Grant{}
	if u == nil {
		return out, nil
	}
	// add records one grant. hosts == nil means the grant is unscoped (every
	// host); an empty-but-non-nil map would mean "no hosts", which never occurs
	// because a role with no host rows is unscoped by definition.
	add := func(section string, write bool, hosts []int64) {
		if !ValidSection(section) {
			return
		}
		g := out[section]
		g.Granted = true
		g.Write = g.Write || write
		if len(hosts) == 0 {
			g.AllHosts = true
		} else {
			if g.Hosts == nil {
				g.Hosts = map[int64]bool{}
			}
			for _, h := range hosts {
				g.Hosts[h] = true
			}
		}
		out[section] = g
	}
	for _, sec := range u.Sections {
		// A per-user section predates host scoping and carries no scope, so it
		// reaches every host — the same reach it had before. Scoping therefore only
		// bites for access that comes from a scoped role, which is what keeps the
		// migration from narrowing anyone.
		add(sec, true, nil) // write capped below when the account is read-only
	}
	// The role's host scope, loaded once per request rather than per role-section
	// row. A role absent from this map is unscoped.
	scopes, err := s.roleScopesForUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	// One join rather than RolesForUser: this runs on every gated request, and
	// per-role round trips made it O(number of roles) queries per request.
	rows, err := s.db.QueryContext(ctx, `
		SELECT rs.role_id, rs.section, rs.write
		FROM role_sections rs
		JOIN user_roles ur ON ur.role_id = rs.role_id
		WHERE ur.user_id = ?`, u.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var roleID int64
		var section string
		var write int
		if err := rows.Scan(&roleID, &section, &write); err != nil {
			return nil, err
		}
		add(section, write != 0, scopes[roleID])
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if u.ReadOnly {
		for sec, g := range out {
			g.Write = false
			out[sec] = g
		}
	}
	disabled, err := s.DisabledSections(ctx)
	if err != nil {
		return nil, err
	}
	for _, sec := range disabled {
		delete(out, sec)
	}
	return out, nil
}

// EffectiveSections lists the sections a user can reach, sorted — used for the
// users list and for LDAP section syncing.
func (s *Store) EffectiveSections(ctx context.Context, u *User) ([]string, error) {
	grants, err := s.EffectiveGrants(ctx, u)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(grants))
	for sec := range grants {
		out = append(out, sec)
	}
	sort.Strings(out)
	return out, nil
}
