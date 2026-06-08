import { useCallback, useEffect, useState } from "react";
import { Plus, Trash2, Shield, Eye, KeyRound, Pencil, Loader2, X } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { AppSettings, ManagedUser } from "../lib/types";
import { sectionLabel } from "../lib/sections";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";
import { useDialogs } from "../components/Dialog";

export function Users() {
  const dialogs = useDialogs();
  const [users, setUsers] = useState<ManagedUser[] | null>(null);
  const [allSections, setAllSections] = useState<string[]>([]);
  const [showForm, setShowForm] = useState(false);
  const [edit, setEdit] = useState<ManagedUser | null>(null);
  const [pwFor, setPwFor] = useState<ManagedUser | null>(null);
  const [err, setErr] = useState("");

  const load = useCallback(() => {
    api.users().then(setUsers).catch(() => setUsers([]));
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

  if (!users) return (<><PageHeader title="Users" /><div className="p-6 flex items-center gap-2 text-muted"><Spinner /> Loading…</div></>);

  return (
    <>
      <PageHeader title="Users" actions={<button className="btn-primary" onClick={() => { setEdit(null); setShowForm(true); }}><Plus className="h-4 w-4" /> New user</button>} />
      <div className="p-6 space-y-4">
        {err && <p className="text-sm text-danger">{err}</p>}
        {showForm && <UserForm allSections={allSections} onDone={() => { setShowForm(false); load(); }} />}
        {users.length === 0 ? (
          <EmptyState title="No users" />
        ) : (
          <div className="card overflow-hidden">
            <table className="w-full text-sm">
              <thead className="text-muted text-xs uppercase tracking-wide">
                <tr className="border-b border-border">
                  <th className="text-left font-medium px-4 py-3">User</th>
                  <th className="text-left font-medium px-4 py-3">Role</th>
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
                      ) : (
                        <span className="text-xs text-muted">user</span>
                      )}
                    </td>
                    <td className="px-4 py-2.5 text-xs text-muted">
                      {u.role === "admin" ? "everything" : (u.sections ?? []).map(sectionLabel).join(", ") || "—"}
                    </td>
                    <td className="px-4 py-2.5 hidden lg:table-cell text-xs">{u.totpEnabled ? <span className="text-ok">enabled</span> : <span className="text-muted">off</span>}</td>
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
      </div>
      {edit && <EditAccessModal user={edit} allSections={allSections} onClose={() => setEdit(null)} onDone={() => { setEdit(null); load(); }} />}
      {pwFor && <ResetPasswordModal user={pwFor} onClose={() => setPwFor(null)} />}
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

function UserForm({ allSections, onDone }: { allSections: string[]; onDone: () => void }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState("user");
  const [readOnly, setReadOnly] = useState(false);
  const [sections, setSections] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr("");
    try {
      const r = await api.createUser({ username, password, role, readOnly, sections: [...sections] });
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
          <label className="label">Role</label>
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
        <div>
          <label className="label">Allowed sections</label>
          <SectionPicker all={allSections} value={sections} onChange={setSections} />
        </div>
      )}
      {err && <p className="text-sm text-danger">{err}</p>}
      <div className="flex justify-end gap-2">
        <button type="button" className="btn-ghost" onClick={onDone}>Cancel</button>
        <button className="btn-primary" disabled={busy}>{busy ? "Creating…" : "Create user"}</button>
      </div>
    </form>
  );
}

function EditAccessModal({ user, allSections, onClose, onDone }: { user: ManagedUser; allSections: string[]; onClose: () => void; onDone: () => void }) {
  const [role, setRole] = useState(user.role);
  const [readOnly, setReadOnly] = useState(user.readOnly);
  const [sections, setSections] = useState<Set<string>>(new Set(user.sections ?? []));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr("");
    const r = await api.updateUser(user.id, { role, readOnly, sections: [...sections] });
    setBusy(false);
    if (r.ok) onDone(); else setErr(r.error ?? "failed");
  };

  return (
    <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <form className="card w-full max-w-xl" onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <div className="font-medium">Edit access — {user.username}</div>
          <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 space-y-3">
          <div className="grid grid-cols-2 gap-4">
            <div>
              <label className="label">Role</label>
              <select className="input" value={role} onChange={(e) => setRole(e.target.value)}>
                <option value="user">User</option>
                <option value="admin">Admin</option>
              </select>
            </div>
            <label className="flex items-center gap-2 text-sm self-end pb-2">
              <input type="checkbox" checked={readOnly} disabled={role === "admin"} onChange={(e) => setReadOnly(e.target.checked)} /> Read-only
            </label>
          </div>
          {role !== "admin" && <div><label className="label">Allowed sections</label><SectionPicker all={allSections} value={sections} onChange={setSections} /></div>}
          {err && <p className="text-sm text-danger">{err}</p>}
        </div>
        <div className="flex justify-end gap-2 p-4 border-t border-border">
          <button type="button" className="btn-ghost" onClick={onClose}>Cancel</button>
          <button className="btn-primary" disabled={busy}>{busy ? <Loader2 className="h-4 w-4 animate-spin" /> : null} Save</button>
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
    <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <form className="card w-full max-w-md" onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <div className="font-medium">Reset password — {user.username}</div>
          <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 space-y-3">
          <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder="new password (min 10 chars)" required />
          {msg && <p className={clsx("text-sm", msg.ok ? "text-ok" : "text-danger")}>{msg.text}</p>}
        </div>
        <div className="flex justify-end gap-2 p-4 border-t border-border">
          <button type="button" className="btn-ghost" onClick={onClose}>Close</button>
          <button className="btn-primary" disabled={busy || !password}>{busy ? <Loader2 className="h-4 w-4 animate-spin" /> : null} Update</button>
        </div>
      </form>
    </div>
  );
}
