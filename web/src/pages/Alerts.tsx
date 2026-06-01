import { useCallback, useEffect, useState } from "react";
import { Plus, Trash2, Webhook as WebhookIcon, Check } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { AlertEvent, AlertRule, AlertType, Severity, Webhook } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";

type Tab = "feed" | "rules" | "webhooks";

export function Alerts() {
  const [tab, setTab] = useState<Tab>("feed");
  return (
    <>
      <PageHeader title="Alerts" />
      <div className="p-6 space-y-4">
        <div className="flex gap-1 border-b border-border">
          {(["feed", "rules", "webhooks"] as const).map((t) => (
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

  const load = useCallback(() => {
    api.alertRules().then(setRules).catch(() => setRules([]));
    api.webhooks().then(setHooks).catch(() => {});
  }, []);
  useEffect(() => load(), [load]);

  const toggle = async (r: AlertRule) => {
    await api.toggleAlertRule(r.id, !r.enabled);
    load();
  };
  const del = async (id: number) => {
    await api.deleteAlertRule(id);
    load();
  };

  if (!rules) return <Loading />;

  return (
    <div className="space-y-4">
      <div className="flex justify-end">
        <button className="btn-primary" onClick={() => setShowForm((v) => !v)}>
          <Plus className="h-4 w-4" /> New rule
        </button>
      </div>
      {showForm && <RuleForm hooks={hooks} onDone={() => { setShowForm(false); load(); }} />}
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
                  <td className="px-4 py-2.5"><span className="text-xs bg-panel2 rounded px-1.5 py-0.5">{r.type}</span></td>
                  <td className="px-4 py-2.5 font-mono text-xs text-muted">{r.target || "*"}</td>
                  <td className="px-4 py-2.5"><span className={clsx("text-xs px-2 py-0.5 rounded-md capitalize", sevBadge[r.severity])}>{r.severity}</span></td>
                  <td className="px-4 py-2.5 font-mono text-[11px] text-muted max-w-[220px] truncate">{r.config}</td>
                  <td className="px-4 py-2.5 text-center">
                    <button onClick={() => toggle(r)} className={clsx("relative w-9 h-5 rounded-full transition-colors", r.enabled ? "bg-accent" : "bg-border")}>
                      <span className={clsx("absolute top-0.5 h-4 w-4 rounded-full bg-white transition-all", r.enabled ? "left-4" : "left-0.5")} />
                    </button>
                  </td>
                  <td className="px-4 py-2.5 text-right">
                    <button className="btn-ghost px-2 py-1 text-danger" title="Delete" onClick={() => del(r.id)}>
                      <Trash2 className="h-4 w-4" />
                    </button>
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

function RuleForm({ hooks, onDone }: { hooks: Webhook[]; onDone: () => void }) {
  const [name, setName] = useState("");
  const [type, setType] = useState<AlertType>("state");
  const [target, setTarget] = useState("");
  const [severity, setSeverity] = useState<Severity>("warning");
  const [webhookId, setWebhookId] = useState<number | null>(null);
  const [cooldown, setCooldown] = useState(60);

  // type-specific
  const [events, setEvents] = useState<Set<string>>(new Set(["die"]));
  const [metric, setMetric] = useState<"cpu" | "mem">("cpu");
  const [op, setOp] = useState<">" | "<">(">");
  const [threshold, setThreshold] = useState(80);
  const [duration, setDuration] = useState(30);
  const [pattern, setPattern] = useState("");
  const [isRegex, setIsRegex] = useState(false);
  const [windowSec, setWindowSec] = useState(60);
  const [count, setCount] = useState(3);
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
      await api.createAlertRule({
        name, enabled: true, type, target, config: buildConfig(),
        severity, webhookId, cooldownSec: cooldown,
      });
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

      <div className="flex justify-end gap-2">
        <button type="button" className="btn-ghost" onClick={onDone}>Cancel</button>
        <button className="btn-primary" disabled={busy}>{busy ? "Saving…" : "Create rule"}</button>
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

  const del = async (id: number) => {
    await api.deleteWebhook(id);
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
              <button className="btn-ghost px-2 py-1 text-danger" title="Delete" onClick={() => del(h.id)}>
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

function Loading() {
  return <div className="flex items-center gap-2 text-muted"><Spinner /> Loading…</div>;
}
