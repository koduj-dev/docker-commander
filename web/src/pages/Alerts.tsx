import { useCallback, useEffect, useRef, useState } from "react";
import { Plus, Trash2, Webhook as WebhookIcon, Check, Mail, Loader2, Send, Pencil, Download, Upload } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import { triggerDownload } from "../components/LoadModal";
import type { AlertEvent, AlertRule, AlertType, Severity, SmtpConfig, Webhook } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";
import { useDialogs } from "../components/Dialog";

type Tab = "feed" | "rules" | "webhooks" | "email";

export function Alerts() {
  const [tab, setTab] = useState<Tab>("feed");
  return (
    <>
      <PageHeader title="Alerts" />
      <div className="p-6 space-y-4">
        <div className="flex gap-1 border-b border-border">
          {(["feed", "rules", "webhooks", "email"] as const).map((t) => (
            <button
              key={t}
              onClick={() => setTab(t)}
              className={clsx(
                "px-4 py-2 text-sm font-medium capitalize border-b-2 -mb-px transition-colors",
                tab === t ? "border-accent text-accent" : "border-transparent text-muted hover:text-text"
              )}
            >
              {t}
            </button>
          ))}
        </div>
        {tab === "feed" && <Feed />}
        {tab === "rules" && <Rules />}
        {tab === "webhooks" && <Webhooks />}
        {tab === "email" && <EmailConfig />}
      </div>
    </>
  );
}

// ---- Severity helper --------------------------------------------------------

const sevBadge: Record<Severity, string> = {
  critical: "bg-danger/15 text-danger",
  warning: "bg-warn/15 text-warn",
  info: "bg-accent/15 text-accent",
};

// ---- Feed -------------------------------------------------------------------

function Feed() {
  const [events, setEvents] = useState<AlertEvent[] | null>(null);

  const load = useCallback(() => {
    api.alerts().then((r) => setEvents(r.events)).catch(() => setEvents([]));
  }, []);
  useEffect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, [load]);

  const ack = async (id: number) => {
    await api.ackAlert(id);
    load();
  };

  if (!events) return <Loading />;
  if (events.length === 0) return <EmptyState title="No alerts yet" hint="Fired alerts will appear here." />;

  return (
    <div className="card overflow-hidden">
      <table className="w-full text-sm">
        <thead className="text-muted text-xs uppercase tracking-wide">
          <tr className="border-b border-border">
            <th className="text-left font-medium px-4 py-3">Time</th>
            <th className="text-left font-medium px-4 py-3">Severity</th>
            <th className="text-left font-medium px-4 py-3">Rule</th>
            <th className="text-left font-medium px-4 py-3 hidden lg:table-cell">Host</th>
            <th className="text-left font-medium px-4 py-3">Container</th>
            <th className="text-left font-medium px-4 py-3">Message</th>
            <th className="px-4 py-3"></th>
          </tr>
        </thead>
        <tbody>
          {events.map((e) => (
            <tr key={e.id} className={clsx("border-b border-border/50", e.acknowledged && "opacity-50")}>
              <td className="px-4 py-2.5 text-muted whitespace-nowrap">{e.createdAt.slice(0, 19).replace("T", " ")}</td>
              <td className="px-4 py-2.5">
                <span className={clsx("text-xs px-2 py-0.5 rounded-md font-medium capitalize", sevBadge[e.severity])}>{e.severity}</span>
              </td>
              <td className="px-4 py-2.5">{e.ruleName}</td>
              <td className="px-4 py-2.5 hidden lg:table-cell text-xs text-muted">{e.hostName || "—"}</td>
              <td className="px-4 py-2.5 font-mono text-xs">{e.containerName}</td>
              <td className="px-4 py-2.5 text-muted">{e.message}</td>
              <td className="px-4 py-2.5 text-right">
                {!e.acknowledged && (
                  <button className="btn-ghost px-2 py-1" title="Acknowledge" onClick={() => ack(e.id)}>
                    <Check className="h-4 w-4" />
                  </button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ---- Rules ------------------------------------------------------------------

const STATE_EVENTS = [
  { id: "die", label: "Died / exited" },
  { id: "kill", label: "Killed" },
  { id: "oom", label: "Out of memory" },
  { id: "stop", label: "Stopped" },
  { id: "health_status: unhealthy", label: "Unhealthy" },
];

function Rules() {
  const [rules, setRules] = useState<AlertRule[] | null>(null);
  const [hooks, setHooks] = useState<Webhook[]>([]);
  const [showForm, setShowForm] = useState(false);
  const [editing, setEditing] = useState<AlertRule | null>(null);

  const load = useCallback(() => {
    api.alertRules().then(setRules).catch(() => setRules([]));
    api.webhooks().then(setHooks).catch(() => {});
  }, []);
  useEffect(() => load(), [load]);

  const dialogs = useDialogs();
  const fileRef = useRef<HTMLInputElement>(null);
  const toggle = async (r: AlertRule) => {
    await api.toggleAlertRule(r.id, !r.enabled);
    load();
  };
  const del = async (r: AlertRule) => {
    if (!(await dialogs.confirm({ title: "Delete alert rule", message: <>Delete the rule <code className="font-mono text-text">{r.name}</code>?</>, danger: true, confirmLabel: "Delete" }))) return;
    await api.deleteAlertRule(r.id);
    load();
  };

  const onImportFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = ""; // allow re-importing the same file
    if (!file) return;
    let bundle: unknown;
    try {
      bundle = JSON.parse(await file.text());
    } catch {
      await dialogs.alert({ title: "Import failed", message: "That file is not valid JSON." });
      return;
    }
    try {
      const res = await api.importAlertRules(bundle);
      const warnings = res.warnings ?? [];
      await dialogs.alert({
        title: "Rules imported",
        message: (
          <div className="space-y-2">
            <p>Imported <strong>{res.imported}</strong> rule{res.imported === 1 ? "" : "s"}.</p>
            {warnings.length > 0 && (
              <ul className="text-xs text-muted list-disc pl-4 space-y-0.5 max-h-40 overflow-auto">
                {warnings.map((wmsg, i) => <li key={i}>{wmsg}</li>)}
              </ul>
            )}
          </div>
        ),
      });
      load();
    } catch (err) {
      await dialogs.alert({ title: "Import failed", message: err instanceof Error ? err.message : "Could not import rules." });
    }
  };

  if (!rules) return <Loading />;

  return (
    <div className="space-y-4">
      <div className="flex justify-end gap-2">
        <input ref={fileRef} type="file" accept="application/json,.json" className="hidden" onChange={onImportFile} />
        <button className="btn-ghost" onClick={() => triggerDownload(api.exportAlertRulesUrl())} disabled={rules.length === 0} title="Download all rules as JSON">
          <Download className="h-4 w-4" /> Export
        </button>
        <button className="btn-ghost" onClick={() => fileRef.current?.click()} title="Import rules from a JSON file">
          <Upload className="h-4 w-4" /> Import
        </button>
        <button className="btn-primary" onClick={() => { setEditing(null); setShowForm((v) => !v); }}>
          <Plus className="h-4 w-4" /> New rule
        </button>
      </div>
      {(showForm || editing) && (
        <RuleForm
          key={editing?.id ?? "new"}
          hooks={hooks}
          existing={editing}
          onDone={() => { setShowForm(false); setEditing(null); load(); }}
        />
      )}
      {rules.length === 0 ? (
        <EmptyState title="No alert rules" hint="Create a rule to start monitoring." />
      ) : (
        <div className="card overflow-hidden">
          <table className="w-full text-sm">
            <thead className="text-muted text-xs uppercase tracking-wide">
              <tr className="border-b border-border">
                <th className="text-left font-medium px-4 py-3">Name</th>
                <th className="text-left font-medium px-4 py-3">Type</th>
                <th className="text-left font-medium px-4 py-3">Target</th>
                <th className="text-left font-medium px-4 py-3">Severity</th>
                <th className="text-left font-medium px-4 py-3">Config</th>
                <th className="text-center font-medium px-4 py-3">Enabled</th>
                <th className="px-4 py-3"></th>
              </tr>
            </thead>
            <tbody>
              {rules.map((r) => (
                <tr key={r.id} className="border-b border-border/50">
                  <td className="px-4 py-2.5 font-medium">{r.name}</td>
                  <td className="px-4 py-2.5"><span className="text-xs bg-panel2 rounded-sm px-1.5 py-0.5">{r.type}</span></td>
                  <td className="px-4 py-2.5 font-mono text-xs text-muted">{r.target || "*"}</td>
                  <td className="px-4 py-2.5"><span className={clsx("text-xs px-2 py-0.5 rounded-md capitalize", sevBadge[r.severity])}>{r.severity}</span></td>
                  <td className="px-4 py-2.5 font-mono text-[11px] text-muted max-w-[220px] truncate">{r.config}</td>
                  <td className="px-4 py-2.5 text-center">
                    <button onClick={() => toggle(r)} className={clsx("relative w-9 h-5 rounded-full transition-colors", r.enabled ? "bg-accent" : "bg-border")}>
                      <span className={clsx("absolute top-0.5 h-4 w-4 rounded-full bg-white transition-all", r.enabled ? "left-4" : "left-0.5")} />
                    </button>
                  </td>
                  <td className="px-4 py-2.5 text-right">
                    <div className="flex items-center justify-end gap-1">
                      <button className="btn-ghost px-2 py-1" title="Edit" onClick={() => { setShowForm(false); setEditing(r); }}><Pencil className="h-4 w-4" /></button>
                      <button className="btn-ghost px-2 py-1 text-danger" title="Delete" onClick={() => del(r)}><Trash2 className="h-4 w-4" /></button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function RuleForm({ hooks, existing, onDone }: { hooks: Webhook[]; existing?: AlertRule | null; onDone: () => void }) {
  // Prefill from an existing rule when editing (config is a raw JSON string).
  const cfg: Record<string, unknown> = (() => { try { return existing ? JSON.parse(existing.config) : {}; } catch { return {}; } })();
  const [name, setName] = useState(existing?.name ?? "");
  const [type, setType] = useState<AlertType>(existing?.type ?? "state");
  const [target, setTarget] = useState(existing?.target ?? "");
  const [severity, setSeverity] = useState<Severity>(existing?.severity ?? "warning");
  const [webhookId, setWebhookId] = useState<number | null>(existing?.webhookId ?? null);
  const [email, setEmail] = useState(existing?.email ?? false);
  const [cooldown, setCooldown] = useState(existing?.cooldownSec ?? 60);

  // type-specific
  const [events, setEvents] = useState<Set<string>>(new Set((cfg.events as string[]) ?? ["die"]));
  const [metric, setMetric] = useState<"cpu" | "mem">((cfg.metric as "cpu" | "mem") ?? "cpu");
  const [op, setOp] = useState<">" | "<">((cfg.op as ">" | "<") ?? ">");
  const [threshold, setThreshold] = useState((cfg.threshold as number) ?? 80);
  const [duration, setDuration] = useState((cfg.durationSec as number) ?? 30);
  const [pattern, setPattern] = useState((cfg.pattern as string) ?? "");
  const [isRegex, setIsRegex] = useState((cfg.isRegex as boolean) ?? false);
  const [windowSec, setWindowSec] = useState((cfg.windowSec as number) ?? 60);
  const [count, setCount] = useState((cfg.count as number) ?? 3);
  const [busy, setBusy] = useState(false);

  const buildConfig = (): unknown => {
    switch (type) {
      case "state":
        return { events: [...events] };
      case "resource":
        return { metric, op, threshold, durationSec: duration };
      case "log":
        return { pattern, isRegex };
      case "restart":
        return { windowSec, count };
    }
  };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const body = { name, type, target, config: buildConfig(), severity, webhookId, email, cooldownSec: cooldown };
      if (existing) await api.updateAlertRule(existing.id, body);
      else await api.createAlertRule({ ...body, enabled: true });
      onDone();
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="card p-5 space-y-4">
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <div>
          <label className="label">Rule name</label>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} required />
        </div>
        <div>
          <label className="label">Type</label>
          <select className="input" value={type} onChange={(e) => setType(e.target.value as AlertType)}>
            <option value="state">Container state</option>
            <option value="resource">Resource threshold</option>
            <option value="log">Log pattern</option>
            <option value="restart">Restart / crash loop</option>
          </select>
        </div>
        <div>
          <label className="label">Target (container name contains, blank = all)</label>
          <input className="input" value={target} onChange={(e) => setTarget(e.target.value)} placeholder="*" />
        </div>
      </div>

      {/* type-specific config */}
      {type === "state" && (
        <div>
          <label className="label">Fire on events</label>
          <div className="flex flex-wrap gap-2">
            {STATE_EVENTS.map((ev) => (
              <button
                key={ev.id}
                type="button"
                onClick={() =>
                  setEvents((prev) => {
                    const n = new Set(prev);
                    n.has(ev.id) ? n.delete(ev.id) : n.add(ev.id);
                    return n;
                  })
                }
                className={clsx("text-xs px-2.5 py-1.5 rounded-md", events.has(ev.id) ? "bg-accent/15 text-accent" : "bg-panel2 text-muted")}
              >
                {ev.label}
              </button>
            ))}
          </div>
        </div>
      )}
      {type === "resource" && (
        <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
          <div>
            <label className="label">Metric</label>
            <select className="input" value={metric} onChange={(e) => setMetric(e.target.value as "cpu" | "mem")}>
              <option value="cpu">CPU %</option>
              <option value="mem">Memory %</option>
            </select>
          </div>
          <div>
            <label className="label">Operator</label>
            <select className="input" value={op} onChange={(e) => setOp(e.target.value as ">" | "<")}>
              <option value=">">above</option>
              <option value="<">below</option>
            </select>
          </div>
          <div>
            <label className="label">Threshold %</label>
            <input className="input" type="number" value={threshold} onChange={(e) => setThreshold(+e.target.value)} />
          </div>
          <div>
            <label className="label">For (seconds)</label>
            <input className="input" type="number" value={duration} onChange={(e) => setDuration(+e.target.value)} />
          </div>
        </div>
      )}
      {type === "log" && (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4 items-end">
          <div>
            <label className="label">Pattern</label>
            <input className="input" value={pattern} onChange={(e) => setPattern(e.target.value)} placeholder="ERROR | panic | OOM" required />
          </div>
          <label className="flex items-center gap-2 text-sm pb-2">
            <input type="checkbox" checked={isRegex} onChange={(e) => setIsRegex(e.target.checked)} />
            Treat as regular expression
          </label>
        </div>
      )}
      {type === "restart" && (
        <div className="grid grid-cols-2 gap-4">
          <div>
            <label className="label">Restarts</label>
            <input className="input" type="number" value={count} onChange={(e) => setCount(+e.target.value)} />
          </div>
          <div>
            <label className="label">Within (seconds)</label>
            <input className="input" type="number" value={windowSec} onChange={(e) => setWindowSec(+e.target.value)} />
          </div>
        </div>
      )}

      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <div>
          <label className="label">Severity</label>
          <select className="input" value={severity} onChange={(e) => setSeverity(e.target.value as Severity)}>
            <option value="info">Info</option>
            <option value="warning">Warning</option>
            <option value="critical">Critical</option>
          </select>
        </div>
        <div>
          <label className="label">Webhook (optional)</label>
          <select className="input" value={webhookId ?? ""} onChange={(e) => setWebhookId(e.target.value ? +e.target.value : null)}>
            <option value="">— none —</option>
            {hooks.map((h) => (
              <option key={h.id} value={h.id}>{h.name}</option>
            ))}
          </select>
        </div>
        <div>
          <label className="label">Cooldown (seconds)</label>
          <input className="input" type="number" value={cooldown} onChange={(e) => setCooldown(+e.target.value)} />
        </div>
      </div>

      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={email} onChange={(e) => setEmail(e.target.checked)} />
        Also send an email (configure the SMTP server in the Email tab)
      </label>

      <div className="flex justify-end gap-2">
        <button type="button" className="btn-ghost" onClick={onDone}>Cancel</button>
        <button className="btn-primary" disabled={busy}>{busy ? "Saving…" : existing ? "Save changes" : "Create rule"}</button>
      </div>
    </form>
  );
}

// ---- Webhooks ---------------------------------------------------------------

function Webhooks() {
  const [hooks, setHooks] = useState<Webhook[] | null>(null);
  const [showForm, setShowForm] = useState(false);

  const load = useCallback(() => {
    api.webhooks().then(setHooks).catch(() => setHooks([]));
  }, []);
  useEffect(() => load(), [load]);

  const dialogs = useDialogs();
  const del = async (h: Webhook) => {
    if (!(await dialogs.confirm({ title: "Delete webhook", message: <>Delete the webhook <code className="font-mono text-text">{h.name}</code>?</>, danger: true, confirmLabel: "Delete" }))) return;
    await api.deleteWebhook(h.id);
    load();
  };

  if (!hooks) return <Loading />;

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <p className="text-sm text-muted">
          Fire alerts to any HTTP endpoint (Slack, Discord, Grafana, n8n…). Also scrape{" "}
          <code className="text-accent">/metrics</code> with Prometheus for Grafana dashboards.
        </p>
        <button className="btn-primary" onClick={() => setShowForm((v) => !v)}>
          <Plus className="h-4 w-4" /> New webhook
        </button>
      </div>
      {showForm && <WebhookForm onDone={() => { setShowForm(false); load(); }} />}
      {hooks.length === 0 ? (
        <EmptyState title="No webhooks" hint="Add a destination to receive alert notifications." />
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          {hooks.map((h) => (
            <div key={h.id} className="card p-4 flex items-start justify-between">
              <div className="min-w-0">
                <div className="flex items-center gap-2 font-medium">
                  <WebhookIcon className="h-4 w-4 text-accent" /> {h.name}
                </div>
                <div className="text-xs text-muted font-mono mt-1 break-all">{h.method} {h.url}</div>
              </div>
              <button className="btn-ghost px-2 py-1 text-danger" title="Delete" onClick={() => del(h)}>
                <Trash2 className="h-4 w-4" />
              </button>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function WebhookForm({ onDone }: { onDone: () => void }) {
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [method] = useState("POST");
  const [bodyTemplate, setBodyTemplate] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      await api.createWebhook({ name, url, method, headers: {}, bodyTemplate });
      onDone();
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="card p-5 space-y-4">
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <div>
          <label className="label">Name</label>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} required />
        </div>
        <div className="md:col-span-2">
          <label className="label">URL</label>
          <input className="input" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://hooks.slack.com/…" required />
        </div>
      </div>
      <div>
        <label className="label">Body template (optional Go template; blank = JSON payload)</label>
        <textarea
          className="input font-mono text-xs h-24"
          value={bodyTemplate}
          onChange={(e) => setBodyTemplate(e.target.value)}
          placeholder={'{"text":"[{{.Severity}}] {{.Container}}: {{.Message}}"}'}
        />
        <p className="text-xs text-muted mt-1">
          Fields: <code>{"{{.RuleName}} {{.Severity}} {{.Type}} {{.Container}} {{.Message}} {{.Value}} {{.Time}}"}</code>
        </p>
      </div>
      <div className="flex justify-end gap-2">
        <button type="button" className="btn-ghost" onClick={onDone}>Cancel</button>
        <button className="btn-primary" disabled={busy}>{busy ? "Saving…" : "Create webhook"}</button>
      </div>
    </form>
  );
}

// ---- Email (SMTP) -----------------------------------------------------------

function EmailConfig() {
  const [cfg, setCfg] = useState<SmtpConfig | null>(null);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState<"" | "save" | "test">("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);

  useEffect(() => {
    api.smtp().then(setCfg).catch(() => setCfg({ host: "", port: 587, username: "", from: "", to: "", tls: false }));
  }, []);

  if (!cfg) return <Loading />;

  const patch = (p: Partial<SmtpConfig>) => setCfg({ ...cfg, ...p });

  const save = async () => {
    setBusy("save"); setMsg(null);
    try {
      await api.setSmtp({ ...cfg, password });
      setPassword("");
      setMsg({ ok: true, text: "Saved." });
      api.smtp().then(setCfg).catch(() => {});
    } catch {
      setMsg({ ok: false, text: "save failed" });
    } finally { setBusy(""); }
  };

  const test = async () => {
    setBusy("test"); setMsg(null);
    try {
      const r = await api.testSmtp({ ...cfg, password });
      setMsg(r.ok ? { ok: true, text: "Test email sent." } : { ok: false, text: r.error ?? "test failed" });
    } catch {
      setMsg({ ok: false, text: "test failed" });
    } finally { setBusy(""); }
  };

  return (
    <div className="card p-5 space-y-4 max-w-2xl">
      <div className="flex items-center gap-2 font-medium"><Mail className="h-4 w-4 text-accent" /> SMTP server</div>
      <p className="text-xs text-muted">
        Configure a mail server, then enable <strong>“Also send an email”</strong> on any alert rule. The password is
        encrypted at rest and never returned by the API.
      </p>
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <div className="md:col-span-2">
          <label className="label">Host</label>
          <input className="input font-mono" value={cfg.host} onChange={(e) => patch({ host: e.target.value })} placeholder="smtp.example.com" />
        </div>
        <div>
          <label className="label">Port</label>
          <input className="input" type="number" value={cfg.port} onChange={(e) => patch({ port: +e.target.value })} placeholder="587" />
        </div>
        <div>
          <label className="label">Username</label>
          <input className="input" value={cfg.username} onChange={(e) => patch({ username: e.target.value })} autoComplete="off" />
        </div>
        <div>
          <label className="label">Password {cfg.hasPassword && <span className="text-ok">(stored)</span>}</label>
          <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder={cfg.hasPassword ? "•••••• (unchanged)" : ""} autoComplete="new-password" />
        </div>
        <label className="flex items-center gap-2 text-sm pb-2 self-end">
          <input type="checkbox" checked={cfg.tls} onChange={(e) => patch({ tls: e.target.checked })} /> Implicit TLS (port 465)
        </label>
        <div>
          <label className="label">From</label>
          <input className="input" value={cfg.from} onChange={(e) => patch({ from: e.target.value })} placeholder="alerts@example.com" />
        </div>
        <div className="md:col-span-2">
          <label className="label">To (comma-separated)</label>
          <input className="input" value={cfg.to} onChange={(e) => patch({ to: e.target.value })} placeholder="ops@example.com, oncall@example.com" />
        </div>
      </div>
      {msg && <p className={clsx("text-sm", msg.ok ? "text-ok" : "text-danger")}>{msg.text}</p>}
      <div className="flex justify-end gap-2">
        <button className="btn-ghost" onClick={test} disabled={busy !== ""}>
          {busy === "test" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Send className="h-4 w-4" />} Send test
        </button>
        <button className="btn-primary" onClick={save} disabled={busy !== ""}>
          {busy === "save" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Mail className="h-4 w-4" />} Save
        </button>
      </div>
    </div>
  );
}

function Loading() {
  return <div className="flex items-center gap-2 text-muted"><Spinner /> Loading…</div>;
}
