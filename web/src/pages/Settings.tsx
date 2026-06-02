import { useCallback, useEffect, useState } from "react";
import { Loader2, ShieldOff, LayoutGrid } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
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
      </div>
    </>
  );
}
