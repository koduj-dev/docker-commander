import { useEffect, useState } from "react";
import { api } from "../lib/api";
import type { SelfUpdatePolicy } from "../lib/types";
import { ArrowUpCircle } from "lucide-react";
import { Spinner } from "./ui";

// Auto-apply is off by default (NEXT.md), and "minor" — patch+minor, not
// major — is the sensible starting granularity once someone opts in, the
// same WordPress-style default this feature is modeled on.
const DEFAULT_POLICY: SelfUpdatePolicy = { enabled: false, granularity: "minor" };

export function SelfUpdatePolicyEditor() {
  const [policy, setPolicy] = useState<SelfUpdatePolicy | null>(null);
  const [saved, setSaved] = useState(false);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.updateStatus()
      .then((st) => setPolicy(st.selfUpdatePolicy ?? DEFAULT_POLICY))
      .catch((e) => setErr(e instanceof Error ? e.message : "failed"));
  }, []);

  if (err && !policy) return <p className="text-xs text-danger">{err}</p>;
  if (!policy) return <div className="flex items-center gap-2 text-muted text-sm"><Spinner /> Loading…</div>;

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

      <label className="flex items-start gap-2 text-sm">
        <input type="checkbox" className="mt-0.5" checked={policy.enabled}
          onChange={(e) => set({ enabled: e.target.checked })} />
        <span>Automatically apply new releases</span>
      </label>

      <div>
        <label className="label">Granularity</label>
        <select className="input" disabled={!policy.enabled} value={policy.granularity}
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
        <button className="btn-primary" disabled={busy} onClick={save}>{busy ? "Saving…" : "Save policy"}</button>
      </div>
    </div>
  );
}
