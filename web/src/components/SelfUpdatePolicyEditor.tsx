import { useEffect, useState } from "react";
import { api } from "../lib/api";
import type { SelfUpdatePolicy, UpdateStatus } from "../lib/types";
import { ArrowUpCircle, AlertTriangle } from "lucide-react";
import { Spinner } from "./ui";

export function SelfUpdatePolicyEditor() {
  const [status, setStatus] = useState<UpdateStatus | null>(null);
  const [policy, setPolicy] = useState<SelfUpdatePolicy | null>(null);
  const [saved, setSaved] = useState(false);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.updateStatus()
      .then((st) => { setStatus(st); setPolicy(st.selfUpdatePolicy); })
      .catch((e) => setErr(e instanceof Error ? e.message : "failed"));
  }, []);

  if (err && !policy) return <p className="text-xs text-danger">{err}</p>;
  if (!policy || !status) return <div className="flex items-center gap-2 text-muted text-sm"><Spinner /> Loading…</div>;

  // Mirrors the server's own gate (internal/api/selfupdate_policy.go
  // autoApplyEnabled): auto-apply can only ever run when the update check
  // itself is on AND the in-app apply/restart path is available. Saving a
  // policy while either is off would silently never take effect — same
  // capability flag the one-tap "Update & restart" button already keys off.
  const available = !status.disabled && !!status.selfUpdate;

  const set = (patch: Partial<SelfUpdatePolicy>) => { setPolicy({ ...policy, ...patch }); setSaved(false); };

  const save = async () => {
    setBusy(true); setErr("");
    try {
      setPolicy(await api.setSelfUpdatePolicy(policy));
      setSaved(true);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="card p-5 space-y-4 max-w-2xl">
      <div className="flex items-center gap-2 font-medium"><ArrowUpCircle className="h-4 w-4 text-accent" /> Self-update auto-apply</div>
      <p className="text-xs text-muted">
        Off by default. When on, a newer release is downloaded, verified and installed automatically —
        the same checksum-verified swap as the one-tap <strong>Update &amp; restart</strong> button — without
        anyone clicking it. Every admin sees a one-time notice after it happens, and it's recorded in the audit log.
      </p>

      {!available && (
        <p className="flex items-start gap-2 text-xs text-warn bg-warn/10 border border-warn/30 rounded-md p-2">
          <AlertTriangle className="h-3.5 w-3.5 shrink-0 mt-0.5" />
          <span>
            {status.disabled
              ? "The update check itself is disabled on this server (DC_UPDATE_CHECK=0) — auto-apply can never run until it's turned back on."
              : "Self-update isn't available on this server (DC_SELF_UPDATE=0, or a run mode that can't restart itself, e.g. Windows) — auto-apply can never run here."}
          </span>
        </p>
      )}

      <label className="flex items-start gap-2 text-sm">
        <input type="checkbox" className="mt-0.5" checked={policy.enabled} disabled={!available}
          onChange={(e) => set({ enabled: e.target.checked })} />
        <span>Automatically apply new releases</span>
      </label>

      <div>
        <label className="label">Granularity</label>
        <select className="input" disabled={!available || !policy.enabled} value={policy.granularity}
          onChange={(e) => set({ granularity: e.target.value as SelfUpdatePolicy["granularity"] })}>
          <option value="patch">Patch only (1.2.x)</option>
          <option value="minor">Patch &amp; minor (1.x.x)</option>
          <option value="major">Everything, including major</option>
        </select>
        <p className="text-xs text-muted mt-1">A ceiling, not an exact match: minor also allows patch releases through.</p>
      </div>

      <div className="flex items-center justify-end gap-3 pt-1">
        {err && <span className="text-sm text-danger">{err}</span>}
        {saved && <span className="text-sm text-ok">Saved.</span>}
        <button className="btn-primary" disabled={busy || !available} onClick={save}>{busy ? "Saving…" : "Save policy"}</button>
      </div>
    </div>
  );
}
