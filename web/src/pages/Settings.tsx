import { useCallback, useEffect, useState } from "react";
import { Loader2, ShieldOff, LayoutGrid, Network, Send } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { LdapConfig } from "../lib/types";
import { sectionLabel } from "../lib/sections";
import { PageHeader } from "../layout/Shell";
import { Spinner } from "../components/ui";

export function Settings() {
  const [all, setAll] = useState<string[]>([]);
  const [disabled, setDisabled] = useState<Set<string>>(new Set());
  const [no2fa, setNo2fa] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");

  const load = useCallback(() => {
    api.settings().then((s) => {
      setAll(s.allSections);
      setDisabled(new Set(s.disabledSections ?? []));
      setNo2fa(s.localhostNo2fa);
      setLoaded(true);
    }).catch(() => setLoaded(true));
  }, []);
  useEffect(() => load(), [load]);

  const save = async () => {
    setBusy(true); setMsg("");
    try {
      await api.setSettings({ disabledSections: [...disabled], localhostNo2fa: no2fa });
      setMsg("Saved. Users may need to reload for nav changes to apply.");
    } catch {
      setMsg("Save failed");
    } finally { setBusy(false); }
  };

  if (!loaded) return (<><PageHeader title="Settings" /><div className="p-6 flex items-center gap-2 text-muted"><Spinner /> Loading…</div></>);

  return (
    <>
      <PageHeader title="Settings" />
      <div className="p-6 space-y-6 max-w-2xl">
        {/* Feature flags */}
        <div className="card p-5 space-y-3">
          <div className="flex items-center gap-2 font-medium"><LayoutGrid className="h-4 w-4 text-accent" /> Enabled features</div>
          <p className="text-xs text-muted">Turn off whole sections the team doesn't need. Disabled sections are hidden from the menu and their APIs are blocked for everyone.</p>
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
        </div>

        {/* Localhost 2FA */}
        <div className="card p-5 space-y-3">
          <div className="flex items-center gap-2 font-medium"><ShieldOff className="h-4 w-4 text-warn" /> Localhost 2FA exemption</div>
          <label className="flex items-start gap-2 text-sm">
            <input type="checkbox" checked={no2fa} onChange={(e) => setNo2fa(e.target.checked)} className="mt-1" />
            <span>
              Allow password-only login from <code>localhost</code> (loopback).
              <span className="block text-xs text-muted mt-0.5">When on, connections from 127.0.0.1/::1 skip the mandatory 2FA enrollment and challenge. Remote connections always require 2FA. Leave off for server deployments.</span>
            </span>
          </label>
        </div>

        {msg && <p className="text-sm text-ok">{msg}</p>}
        <div className="flex justify-end">
          <button className="btn-primary" onClick={save} disabled={busy}>{busy ? <Loader2 className="h-4 w-4 animate-spin" /> : null} Save settings</button>
        </div>

        <LdapSettings />
      </div>
    </>
  );
}

// LdapSettings configures optional LDAP / Active Directory authentication.
function LdapSettings() {
  const [cfg, setCfg] = useState<LdapConfig | null>(null);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState<"" | "save" | "test">("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);

  const load = useCallback(() => {
    api.ldap().then(setCfg).catch(() => setCfg({ enabled: false, url: "", startTls: false, bindDn: "", userBaseDn: "", userFilter: "(uid=%s)", adminGroupDn: "" }));
  }, []);
  useEffect(() => load(), [load]);
  if (!cfg) return null;
  const patch = (p: Partial<LdapConfig>) => setCfg({ ...cfg, ...p });

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
      {msg && <p className={clsx("text-sm", msg.ok ? "text-ok" : "text-danger")}>{msg.text}</p>}
      <div className="flex justify-end gap-2">
        <button className="btn-ghost" onClick={() => run("test")} disabled={busy !== ""}>{busy === "test" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Send className="h-4 w-4" />} Test</button>
        <button className="btn-primary" onClick={() => run("save")} disabled={busy !== ""}>{busy === "save" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Network className="h-4 w-4" />} Save LDAP</button>
      </div>
    </div>
  );
}
