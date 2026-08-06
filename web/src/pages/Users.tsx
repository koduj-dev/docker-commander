import { useCallback, useEffect, useState } from "react";
import { Plus, Trash2, Shield, Eye, KeyRound, Pencil, Loader2, X, Copy, IdCard, Users as UsersIcon, Save } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { AppSettings, Host, ManagedUser, Role, RoleSection } from "../lib/types";
import { sectionLabel } from "../lib/sections";
import { canWriteAnywhere, describeAccess, roleSummary, rolesForUser, sectionState, toggleSection } from "../lib/roles";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";
import { Tabs } from "../components/Tabs";
import { useDialogs } from "../components/Dialog";

type Tab = "accounts" | "roles";

export function Users() {
  const dialogs = useDialogs();
  const [users, setUsers] = useState<ManagedUser[] | null>(null);
  const [roles, setRoles] = useState<Role[]>([]);
  const [allSections, setAllSections] = useState<string[]>([]);
  const [tab, setTab] = useState<Tab>("accounts");
  const [showForm, setShowForm] = useState(false);
  const [edit, setEdit] = useState<ManagedUser | null>(null);
  const [pwFor, setPwFor] = useState<ManagedUser | null>(null);
  const [roleEdit, setRoleEdit] = useState<Role | "new" | null>(null);
  const [err, setErr] = useState("");

  const load = useCallback(() => {
    api.users().then(setUsers).catch(() => setUsers([]));
    api.roles().then(setRoles).catch(() => setRoles([]));
    api.settings().then((s: AppSettings) => setAllSections(s.allSections)).catch(() => {});
  }, []);
  useEffect(() => load(), [load]);

  const del = async (u: ManagedUser) => {
    if (!(await dialogs.confirm({ title: "Delete user", message: <>Delete the account <code className="font-mono text-text">{u.username}</code>?</>, danger: true, confirmLabel: "Delete" }))) return;
    setErr("");
    const r = await api.deleteUser(u.id);
    if (!r.ok) setErr(r.error ?? "could not delete");
    load();
  };

  const delRole = async (r: Role) => {
    const held = r.users ?? 0;
    if (!(await dialogs.confirm({
      title: `Delete role "${r.name}"?`,
      message: held > 0
        ? `${held} account(s) hold this role and will lose the access it grants. They keep any sections granted directly on the account.`
        : "No accounts hold this role.",
      danger: true,
      confirmLabel: "Delete role",
    }))) return;
    setErr("");
    try {
      await api.deleteRole(r.id);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "could not delete the role");
    }
    load();
  };

  const dupRole = async (r: Role) => {
    setErr("");
    try {
      const res = await api.duplicateRole(r.id);
      load();
      // Open the copy straight away — duplicating is only ever a step toward editing.
      const fresh = await api.roles();
      setRoles(fresh);
      const copy = fresh.find((x) => x.id === res.id);
      if (copy) setRoleEdit(copy);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "could not duplicate the role");
    }
  };

  if (!users) return (<><PageHeader title="Users & roles" /><div className="p-6 flex items-center gap-2 text-muted"><Spinner /> Loading…</div></>);

  const action = tab === "accounts"
    ? <button className="btn-primary" onClick={() => { setEdit(null); setShowForm(true); }}><Plus className="h-4 w-4" /> New user</button>
    : <button className="btn-primary" onClick={() => setRoleEdit("new")}><Plus className="h-4 w-4" /> New role</button>;

  return (
    <>
      <PageHeader title="Users & roles" actions={action} />
      <div className="p-6 space-y-4">
        <Tabs
          active={tab}
          onChange={setTab}
          tabs={[
            { key: "accounts", label: "Accounts", icon: <UsersIcon className="h-4 w-4" />, count: users.length },
            { key: "roles", label: "Roles", icon: <IdCard className="h-4 w-4" />, count: roles.length },
          ]}
        />
        {err && <p className="text-sm text-danger">{err}</p>}

        {tab === "accounts" && (
          <>
            {showForm && <UserForm allSections={allSections} roles={roles} onDone={() => { setShowForm(false); load(); }} />}
            {users.length === 0 ? (
              <EmptyState title="No users" />
            ) : (
              <div className="card overflow-hidden">
                <table className="w-full text-sm">
                  <thead className="text-muted text-xs uppercase tracking-wide">
                    <tr className="border-b border-border">
                      <th className="text-left font-medium px-4 py-3">User</th>
                      <th className="text-left font-medium px-4 py-3">Type</th>
                      <th className="text-left font-medium px-4 py-3">Roles</th>
                      <th className="text-left font-medium px-4 py-3">Access</th>
                      <th className="text-left font-medium px-4 py-3 hidden lg:table-cell">2FA</th>
                      <th className="px-4 py-3"></th>
                    </tr>
                  </thead>
                  <tbody>
                    {users.map((u) => (
                      <tr key={u.id} className="border-b border-border/50">
                        <td className="px-4 py-2.5 font-medium">{u.username}</td>
                        <td className="px-4 py-2.5">
                          {u.role === "admin" ? (
                            <span className="inline-flex items-center gap-1 text-xs text-accent"><Shield className="h-3.5 w-3.5" /> admin</span>
                          ) : u.readOnly ? (
                            <span className="inline-flex items-center gap-1 text-xs text-warn"><Eye className="h-3.5 w-3.5" /> read-only</span>
                          ) : canWriteAnywhere(u, roles) ? (
                            <span className="text-xs text-muted">user</span>
                          ) : (
                            // Not flagged read-only, but nothing it holds grants write.
                            <span className="inline-flex items-center gap-1 text-xs text-muted" title="No writable grant">user · view only</span>
                          )}
                        </td>
                        <td className="px-4 py-2.5 text-xs">
                          {u.role === "admin" ? <span className="text-muted">—</span> : (
                            rolesForUser(u, roles).length === 0
                              ? <span className="text-muted">—</span>
                              : <span className="flex flex-wrap gap-1">{rolesForUser(u, roles).map((r) => (
                                  <span key={r.id} className="text-[10px] border border-border rounded px-1 py-0.5">{r.name}</span>
                                ))}</span>
                          )}
                        </td>
                        <td className="px-4 py-2.5 text-xs text-muted max-w-[24rem]">{describeAccess(u, roles)}</td>
                        {/* "Is this account protected?", which is not the same
                            question as "does it have an authenticator app": an
                            account holding only a passkey has totpEnabled=false
                            and is protected. Reading the wrong field told an
                            admin auditing their users that it was off. */}
                        <td className="px-4 py-2.5 hidden lg:table-cell text-xs">
                          {u.mfaEnabled ?? u.totpEnabled
                            ? <span className="text-ok">{u.totpEnabled ? "enabled" : "passkey"}</span>
                            : <span className="text-muted">off</span>}
                        </td>
                        <td className="px-4 py-2.5">
                          <div className="flex items-center justify-end gap-1">
                            <button className="btn-ghost px-2 py-1" title="Edit access" onClick={() => { setShowForm(false); setEdit(u); }}><Pencil className="h-4 w-4" /></button>
                            <button className="btn-ghost px-2 py-1" title="Reset password" onClick={() => setPwFor(u)}><KeyRound className="h-4 w-4" /></button>
                            <button className="btn-ghost px-2 py-1 text-danger" title="Delete" onClick={() => del(u)}><Trash2 className="h-4 w-4" /></button>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}

        {tab === "roles" && (
          <>
            <p className="text-xs text-muted">
              A role bundles section grants so you don&apos;t tick every checkbox per account. Each section is
              read-only or writable. The two built-in roles can&apos;t be edited — <strong>duplicate</strong> one to
              customise it. An account&apos;s read-only flag still caps everything a role grants.
            </p>
            {roles.length === 0 ? (
              <EmptyState title="No roles" hint="Create a role to grant a reusable bundle of sections." />
            ) : (
              <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
                {roles.map((r) => (
                  <div key={r.id} className="card p-3 flex items-start gap-3">
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="font-medium truncate">{r.name}</span>
                        {r.builtin
                          ? <span className="text-[10px] uppercase tracking-wide text-muted border border-border rounded px-1">built-in</span>
                          : <span className="text-[10px] uppercase tracking-wide text-accent border border-accent/40 rounded px-1">yours</span>}
                        {(r.users ?? 0) > 0 && <span className="text-[10px] text-muted">{r.users} user{r.users === 1 ? "" : "s"}</span>}
                      </div>
                      {r.description && <div className="text-xs text-muted mt-0.5">{r.description}</div>}
                      <div className="text-xs text-muted mt-1">{roleSummary(r)}</div>
                    </div>
                    <div className="shrink-0 flex items-center gap-1">
                      <button className="btn-ghost px-2 py-1" title={r.builtin ? "View" : "Edit"} onClick={() => setRoleEdit(r)}><Pencil className="h-4 w-4" /></button>
                      <button className="btn-ghost px-2 py-1" title="Duplicate" onClick={() => dupRole(r)}><Copy className="h-4 w-4" /></button>
                      {!r.builtin && (
                        <button className="btn-ghost px-2 py-1 text-danger" title="Delete" onClick={() => delRole(r)}><Trash2 className="h-4 w-4" /></button>
                      )}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </>
        )}
      </div>
      {edit && <EditAccessModal user={edit} allSections={allSections} roles={roles} onClose={() => setEdit(null)} onDone={() => { setEdit(null); load(); }} />}
      {pwFor && <ResetPasswordModal user={pwFor} onClose={() => setPwFor(null)} />}
      {roleEdit && <RoleEditorModal role={roleEdit === "new" ? null : roleEdit} allSections={allSections} onClose={() => setRoleEdit(null)} onDone={() => { setRoleEdit(null); load(); }} />}
    </>
  );
}

// SectionPicker is a checkbox grid of sections, shared by create + edit.
function SectionPicker({ all, value, onChange, disabled }: { all: string[]; value: Set<string>; onChange: (s: Set<string>) => void; disabled?: boolean }) {
  return (
    <div className={clsx("grid grid-cols-2 md:grid-cols-3 gap-1.5", disabled && "opacity-40 pointer-events-none")}>
      {all.map((s) => (
        <label key={s} className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={value.has(s)} onChange={(e) => { const n = new Set(value); e.target.checked ? n.add(s) : n.delete(s); onChange(n); }} />
          {sectionLabel(s)}
        </label>
      ))}
    </div>
  );
}

// RolePicker assigns named roles to an account.
function RolePicker({ roles, value, onChange }: { roles: Role[]; value: Set<number>; onChange: (s: Set<number>) => void }) {
  if (roles.length === 0) return <p className="text-xs text-muted">No roles defined yet — create one on the Roles tab.</p>;
  return (
    <div className="grid grid-cols-1 md:grid-cols-2 gap-1.5">
      {roles.map((r) => (
        <label key={r.id} className="flex items-start gap-2 text-sm">
          <input type="checkbox" className="mt-1" checked={value.has(r.id)}
            onChange={(e) => { const n = new Set(value); e.target.checked ? n.add(r.id) : n.delete(r.id); onChange(n); }} />
          <span>
            {r.name}
            <span className="block text-xs text-muted mt-0.5">{roleSummary(r)}</span>
          </span>
        </label>
      ))}
    </div>
  );
}

function UserForm({ allSections, roles, onDone }: { allSections: string[]; roles: Role[]; onDone: () => void }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState("user");
  const [readOnly, setReadOnly] = useState(false);
  const [sections, setSections] = useState<Set<string>>(new Set());
  const [roleIds, setRoleIds] = useState<Set<number>>(new Set());
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr("");
    try {
      const r = await api.createUser({ username, password, role, readOnly, sections: [...sections], roleIds: [...roleIds] });
      if (r.ok) onDone();
      else setErr(r.error ?? "failed");
    } catch (e) { setErr(e instanceof Error ? e.message : "failed"); } finally { setBusy(false); }
  };

  return (
    <form onSubmit={submit} className="card p-5 space-y-4">
      <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
        <div>
          <label className="label">Username</label>
          <input className="input" value={username} onChange={(e) => setUsername(e.target.value)} required />
        </div>
        <div>
          <label className="label">Password</label>
          <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder="min 10 chars" required />
        </div>
        <div>
          <label className="label">Account type</label>
          <select className="input" value={role} onChange={(e) => setRole(e.target.value)}>
            <option value="user">User</option>
            <option value="admin">Admin</option>
          </select>
        </div>
        <label className="flex items-center gap-2 text-sm self-end pb-2">
          <input type="checkbox" checked={readOnly} disabled={role === "admin"} onChange={(e) => setReadOnly(e.target.checked)} /> Read-only
        </label>
      </div>
      {role !== "admin" && (
        <>
          <div>
            <label className="label">Roles</label>
            <RolePicker roles={roles} value={roleIds} onChange={setRoleIds} />
          </div>
          <div>
            <label className="label">Extra sections (on top of any roles)</label>
            <SectionPicker all={allSections} value={sections} onChange={setSections} />
          </div>
        </>
      )}
      {err && <p className="text-sm text-danger">{err}</p>}
      <div className="flex justify-end gap-2">
        <button type="button" className="btn-ghost" onClick={onDone}>Cancel</button>
        <button className="btn-primary" disabled={busy}>{busy ? "Creating…" : "Create user"}</button>
      </div>
    </form>
  );
}

function EditAccessModal({ user, allSections, roles, onClose, onDone }: { user: ManagedUser; allSections: string[]; roles: Role[]; onClose: () => void; onDone: () => void }) {
  const [role, setRole] = useState(user.role);
  const [readOnly, setReadOnly] = useState(user.readOnly);
  const [sections, setSections] = useState<Set<string>>(new Set(user.sections ?? []));
  const [roleIds, setRoleIds] = useState<Set<number>>(new Set(user.roleIds ?? []));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr("");
    // roleIds is always sent, so clearing every checkbox actually unassigns.
    const r = await api.updateUser(user.id, { role, readOnly, sections: [...sections], roleIds: [...roleIds] });
    setBusy(false);
    if (r.ok) onDone(); else setErr(r.error ?? "failed");
  };

  return (
    <div className="fixed inset-0 z-[55] bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <form className="card w-full max-w-2xl flex flex-col max-h-[88vh]" onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <IdCard className="h-4 w-4 text-accent" />
          <div className="font-medium">Edit access — {user.username}</div>
          <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 space-y-3 overflow-y-auto">
          <div className="grid grid-cols-2 gap-4">
            <div>
              <label className="label">Account type</label>
              <select className="input" value={role} onChange={(e) => setRole(e.target.value)}>
                <option value="user">User</option>
                <option value="admin">Admin</option>
              </select>
            </div>
            <label className="flex items-center gap-2 text-sm self-end pb-2">
              <input type="checkbox" checked={readOnly} disabled={role === "admin"} onChange={(e) => setReadOnly(e.target.checked)} /> Read-only
            </label>
          </div>
          {role !== "admin" && (
            <>
              <div>
                <label className="label">Roles</label>
                <RolePicker roles={roles} value={roleIds} onChange={setRoleIds} />
              </div>
              <div>
                <label className="label">Extra sections (on top of any roles)</label>
                <SectionPicker all={allSections} value={sections} onChange={setSections} />
              </div>
              {readOnly && (
                <p className="text-xs text-warn">
                  This account is read-only, so every grant above — including writable roles — is capped to reads.
                </p>
              )}
            </>
          )}
          {err && <p className="text-sm text-danger">{err}</p>}
        </div>
        <div className="flex justify-end gap-2 p-4 border-t border-border">
          <button type="button" className="btn-ghost px-3 py-1.5 text-sm" onClick={onClose}>Cancel</button>
          <button className="btn-primary px-3 py-1.5 text-sm disabled:opacity-40" disabled={busy}>{busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />} Save</button>
        </div>
      </form>
    </div>
  );
}

// RoleEditorModal creates or edits a role. Built-ins open read-only with a Close
// footer — the card list offers Duplicate to make an editable copy.
function RoleEditorModal({ role, allSections, onClose, onDone }: { role: Role | null; allSections: string[]; onClose: () => void; onDone: () => void }) {
  const readOnly = !!role?.builtin;
  const [name, setName] = useState(role?.name ?? "");
  const [description, setDescription] = useState(role?.description ?? "");
  const [sections, setSections] = useState<RoleSection[]>(role?.sections ?? []);
  const [hostIds, setHostIds] = useState<number[]>(role?.hostIds ?? []);
  const [hosts, setHosts] = useState<Host[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => { api.hosts().then(setHosts).catch(() => setHosts([])); }, []);
  // Functional form: deriving from the render's `hostIds` loses updates when two
  // toggles land in the same React batch.
  const toggleHost = (id: number) =>
    setHostIds((prev) => (prev.includes(id) ? prev.filter((h) => h !== id) : [...prev, id]));

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (readOnly) { onClose(); return; }
    setBusy(true); setErr("");
    try {
      if (role) await api.updateRole(role.id, { name, description, sections, hostIds });
      else await api.createRole({ name, description, sections, hostIds });
      onDone();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "failed");
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-[55] bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <form className="card w-full max-w-3xl flex flex-col max-h-[88vh]" onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <IdCard className="h-4 w-4 text-accent" />
          <div className="font-medium">
            {readOnly ? `Role — ${role?.name}` : role ? `Edit role — ${role.name}` : "New role"}
          </div>
          {readOnly && <span className="text-[10px] uppercase tracking-wide text-muted border border-border rounded px-1">built-in</span>}
          <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 space-y-3 overflow-y-auto">
          {readOnly && (
            <p className="text-xs text-muted">
              Built-in roles can&apos;t be changed, so the known-good baseline stays intact. Use <strong>Duplicate</strong> on
              the Roles tab to get an editable copy.
            </p>
          )}
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div>
              <label className="label">Name</label>
              <input className="input" value={name} readOnly={readOnly} onChange={(e) => setName(e.target.value)} required />
            </div>
            <div>
              <label className="label">Description</label>
              <input className="input" value={description} readOnly={readOnly} onChange={(e) => setDescription(e.target.value)} placeholder="what this role is for" />
            </div>
          </div>
          <div>
            <label className="label">Sections</label>
            <div className={clsx("grid grid-cols-1 md:grid-cols-2 gap-x-6 gap-y-1", readOnly && "opacity-70 pointer-events-none")}>
              {allSections.map((s) => {
                const state = sectionState(sections, s);
                return (
                  <div key={s} className="flex items-center gap-2 text-sm">
                    <span className="flex-1 min-w-0 truncate">{sectionLabel(s)}</span>
                    <div className="flex gap-1 rounded-lg bg-panel2/50 p-0.5 shrink-0">
                      {(["none", "read", "write"] as const).map((opt) => (
                        <button
                          key={opt}
                          type="button"
                          className={clsx("px-2 py-1 rounded-md text-xs capitalize",
                            state === opt ? "bg-panel text-text shadow-sm" : "text-muted hover:text-text")}
                          onClick={() => setSections(toggleSection(sections, s, opt))}
                        >
                          {opt === "none" ? "—" : opt}
                        </button>
                      ))}
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
          <div>
            <label className="label">Hosts</label>
            <p className="text-xs text-muted mb-1.5">
              {hostIds.length === 0
                ? "This role applies to EVERY Docker host. Pick hosts to limit it."
                : "This role applies only to the selected hosts. The local daemon is always included — a single-host install must not be able to lock itself out."}
            </p>
            <div className={clsx("flex flex-wrap gap-1.5", readOnly && "opacity-70 pointer-events-none")}>
              {hosts.filter((h) => h.id > 0).length === 0 ? (
                <span className="text-xs text-muted">No remote hosts configured — there is nothing to scope to yet.</span>
              ) : hosts.filter((h) => h.id > 0).map((h) => (
                <button
                  key={h.id}
                  type="button"
                  onClick={() => toggleHost(h.id)}
                  className={clsx("text-xs px-2 py-0.5 rounded-md border",
                    hostIds.includes(h.id) ? "bg-accent/20 border-accent/40 text-text" : "border-border text-muted")}
                >
                  {h.name}
                </button>
              ))}
            </div>
          </div>
          {err && <p className="text-sm text-danger">{err}</p>}
        </div>
        <div className="flex justify-end gap-2 p-4 border-t border-border">
          {readOnly ? (
            <button type="button" className="btn-ghost px-3 py-1.5 text-sm" onClick={onClose}>Close</button>
          ) : (
            <>
              <button type="button" className="btn-ghost px-3 py-1.5 text-sm" onClick={onClose}>Cancel</button>
              <button className="btn-primary px-3 py-1.5 text-sm disabled:opacity-40" disabled={busy || !name.trim()}>
                {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />} Save
              </button>
            </>
          )}
        </div>
      </form>
    </div>
  );
}

function ResetPasswordModal({ user, onClose }: { user: ManagedUser; onClose: () => void }) {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const r = await api.resetUserPassword(user.id, password);
      setMsg(r.ok ? { ok: true, text: "Password updated." } : { ok: false, text: r.error ?? "failed" });
      if (r.ok) setPassword("");
    } catch (e) { setMsg({ ok: false, text: e instanceof Error ? e.message : "failed" }); } finally { setBusy(false); }
  };

  return (
    <div className="fixed inset-0 z-[55] bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <form className="card w-full max-w-lg" onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <KeyRound className="h-4 w-4 text-accent" />
          <div className="font-medium">Reset password — {user.username}</div>
          <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 space-y-3">
          <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder="new password (min 10 chars)" required />
          {msg && <p className={clsx("text-sm", msg.ok ? "text-ok" : "text-danger")}>{msg.text}</p>}
        </div>
        <div className="flex justify-end gap-2 p-4 border-t border-border">
          <button type="button" className="btn-ghost px-3 py-1.5 text-sm" onClick={onClose}>Close</button>
          <button className="btn-primary px-3 py-1.5 text-sm disabled:opacity-40" disabled={busy || !password}>{busy ? <Loader2 className="h-4 w-4 animate-spin" /> : null} Update</button>
        </div>
      </form>
    </div>
  );
}
