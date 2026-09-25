import { useCallback, useEffect, useState } from "react";
import { Database, Loader2, Trash2 } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { RetentionArea, RetentionPolicy, RetentionRun, RetentionState } from "../lib/types";
import { bytes } from "../lib/format";
import { Spinner } from "./ui";
import { useDialogs } from "./Dialog";

type Key = keyof RetentionPolicy;

// Row descriptions say what is deleted, in the words a person deciding a TTL
// needs — not the table names.
const AREAS: { key: Key; label: string; what: string; unit: "days" | "revisions"; stat: (s: RetentionState["stats"]) => RetentionArea }[] = [
  { key: "alertEventsDays", label: "Alert events", unit: "days", stat: (s) => s.alertEvents,
    what: "The Alerts feed. Older events disappear from the list, and with them their delivery records." },
  { key: "alertDeliveriesDays", label: "Alert deliveries", unit: "days", stat: (s) => s.alertDeliveries,
    what: "The record of each webhook / email attempt. Cannot be kept longer than the events they belong to." },
  { key: "auditDays", label: "Audit log", unit: "days", stat: (s) => s.audit,
    what: "Who did what. Kept for at least the minimum below: it is what an incident review reads." },
  { key: "revisionsKeep", label: "Project revisions", unit: "revisions", stat: (s) => s.revisions,
    what: "Deploy history and the file snapshot of each revision, per project. The newest ones are kept." },
];

// validate mirrors the server's rules so a bad value is explained where it is
// typed instead of after a round trip; the server still has the last word.
function validate(p: RetentionPolicy, l: RetentionState["limits"]): string {
  const days = (name: string, v: number, min: number) =>
    v !== 0 && (v < min || v > l.maxDays) ? `${name} must be between ${min} and ${l.maxDays} days (or keep forever).` : "";
  return (
    days("Alert events", p.alertEventsDays, l.minAlertDays) ||
    days("Alert deliveries", p.alertDeliveriesDays, l.minAlertDays) ||
    days("Audit log", p.auditDays, l.minAuditDays) ||
    (p.revisionsKeep !== 0 && p.revisionsKeep < l.minRevisionsKeep ? `Keep at least ${l.minRevisionsKeep} revisions per project (or keep forever).` : "") ||
    (p.alertEventsDays !== 0 && (p.alertDeliveriesDays === 0 || p.alertDeliveriesDays > p.alertEventsDays)
      ? "Alert deliveries cannot be kept longer than the alert events they belong to." : "")
  );
}

function when(iso?: string): string {
  return iso ? iso.slice(0, 10) : "—";
}

// RetentionSettings edits how long history is kept and lets an admin purge it
// on demand. The daily job applies the SAVED policy, and so does "Purge now" —
// which is why it is disabled while the form has unsaved changes: what it
// deletes must never differ from what the page shows.
export function RetentionSettings() {
  const dialogs = useDialogs();
  const [state, setState] = useState<RetentionState | null>(null);
  const [form, setForm] = useState<RetentionPolicy | null>(null);
  const [busy, setBusy] = useState<"" | "save" | "purge">("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [failed, setFailed] = useState(false);
  // What is being typed into a field, so clearing it to type a new number doesn't
  // flip the row to "keep forever" (an empty box parses as 0).
  const [draft, setDraft] = useState<Partial<Record<Key, string>>>({});

  const load = useCallback(() => {
    api.retention().then((s) => { setState(s); setForm(s.policy); setDraft({}); setFailed(false); }).catch(() => setFailed(true));
  }, []);
  useEffect(() => load(), [load]);

  if (failed) return <div className="text-sm text-danger">Could not load the retention settings.</div>;
  if (!state || !form) return <div className="p-2 flex items-center gap-2 text-muted"><Spinner /> Loading…</div>;

  const dirty = (Object.keys(form) as Key[]).some((k) => form[k] !== state.policy[k]);
  const { limits } = state;
  const minFor = (k: Key) => (k === "auditDays" ? limits.minAuditDays : k === "revisionsKeep" ? limits.minRevisionsKeep : limits.minAlertDays);
  const set = (k: Key, v: number) => { setMsg(null); setDraft((d) => ({ ...d, [k]: undefined })); setForm({ ...form, [k]: v }); };
  const type = (k: Key, text: string) => {
    setMsg(null);
    setDraft((d) => ({ ...d, [k]: text }));
    const n = Number(text);
    if (text.trim() !== "" && Number.isInteger(n) && n >= 0) setForm({ ...form, [k]: n });
  };
  const problem = validate(form, limits);

  const save = async () => {
    setBusy("save"); setMsg(null);
    try {
      await api.setRetention(form);
      setMsg({ ok: true, text: "Saved. The next purge uses these values." });
      load();
    } catch (e) {
      setMsg({ ok: false, text: e instanceof Error ? e.message : "Save failed" });
    } finally { setBusy(""); }
  };

  const purge = async () => {
    const ok = await dialogs.confirm({
      title: "Purge old data now?",
      message: "Permanently deletes everything older than the saved retention policy. This cannot be undone.",
      confirmLabel: "Purge", danger: true,
    });
    if (!ok) return;
    setBusy("purge"); setMsg(null);
    try {
      const run = await api.purgeRetention();
      setMsg(run.error ? { ok: false, text: run.error } : { ok: true, text: `Purged ${run.alertEvents + run.alertDeliveries + run.audit + run.revisions} rows.` });
      load();
    } catch (e) {
      setMsg({ ok: false, text: e instanceof Error ? e.message : "Purge failed" });
    } finally { setBusy(""); }
  };

  return (
    <div className="space-y-4 max-w-3xl">
      <div className="card p-5 space-y-4">
        <div className="flex items-center gap-2 font-medium"><Database className="h-4 w-4 text-accent" /> Data retention</div>
        <p className="text-xs text-muted">
          Old history is deleted automatically once a day. Leave a value empty (“keep forever”) to never delete that kind of data.
          Database: <b>{bytes(state.stats.dbBytes)}</b> ({bytes(state.stats.dbFreeBytes)} of it free space that new data reuses — deleting rows does not shrink the file).
        </p>
        <div className="divide-y divide-border">
          {AREAS.map((a) => {
            const st = a.stat(state.stats);
            const value = form[a.key];
            const forever = value === 0;
            const id = `retention-${a.key}`;
            return (
              <div key={a.key} className="py-3 grid grid-cols-1 md:grid-cols-[1fr_auto] gap-3 items-start">
                <div className="min-w-0">
                  <label htmlFor={id} className="font-medium text-sm">{a.label}</label>
                  <p className="text-xs text-muted mt-0.5">{a.what}</p>
                  <p className="text-xs text-muted mt-1">
                    {st.rows.toLocaleString()} stored{st.oldest ? <> · oldest {when(st.oldest)}</> : null}
                  </p>
                </div>
                <div className="flex items-center gap-2">
                  <input
                    id={id}
                    type="number"
                    className="input w-24 text-right"
                    disabled={forever}
                    min={minFor(a.key)}
                    max={a.unit === "days" ? limits.maxDays : undefined}
                    value={forever ? "" : (draft[a.key] ?? String(value))}
                    placeholder="∞"
                    onChange={(e) => type(a.key, e.target.value)}
                    onBlur={() => setDraft((d) => ({ ...d, [a.key]: undefined }))}
                  />
                  <span className="text-xs text-muted w-16">{a.unit}</span>
                  <label className="text-xs flex items-center gap-1.5 whitespace-nowrap">
                    <input
                      type="checkbox"
                      checked={forever}
                      onChange={(e) => set(a.key, e.target.checked ? 0 : state.defaults[a.key])}
                    /> keep forever
                  </label>
                </div>
              </div>
            );
          })}
        </div>
        <p className="text-xs text-muted">
          Minimums: audit log {limits.minAuditDays} days, alert data {limits.minAlertDays} day, {limits.minRevisionsKeep} revisions per project.
        </p>
        {problem && <p role="alert" className="text-xs text-danger">{problem}</p>}
        <div className="flex flex-wrap items-center justify-between gap-3">
          <button className="btn-ghost px-3 py-1.5 text-sm" onClick={() => { setMsg(null); setDraft({}); setForm(state.defaults); }} disabled={busy !== ""}>
            Reset to defaults
          </button>
          <div className="flex items-center gap-3">
            {msg && <span role="status" className={clsx("text-sm", msg.ok ? "text-ok" : "text-danger")}>{msg.text}</span>}
            <button className="btn-ghost px-3 py-1.5 text-sm text-danger" onClick={purge} disabled={busy !== "" || dirty}
              title={dirty ? "Save your changes first — a purge uses the saved policy" : "Delete everything older than the saved policy"}>
              {busy === "purge" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Trash2 className="h-4 w-4" />} Purge now
            </button>
            <button className="btn-primary" onClick={save} disabled={busy !== "" || !dirty || !!problem}>
              {busy === "save" ? <Loader2 className="h-4 w-4 animate-spin" /> : null} Save
            </button>
          </div>
        </div>
      </div>

      <LastRun run={state.lastRun} />
    </div>
  );
}

function LastRun({ run }: { run: RetentionRun | null }) {
  return (
    <div className="card p-5 space-y-2">
      <div className="font-medium text-sm">Last purge</div>
      {!run ? (
        <p className="text-xs text-muted">Nothing has been purged yet. The first automatic run happens shortly after the server starts, then daily.</p>
      ) : (
        <div className="text-sm space-y-1">
          <div className="text-xs text-muted">
            {new Date(run.at).toLocaleString()} · {run.trigger} · {run.durationMs} ms
          </div>
          <div>
            Deleted {run.alertEvents.toLocaleString()} alert events, {run.alertDeliveries.toLocaleString()} deliveries,{" "}
            {run.audit.toLocaleString()} audit entries, {run.revisions.toLocaleString()} project revisions ({run.revisionFiles.toLocaleString()} snapshot files).
          </div>
          <div className="text-xs text-muted">Database {bytes(run.dbBytesBefore)} → {bytes(run.dbBytesAfter)}</div>
          {run.error && <div className="text-xs text-danger">Errors: {run.error}</div>}
        </div>
      )}
    </div>
  );
}
