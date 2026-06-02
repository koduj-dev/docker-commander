import { useCallback, useEffect, useState } from "react";
import { X, Trash2, Plus, Loader2 } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { ParseRule } from "../lib/types";
import { compileRule, groupNames } from "../lib/parse";
import { PARSE_PRESETS } from "../lib/parsePresets";

// ParseRulesModal manages saved log-parsing rules: list, delete, and add with a
// live validation/preview against a sample line.
export function ParseRulesModal({ sample, onClose, onChanged }: { sample: string; onClose: () => void; onChanged: () => void }) {
  const [rules, setRules] = useState<ParseRule[] | null>(null);
  const [name, setName] = useState("");
  const [pattern, setPattern] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const load = useCallback(() => { api.parseRules().then(setRules).catch(() => setRules([])); }, []);
  useEffect(() => load(), [load]);

  const compiled = pattern ? compileRule(pattern) : null;
  const preview = compiled ? compiled.exec(sample)?.groups ?? null : null;
  const cols = groupNames(pattern);

  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim() || !pattern.trim()) return;
    if (!compileRule(pattern)) { setErr("invalid regular expression"); return; }
    if (cols.length === 0) { setErr("the pattern has no (?<name>…) capture groups"); return; }
    setBusy(true); setErr("");
    try {
      await api.createParseRule(name.trim(), pattern);
      setName(""); setPattern("");
      load(); onChanged();
    } catch {
      setErr("could not save rule");
    } finally { setBusy(false); }
  };

  const del = async (id: number) => { await api.deleteParseRule(id); load(); onChanged(); };

  return (
    <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <div className="card w-full max-w-2xl max-h-[88vh] flex flex-col" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <div className="font-medium">Log parsing rules</div>
          <button className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 space-y-4 overflow-auto">
          {rules && rules.length > 0 && (
            <div className="space-y-1">
              {rules.map((r) => (
                <div key={r.id} className="flex items-center gap-2 text-sm bg-panel2 rounded-md px-3 py-2">
                  <span className="font-medium">{r.name}</span>
                  <code className="text-xs text-muted font-mono truncate flex-1">{r.pattern}</code>
                  <button className="btn-ghost px-1.5 py-1 text-danger" onClick={() => del(r.id)}><Trash2 className="h-3.5 w-3.5" /></button>
                </div>
              ))}
            </div>
          )}

          <form onSubmit={add} className="space-y-3 border-t border-border pt-4">
            <div className="flex items-center justify-between">
              <div className="text-xs uppercase tracking-wide text-muted">New rule</div>
              <select
                className="input py-1 w-44 text-xs"
                value=""
                onChange={(e) => {
                  const p = PARSE_PRESETS.find((x) => x.name === e.target.value);
                  if (p) { setName(p.name); setPattern(p.pattern); }
                }}
                title="Start from a preset"
              >
                <option value="">Start from preset…</option>
                {PARSE_PRESETS.map((p) => <option key={p.name} value={p.name}>{p.name}</option>)}
              </select>
            </div>
            <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
              <div>
                <label className="label">Name</label>
                <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="nginx access" />
              </div>
              <div className="md:col-span-2">
                <label className="label">Pattern (regex with <code>{"(?<name>…)"}</code> groups)</label>
                <input className={clsx("input font-mono text-xs", pattern && !compiled && "ring-2 ring-danger/60")} value={pattern} onChange={(e) => setPattern(e.target.value)} placeholder={'(?<ip>\\S+) \\S+ \\S+ \\[(?<time>[^\\]]+)\\] "(?<method>\\S+) (?<path>\\S+)'} />
              </div>
            </div>
            {/* live preview against a current log line */}
            {pattern && (
              <div className="text-xs rounded-md bg-bg border border-border p-2.5 font-mono">
                <div className="text-muted/60 mb-1 truncate">sample: {sample || "(no log line yet)"}</div>
                {!compiled ? (
                  <span className="text-danger">invalid regex</span>
                ) : cols.length === 0 ? (
                  <span className="text-warn">no named groups yet</span>
                ) : preview ? (
                  <div className="flex flex-wrap gap-x-4 gap-y-1">
                    {cols.map((c) => (
                      <span key={c}><span className="text-accent">{c}</span>=<span className="text-text/80">{preview[c] ?? "—"}</span></span>
                    ))}
                  </div>
                ) : (
                  <span className="text-muted">columns: {cols.join(", ")} — sample line doesn't match</span>
                )}
              </div>
            )}
            {err && <p className="text-sm text-danger">{err}</p>}
            <div className="flex justify-end">
              <button className="btn-primary" disabled={busy || !name.trim() || !pattern.trim()}>
                {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />} Add rule
              </button>
            </div>
          </form>
        </div>
      </div>
    </div>
  );
}
