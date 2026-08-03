import { useEffect, useState } from "react";
import { api } from "../lib/api";
import type { MCPTokenPolicy } from "../lib/types";
import { Timer } from "lucide-react";
import { Spinner } from "./ui";

// The lifetime rules applied when anyone mints an MCP token.
//
// An expiry date is the only control here that works when nobody is paying
// attention: revocation needs someone to remember, and the tokens most worth
// revoking are the ones everybody has forgotten. Hence a default that expires,
// a ceiling so "no never-expiring tokens" cannot be sidestepped by asking for a
// hundred years, and an explicit switch for installations that genuinely need
// a permanent credential.
export function TokenPolicyEditor() {
  const [policy, setPolicy] = useState<MCPTokenPolicy | null>(null);
  const [saved, setSaved] = useState(false);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.mcpAdminTokenPolicy().then(setPolicy).catch((e) => setErr(e instanceof Error ? e.message : "failed"));
  }, []);

  if (err && !policy) return <p className="text-xs text-danger">{err}</p>;
  if (!policy) return <div className="flex items-center gap-2 text-muted text-sm"><Spinner /> Loading…</div>;

  const set = (patch: Partial<MCPTokenPolicy>) => { setPolicy({ ...policy, ...patch }); setSaved(false); };

  const save = async () => {
    setBusy(true); setErr("");
    try {
      // Take the server's normalised answer rather than keeping the typed one:
      // it repairs contradictions (a ceiling below the default, or none at all
      // while never-expiring tokens are off), and the admin should see what is
      // actually in force.
      setPolicy(await api.mcpAdminSetTokenPolicy(policy));
      setSaved(true);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="card p-5 space-y-4 max-w-2xl">
      <div className="flex items-center gap-2 font-medium"><Timer className="h-4 w-4 text-warn" /> MCP token lifetime</div>
      <p className="text-xs text-muted">
        Applies when a token is <strong>created</strong>. Tokens that already exist keep the expiry they were given —
        tightening this will not cut off a running integration. To retire tokens that predate a stricter rule, revoke
        them on the <strong>MCP Admin</strong> page.
      </p>

      <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <div>
          <label className="label">Default lifetime (days)</label>
          <input type="number" min={1} className="input" value={policy.defaultDays}
            onChange={(e) => set({ defaultDays: Number(e.target.value) })} />
          <p className="text-xs text-muted mt-1">Pre-selected in the creation form, and used when a client asks for no particular lifetime.</p>
        </div>
        <div>
          <label className="label">Maximum lifetime (days)</label>
          <input type="number" min={0} className="input" value={policy.maxDays}
            onChange={(e) => set({ maxDays: Number(e.target.value) })} />
          <p className="text-xs text-muted mt-1">
            The longest anyone may choose. {policy.allowUnlimited ? "0 = no ceiling." : "Required while never-expiring tokens are off — otherwise the rule can be sidestepped with a very large number."}
          </p>
        </div>
      </div>

      <label className="flex items-start gap-2 text-sm">
        <input type="checkbox" className="mt-0.5" checked={policy.allowUnlimited}
          onChange={(e) => set({ allowUnlimited: e.target.checked })} />
        <span>
          Allow never-expiring tokens
          <span className="block text-xs text-muted">
            Off by default. A token with no expiry outlives the laptop it was pasted into and the person it was made for.
          </span>
        </span>
      </label>

      {/* Same shape as the other cards on this page: the action sits inside the
          card it applies to, bottom-right, with its result beside it. */}
      <div className="flex items-center justify-end gap-3 pt-1">
        {err && <span className="text-sm text-danger">{err}</span>}
        {saved && <span className="text-sm text-ok">Saved.</span>}
        <button className="btn-primary" disabled={busy} onClick={save}>{busy ? "Saving…" : "Save policy"}</button>
      </div>
    </div>
  );
}
