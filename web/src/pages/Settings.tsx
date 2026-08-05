import { useCallback, useEffect, useState } from "react";
import { Loader2, ShieldOff, LayoutGrid, Network, Send, Plus, Trash2, Mail } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { LdapConfig, Role } from "../lib/types";
import { sectionLabel } from "../lib/sections";
import { PageHeader } from "../layout/Shell";
import { Spinner } from "../components/ui";
import { Tabs } from "../components/Tabs";
import { TokenPolicyEditor } from "../components/TokenPolicyEditor";
import { EmailConfig } from "../components/EmailConfig";

type Tab = "features" | "security" | "ldap" | "email";

export function Settings() {
  const [all, setAll] = useState<string[]>([]);
  const [disabled, setDisabled] = useState<Set<string>>(new Set());
  const [no2fa, setNo2fa] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  // Scoped to the card that saved. Both cards write through the same endpoint
  // (one settings payload), so a single message string meant pressing Save under
  // Features also lit up the 2FA card in Security — with Features' wording, which
  // is about nav changes and says nothing true about the exemption.
  const [msg, setMsg] = useState<{ scope: Tab; text: string } | null>(null);
  const [tab, setTab] = useState<Tab>("features");

  const load = useCallback(() => {
    api.settings().then((s) => {
      setAll(s.allSections);
      setDisabled(new Set(s.disabledSections ?? []));
      setNo2fa(s.localhostNo2fa);
      setLoaded(true);
    }).catch(() => setLoaded(true));
  }, []);
  useEffect(() => load(), [load]);

  const save = async (scope: Tab, okText: string) => {
    setBusy(true); setMsg(null);
    try {
      await api.setSettings({ disabledSections: [...disabled], localhostNo2fa: no2fa });
      setMsg({ scope, text: okText });
    } catch {
      setMsg({ scope, text: "Save failed" });
    } finally { setBusy(false); }
  };

  if (!loaded) return (<><PageHeader title="Settings" /><div className="p-6 flex items-center gap-2 text-muted"><Spinner /> Loading…</div></>);

  return (
    <>
      <PageHeader title="Settings" />
      <div className="p-6 space-y-4">
        <Tabs
          active={tab}
          onChange={setTab}
          tabs={[
            { key: "features", label: "Features", icon: <LayoutGrid className="h-4 w-4" />, count: all.length - disabled.size },
            { key: "security", label: "Security", icon: <ShieldOff className="h-4 w-4" /> },
            { key: "ldap", label: "LDAP", icon: <Network className="h-4 w-4" /> },
            { key: "email", label: "Email", icon: <Mail className="h-4 w-4" /> },
          ]}
        />

        {tab === "features" && (
          <div className="space-y-4 max-w-2xl">
            <div className="card p-5 space-y-3">
              <div className="flex items-center gap-2 font-medium"><LayoutGrid className="h-4 w-4 text-accent" /> Enabled features</div>
              <p className="text-xs text-muted">Turn off whole sections the team doesn&apos;t need. Disabled sections are hidden from the menu and their APIs are blocked for everyone.</p>
              <div className="grid grid-cols-2 md:grid-cols-3 gap-1.5">
                {all.map((s) => {
                  const enabled = !disabled.has(s);
                  return (
                    <label key={s} className="flex items-center gap-2 text-sm">
                      <input type="checkbox" checked={enabled} onChange={(e) => {
                        const n = new Set(disabled);
                        e.target.checked ? n.delete(s) : n.add(s);
                        setDisabled(n);
                      }} />
                      <span className={clsx(!enabled && "text-muted line-through")}>{sectionLabel(s)}</span>
                    </label>
                  );
                })}
              </div>
              <div className="flex items-center justify-end gap-3 pt-1">
                {msg?.scope === "features" && <span className="text-sm text-ok">{msg.text}</span>}
                <button
                  className="btn-primary"
                  onClick={() => save("features", "Saved. Users may need to reload for nav changes to apply.")}
                  disabled={busy}
                >{busy ? <Loader2 className="h-4 w-4 animate-spin" /> : null} Save settings</button>
              </div>
            </div>
          </div>
        )}

        {tab === "security" && (
          <div className="space-y-4 max-w-2xl">
            <div className="card p-5 space-y-3">
              <div className="flex items-center gap-2 font-medium"><ShieldOff className="h-4 w-4 text-warn" /> Localhost 2FA exemption</div>
              <label className="flex items-start gap-2 text-sm">
                <input type="checkbox" checked={no2fa} onChange={(e) => setNo2fa(e.target.checked)} className="mt-1" />
                <span>
                  Allow password-only login from <code>localhost</code> (loopback).
                  <span className="block text-xs text-muted mt-0.5">When on, connections from 127.0.0.1/::1 skip the mandatory 2FA enrollment and challenge. Remote connections always require 2FA. Leave off for server deployments.</span>
                </span>
              </label>
              <div className="flex items-center justify-end gap-3 pt-1">
                {msg?.scope === "security" && <span className="text-sm text-ok">{msg.text}</span>}
                <button
                  className="btn-primary"
                  onClick={() => save("security", "Saved. It applies to the next sign-in.")}
                  disabled={busy}
                >{busy ? <Loader2 className="h-4 w-4 animate-spin" /> : null} Save settings</button>
              </div>
            </div>

            {/* MCP token lifetimes are the same kind of thing as the exemption
                above: an instance-wide rule about how credentials work here. The
                MCP admin page stays the operational view — who holds a token,
                and revoking it. */}
            <TokenPolicyEditor />
          </div>
        )}

        {tab === "ldap" && <div className="max-w-2xl"><LdapSettings allSections={all} /></div>}

        {tab === "email" && (
          <div className="max-w-2xl space-y-3">
            <p className="text-xs text-muted">
              One outbound mail relay for the whole installation — used by alert rules and by system
              notifications. Configuring it is admin-only; the password is encrypted at rest and never
              returned by the API.
            </p>
            <EmailConfig />
          </div>
        )}
      </div>
    </>
  );
}

// LdapSettings configures optional LDAP / Active Directory authentication.
function LdapSettings({ allSections }: { allSections: string[] }) {
  const [cfg, setCfg] = useState<LdapConfig | null>(null);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState<"" | "save" | "test">("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);

  const [roles, setRoles] = useState<Role[]>([]);

  const load = useCallback(() => {
    api.ldap().then(setCfg).catch(() => setCfg({ enabled: false, url: "", startTls: false, bindDn: "", userBaseDn: "", userFilter: "(uid=%s)", adminGroupDn: "", groupMappings: [] }));
  }, []);
  useEffect(() => load(), [load]);
  useEffect(() => { api.roles().then(setRoles).catch(() => setRoles([])); }, []);
  if (!cfg) return null;
  const patch = (p: Partial<LdapConfig>) => setCfg({ ...cfg, ...p });
  const mappings = cfg.groupMappings ?? [];
  const setMappings = (m: typeof mappings) => patch({ groupMappings: m });
  const addMapping = () => setMappings([...mappings, { groupDn: "", sections: [], roleIds: [] }]);
  const updateMapping = (i: number, p: Partial<(typeof mappings)[number]>) => setMappings(mappings.map((m, j) => (j === i ? { ...m, ...p } : m)));
  const removeMapping = (i: number) => setMappings(mappings.filter((_, j) => j !== i));
  const toggleSection = (i: number, sec: string) => {
    const cur = mappings[i].sections;
    updateMapping(i, { sections: cur.includes(sec) ? cur.filter((s) => s !== sec) : [...cur, sec] });
  };
  const toggleRole = (i: number, id: number) => {
    const cur = mappings[i].roleIds ?? [];
    updateMapping(i, { roleIds: cur.includes(id) ? cur.filter((r) => r !== id) : [...cur, id] });
  };
  // Roles are only authoritative once at least one mapping hands one out, so the
  // hint below has to say which of the two modes the current config is in.
  const mapsRoles = mappings.some((m) => (m.roleIds ?? []).length > 0);

  const run = async (kind: "save" | "test") => {
    setBusy(kind); setMsg(null);
    try {
      if (kind === "save") { await api.setLdap({ ...cfg, bindPassword: password }); setPassword(""); setMsg({ ok: true, text: "Saved." }); load(); }
      else { const r = await api.testLdap({ ...cfg, bindPassword: password }); setMsg(r.ok ? { ok: true, text: `Connection OK (${r.entries} entries under base).` } : { ok: false, text: r.error ?? "test failed" }); }
    } catch { setMsg({ ok: false, text: "request failed" }); } finally { setBusy(""); }
  };

  return (
    <div className="card p-5 space-y-3">
      <div className="flex items-center gap-2 font-medium"><Network className="h-4 w-4 text-accent" /> LDAP / Active Directory</div>
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={cfg.enabled} onChange={(e) => patch({ enabled: e.target.checked })} /> Enable LDAP authentication
      </label>
      <p className="text-xs text-muted">Users not found locally are authenticated against LDAP and provisioned as local accounts (so you can grant sections). Local admin accounts always use their local password. The bind password is encrypted at rest.</p>
      <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
        <div><label className="label">Server URL</label><input className="input font-mono" value={cfg.url} onChange={(e) => patch({ url: e.target.value })} placeholder="ldap://ldap.example.com:389" /></div>
        <label className="flex items-center gap-2 text-sm self-end pb-2"><input type="checkbox" checked={cfg.startTls} onChange={(e) => patch({ startTls: e.target.checked })} /> StartTLS</label>
        <div><label className="label">Bind DN (service account)</label><input className="input font-mono" value={cfg.bindDn} onChange={(e) => patch({ bindDn: e.target.value })} placeholder="cn=readonly,dc=example,dc=com" /></div>
        <div><label className="label">Bind password {cfg.hasBindPassword && <span className="text-ok">(stored)</span>}</label><input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder={cfg.hasBindPassword ? "•••••• (unchanged)" : ""} autoComplete="new-password" /></div>
        <div><label className="label">User base DN</label><input className="input font-mono" value={cfg.userBaseDn} onChange={(e) => patch({ userBaseDn: e.target.value })} placeholder="ou=people,dc=example,dc=com" /></div>
        <div><label className="label">User filter (must contain %s)</label><input className="input font-mono" value={cfg.userFilter} onChange={(e) => patch({ userFilter: e.target.value })} placeholder="(uid=%s)" /></div>
        <div className="md:col-span-2"><label className="label">Admin group DN (optional — members become admins)</label><input className="input font-mono" value={cfg.adminGroupDn} onChange={(e) => patch({ adminGroupDn: e.target.value })} placeholder="cn=admins,ou=groups,dc=example,dc=com" /></div>
      </div>

      <div className="space-y-2 border-t border-border pt-3">
        <div className="flex items-center justify-between">
          <label className="label mb-0">Group mappings (optional)</label>
          <button className="btn-ghost px-2 py-1 text-xs" onClick={addMapping}><Plus className="h-3.5 w-3.5" /> Add mapping</button>
        </div>
        <p className="text-xs text-muted">
          Grant access by LDAP group membership. Prefer <b>roles</b> — the sections below predate them
          and are still applied for configs that use them. When any mapping is set, group membership is
          authoritative for non-admin users' sections, re-synced on each login (manual section edits are
          overwritten). {mapsRoles
            ? "Because a mapping grants a role, roles are re-synced from the directory too — roles assigned by hand on an account are replaced on the next login."
            : "No mapping grants a role yet, so roles assigned by hand on an account are left alone."}{" "}
          Admins (via the admin group) see everything regardless.
        </p>
        {mappings.map((m, i) => (
          <div key={i} className="rounded-md border border-border p-3 space-y-2">
            <div className="flex items-center gap-2">
              <input className="input font-mono flex-1" value={m.groupDn} onChange={(e) => updateMapping(i, { groupDn: e.target.value })} placeholder="cn=devops,ou=groups,dc=example,dc=com" />
              <button className="btn-ghost px-2 py-1 text-danger" title="Remove mapping" onClick={() => removeMapping(i)}><Trash2 className="h-4 w-4" /></button>
            </div>
            <div className="space-y-1">
              <div className="text-[11px] uppercase tracking-wide text-muted">Roles</div>
              {roles.length === 0 ? (
                <p className="text-xs text-muted">No roles defined yet — create one under <b>Users → Roles</b>.</p>
              ) : (
                <div className="flex flex-wrap gap-1.5">
                  {roles.map((r) => (
                    <button
                      key={r.id}
                      onClick={() => toggleRole(i, r.id)}
                      title={r.description}
                      className={clsx("text-xs px-2 py-0.5 rounded-md border", (m.roleIds ?? []).includes(r.id) ? "bg-accent/20 border-accent/40 text-text" : "border-border text-muted")}
                    >
                      {r.name}
                    </button>
                  ))}
                </div>
              )}
            </div>
            <div className="space-y-1">
              <div className="text-[11px] uppercase tracking-wide text-muted">Sections</div>
              <div className="flex flex-wrap gap-1.5">
                {allSections.map((sec) => (
                  <button
                    key={sec}
                    onClick={() => toggleSection(i, sec)}
                    className={clsx("text-xs px-2 py-0.5 rounded-md border capitalize", m.sections.includes(sec) ? "bg-accent/20 border-accent/40 text-text" : "border-border text-muted")}
                  >
                    {sectionLabel(sec)}
                  </button>
                ))}
              </div>
            </div>
          </div>
        ))}
        {mapsRoles && (
          <div className="pt-1">
            <label className="label">Fallback role</label>
            <select
              className="input"
              value={cfg.fallbackRoleId ?? 0}
              onChange={(e) => patch({ fallbackRoleId: Number(e.target.value) })}
            >
              <option value={0}>None — members of a broken mapping get no role</option>
              {roles.map((r) => (
                <option key={r.id} value={r.id}>{r.name}{r.builtin ? " (built-in)" : ""}</option>
              ))}
            </select>
            <p className="text-xs text-muted mt-1">
              Granted in place of a mapped role that no longer exists, so deleting a role degrades its
              members to a known baseline instead of leaving them with nothing. It does <b>not</b> apply
              to users whose groups map to no role at all — that would hand a role to everyone in the
              directory. The chosen role can&apos;t be deleted while it&apos;s the fallback.
            </p>
          </div>
        )}
      </div>

      {msg && <p className={clsx("text-sm", msg.ok ? "text-ok" : "text-danger")}>{msg.text}</p>}
      <div className="flex justify-end gap-2">
        <button className="btn-ghost" onClick={() => run("test")} disabled={busy !== ""}>{busy === "test" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Send className="h-4 w-4" />} Test</button>
        <button className="btn-primary" onClick={() => run("save")} disabled={busy !== ""}>{busy === "save" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Network className="h-4 w-4" />} Save LDAP</button>
      </div>
    </div>
  );
}
