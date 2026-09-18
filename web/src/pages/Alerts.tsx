import { useCallback, useEffect, useRef, useState } from "react";
import { Plus, Trash2, Webhook as WebhookIcon, Check, CheckCheck, Pencil, Download, Upload, X , ChevronDown, ChevronUp, ChevronsUpDown, BellOff, Ban} from "lucide-react";
import { Link } from "react-router-dom";
import clsx from "clsx";
import { api } from "../lib/api";
import type { MaintenanceWindowBody } from "../lib/api";
import { triggerDownload } from "../components/LoadModal";
import type { AlertEvent, AlertRule, AlertType, Host, MaintenanceWindow, Severity, Webhook } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";
import { useAuth } from "../auth/AuthContext";
import { Tabs } from "../components/Tabs";
import { useDialogs } from "../components/Dialog";
import { useAlertPulse } from "../lib/alertStream";

// Metric names understood by a resource rule. "cpu" is Docker's own
// one-core-is-100% figure; "cpu_total" normalises it across the host's cores.
// netrx_rate/nettx_rate are bytes/s, entered here as MB/s (same convention as
// the memory-limit field elsewhere) and converted in buildConfig().
type Metric = "cpu" | "cpu_total" | "mem" | "netrx_rate" | "nettx_rate";
const isRateMetric = (m: Metric) => m === "netrx_rate" || m === "nettx_rate";

type Tab = "feed" | "rules" | "webhooks" | "maintenance";

export function Alerts() {
  const [tab, setTab] = useState<Tab>("feed");
  // The feed publishes its "acknowledge all" here so the page's primary action
  // sits in the header with the others, not buried in the filter row.
  const [ackAll, setAckAll] = useState<(() => void) | null>(null);
  return (
    <>
      <PageHeader
        title="Alerts"
        actions={
          tab === "feed" && ackAll ? (
            <button className="btn-ghost px-3 py-1.5 text-sm" onClick={ackAll} title="Acknowledge everything matching the current filters">
              <CheckCheck className="h-4 w-4" /> Ack all
            </button>
          ) : undefined
        }
      />
      <div className="p-6 space-y-4">
        <Tabs
          active={tab}
          onChange={setTab}
          tabs={[
            { key: "feed", label: "Feed" },
            { key: "rules", label: "Rules" },
            { key: "webhooks", label: "Webhooks" },
            { key: "maintenance", label: "Maintenance", icon: <BellOff className="h-4 w-4" /> },
          ]}
        />
        {tab === "feed" && <Feed onAckAllReady={setAckAll} />}
        {tab === "rules" && <Rules />}
        {tab === "webhooks" && <Webhooks />}
        {tab === "maintenance" && <MaintenanceWindows />}
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

// kindBadge colours a feed row by what the event means. A resolved condition is
// good news and must not be painted with the severity that raised it — an all-red
// feed is what made the old log unreadable.
function kindBadge(e: AlertEvent): string {
  if (e.kind === "resolved") return "bg-ok/15 text-ok";
  return sevBadge[e.severity];
}

// ---- Feed -------------------------------------------------------------------

const PAGE = 50;

function Feed({ onAckAllReady }: { onAckAllReady: (fn: (() => void) | null) => void }) {
  const [events, setEvents] = useState<AlertEvent[] | null>(null);
  const [total, setTotal] = useState(0);
  const [outstanding, setOutstanding] = useState(0);
  const [offset, setOffset] = useState(0);
  const [detail, setDetail] = useState<AlertEvent | null>(null);
  const [sort, setSort] = useState("time");
  const [desc, setDesc] = useState(true);
  const dialogs = useDialogs();

  // Filters. `text` is debounced into `q` so typing doesn't fire a request per
  // keystroke against a table that can hold a lot of rows.
  const [severity, setSeverity] = useState("");
  const [kind, setKind] = useState("");
  const [container, setContainer] = useState("");
  const [rule, setRule] = useState("");
  const [text, setText] = useState("");
  const [q, setQ] = useState("");
  const [unacked, setUnacked] = useState(false);
  const [host, setHost] = useState<string>("");
  const [hosts, setHosts] = useState<Host[]>([]);
  useEffect(() => {
    api.hosts().then(setHosts).catch(() => setHosts([]));
  }, []);
  useEffect(() => {
    const t = setTimeout(() => setQ(text), 350);
    return () => clearTimeout(t);
  }, [text]);

  // Any filter change restarts paging: staying on page 4 of a result set that
  // just shrank to one page shows an empty table and looks like a bug.
  useEffect(() => setOffset(0), [severity, kind, container, rule, q, unacked, host]);

  const load = useCallback(() => {
    api
      .alerts({ severity, kind, container, rule, q, unacked, host: host === "" ? undefined : Number(host), sort, desc, limit: PAGE, offset })
      .then((r) => {
        setEvents(r.events);
        setTotal(r.total);
        setOutstanding(r.outstanding);
      })
      .catch(() => setEvents([]));
  }, [severity, kind, container, rule, q, unacked, host, sort, desc, offset]);

  // Refresh on the shared alert poll rather than a timer of its own: a second
  // interval is what made a toast arrive seconds after its row appeared.
  const pulse = useAlertPulse();
  useEffect(() => {
    load();
  }, [load, pulse.tick]);

  const ack = async (id: number) => {
    await api.ackAlert(id);
    load();
  };

  // Clicking a column sorts by it; clicking the active one flips direction.
  // Sorting restarts paging for the same reason filtering does.
  const applySort = (col: string) => {
    if (col === sort) {
      setDesc(!desc);
    } else {
      setSort(col);
      setDesc(col === "time"); // newest-first reads right for time, A-Z for names
    }
    setOffset(0);
  };

  const filtered = !!(severity || kind || container || rule || q || unacked || host);
  const ackAll = async () => {
    if (
      !(await dialogs.confirm({
        title: "Acknowledge all",
        message: filtered ? (
          <>
            Acknowledge the <strong>{outstanding}</strong> outstanding alert{outstanding === 1 ? "" : "s"} matching the
            current filters? This cannot be undone, and they will be attributed to you.
          </>
        ) : (
          <>
            Acknowledge <strong>all {outstanding}</strong> outstanding alert{outstanding === 1 ? "" : "s"}? This cannot
            be undone, and they will be attributed to you.
          </>
        ),
        danger: true,
        confirmLabel: "Acknowledge all",
      }))
    )
      return;
    await api.ackAllAlerts({ severity, kind, container, rule, q, host: host === "" ? undefined : Number(host) });
    load();
  };
  // Hand the action to the page header, and take it back on unmount so it can't
  // outlive the tab it belongs to.
  //
  // Published through a ref on purpose. Depending on `ackAll` directly is an
  // infinite render loop: it is a new closure every render, so the effect re-runs
  // every render, which sets state in the parent, which re-renders this, which
  // makes a new closure. The page then stops responding entirely — navigation
  // changes the URL while React never gets far enough to paint. Only the setter
  // is a dependency here, and it is stable, so this publishes exactly once.
  const ackAllRef = useRef(ackAll);
  ackAllRef.current = ackAll;
  useEffect(() => {
    const stable = () => {
      void ackAllRef.current();
    };
    onAckAllReady(() => stable);
    return () => onAckAllReady(null);
  }, [onAckAllReady]);

  const clear = () => {
    setSeverity("");
    setKind("");
    setContainer("");
    setRule("");
    setText("");
    setQ("");
    setUnacked(false);
    setHost("");
  };

  return (
    <div className="space-y-3">
      <div className="card p-3 flex flex-wrap items-end gap-3">
        <label className="block">
          <span className="label">Severity</span>
          <select className="input py-1.5" value={severity} onChange={(e) => setSeverity(e.target.value)}>
            <option value="">Any</option>
            <option value="critical">Critical</option>
            <option value="warning">Warning</option>
            <option value="info">Info</option>
          </select>
        </label>
        <label className="block">
          <span className="label">Lifecycle</span>
          <select className="input py-1.5" value={kind} onChange={(e) => setKind(e.target.value)}>
            <option value="">Any</option>
            <option value="firing">Firing</option>
            <option value="escalated">Escalated</option>
            <option value="eased">Eased</option>
            <option value="repeat">Repeat</option>
            <option value="resolved">Resolved</option>
          </select>
        </label>
        <label className="block">
          <span className="label">Rule</span>
          <input className="input py-1.5" value={rule} onChange={(e) => setRule(e.target.value)} placeholder="name contains…" />
        </label>
        <label className="block">
          <span className="label">Container</span>
          <input className="input py-1.5" value={container} onChange={(e) => setContainer(e.target.value)} placeholder="name contains…" />
        </label>
        <label className="block flex-1 min-w-[12rem]">
          <span className="label">Message</span>
          <input className="input py-1.5 w-full" value={text} onChange={(e) => setText(e.target.value)} placeholder="search text…" />
        </label>
        <label className="block">
          <span className="label">Host</span>
          <select className="input py-1.5" value={host} onChange={(e) => setHost(e.target.value)}>
            <option value="">Any</option>
            {hosts.map((h) => (
              <option key={h.id} value={h.id}>
                {h.name}
              </option>
            ))}
          </select>
        </label>
        <label className="flex items-center gap-2 text-sm pb-1.5">
          <input type="checkbox" checked={unacked} onChange={(e) => setUnacked(e.target.checked)} />
          Unacknowledged only
        </label>
        {filtered && (
          <button className="btn-ghost px-3 py-1.5 text-sm" onClick={clear}>
            Clear
          </button>
        )}
      </div>

      {!events ? (
        <Loading />
      ) : events.length === 0 ? (
        <EmptyState
          title={filtered ? "No alerts match those filters" : "No alerts yet"}
          hint={filtered ? "Widen or clear the filters to see more." : "Fired alerts will appear here."}
        />
      ) : (
        <>
          <div className="card overflow-hidden">
            <table className="w-full text-sm">
              <thead className="text-muted text-xs uppercase tracking-wide">
                <tr className="border-b border-border">
                  <SortTh label="Time" col="time" sort={sort} desc={desc} onSort={applySort} />
                  <SortTh label="Severity" col="severity" sort={sort} desc={desc} onSort={applySort} />
                  <SortTh label="Rule" col="rule" sort={sort} desc={desc} onSort={applySort} />
                  <SortTh label="Host" col="host" sort={sort} desc={desc} onSort={applySort} className="hidden lg:table-cell" />
                  <SortTh label="Container" col="container" sort={sort} desc={desc} onSort={applySort} />
                  <th className="text-left font-medium px-4 py-3">Message</th>
                  <th className="text-left font-medium px-4 py-3 hidden md:table-cell">Delivery</th>
                  <th className="px-4 py-3"></th>
                </tr>
              </thead>
              <tbody>
                {events.map((e) => (
                  <FeedRow key={e.id} e={e} onOpen={() => setDetail(e)} onAck={() => ack(e.id)} />
                ))}
              </tbody>
            </table>
          </div>

          <div className="flex items-center gap-3 text-sm">
            <span className="text-muted">
              {offset + 1}–{Math.min(offset + events.length, total)} of {total}
            </span>
            <div className="ml-auto flex gap-2">
              <button
                className="btn-ghost px-3 py-1.5 disabled:opacity-40"
                disabled={offset === 0}
                onClick={() => setOffset(Math.max(0, offset - PAGE))}
              >
                Previous
              </button>
              <button
                className="btn-ghost px-3 py-1.5 disabled:opacity-40"
                disabled={offset + PAGE >= total}
                onClick={() => setOffset(offset + PAGE)}
              >
                Next
              </button>
            </div>
          </div>
        </>
      )}
      {detail && <AlertDetailModal e={detail} onClose={() => setDetail(null)} onAck={() => { void ack(detail.id); setDetail(null); }} />}
    </div>
  );
}

// SortTh is a sortable column header. Sorting happens server-side, so it orders
// the whole result set rather than just the page on screen — the alternative
// looks identical and is wrong the moment there is more than one page.
function SortTh({
  label,
  col,
  sort,
  desc,
  onSort,
  className,
}: {
  label: string;
  col: string;
  sort: string;
  desc: boolean;
  onSort: (col: string) => void;
  className?: string;
}) {
  const active = sort === col;
  return (
    <th className={clsx("text-left font-medium px-4 py-3", className)}>
      <button className={clsx("inline-flex items-center gap-1", active ? "text-text" : "hover:text-text")} onClick={() => onSort(col)}>
        {label}
        {active ? (
          desc ? <ChevronDown className="h-3 w-3" /> : <ChevronUp className="h-3 w-3" />
        ) : (
          <ChevronsUpDown className="h-3 w-3 opacity-40" />
        )}
      </button>
    </th>
  );
}

// FeedRow renders one event. The whole row opens the detail — the table can only
// ever show a truncated view, and the message is often the least of it.
function FeedRow({ e, onOpen, onAck }: { e: AlertEvent; onOpen: () => void; onAck: () => void }) {
  const deliveries = e.deliveries ?? [];
  return (
    <tr
      className={clsx("border-b border-border/50 cursor-pointer hover:bg-panel2/40", e.acknowledged && "opacity-50")}
      onClick={onOpen}
    >
      <td className="px-4 py-2.5 text-muted whitespace-nowrap">{e.createdAt.slice(0, 19).replace("T", " ")}</td>
      <td className="px-4 py-2.5 whitespace-nowrap">
        <span className={clsx("text-xs px-2 py-0.5 rounded-md font-medium capitalize", kindBadge(e))}>
          {e.kind === "resolved" ? "resolved" : e.severity}
        </span>
        {e.kind && e.kind !== "firing" && e.kind !== "resolved" && (
          <span className="ml-1 text-[10px] uppercase tracking-wide text-muted">{e.kind}</span>
        )}
        {e.suppressed && (
          <span className="ml-1 inline-flex items-center gap-0.5 text-[10px] uppercase tracking-wide text-muted" title="A maintenance window suppressed delivery for this event">
            <BellOff className="h-3 w-3" /> silenced
          </span>
        )}
      </td>
      <td className="px-4 py-2.5">{e.ruleName}</td>
      <td className="px-4 py-2.5 hidden lg:table-cell text-xs text-muted">{e.hostName || "—"}</td>
      <td className="px-4 py-2.5 font-mono text-xs">{e.containerName}</td>
      <td className="px-4 py-2.5 text-muted max-w-md truncate" title={e.message}>{e.message}</td>
      <td className="px-4 py-2.5 hidden md:table-cell">
        {deliveries.length === 0 ? (
          <span className="text-xs text-muted">—</span>
        ) : (
          <span className="text-xs">
            {deliveries.every((d) => d.ok) ? (
              <span className="text-ok">delivered</span>
            ) : deliveries.some((d) => d.ok) ? (
              <span className="text-warn">partial</span>
            ) : (
              <span className="text-danger">failed</span>
            )}
            <span className="text-muted"> ({deliveries.length})</span>
          </span>
        )}
      </td>
      <td className="px-4 py-2.5 text-right whitespace-nowrap">
        {e.acknowledged ? (
          // No name means no person did it — a resolution is stored already
          // settled, because there is nothing to act on once a condition has
          // ended. Saying "ack" there would claim someone looked at it.
          <span className="text-xs text-muted" title={e.acknowledgedAt ? `at ${e.acknowledgedAt.slice(0, 19).replace("T", " ")}` : undefined}>
            {e.acknowledgedBy ? `ack by ${e.acknowledgedBy}` : "—"}
          </span>
        ) : (
          <button
            className="btn-ghost px-2 py-1"
            title="Acknowledge"
            onClick={(ev) => {
              ev.stopPropagation(); // acknowledging is not "open the detail"
              onAck();
            }}
          >
            <Check className="h-4 w-4" />
          </button>
        )}
      </td>
    </tr>
  );
}

// AlertDetailModal is where an alert is actually readable: the full message, what
// the number was, how long the condition lasted, who acknowledged it, and every
// attempt made to deliver it — with a way through to the container it is about.
function AlertDetailModal({ e, onClose, onAck }: { e: AlertEvent; onClose: () => void; onAck: () => void }) {
  const deliveries = e.deliveries ?? [];
  const row = (label: string, value: React.ReactNode) => (
    <div className="grid grid-cols-[10rem_1fr] gap-3 py-1.5 border-b border-border/40 last:border-0">
      <div className="text-xs uppercase tracking-wide text-muted pt-0.5">{label}</div>
      <div className="text-sm break-words">{value}</div>
    </div>
  );

  return (
    <div className="fixed inset-0 z-[55] bg-black/60 grid place-items-center p-6" onClick={(e) => { e.stopPropagation(); onClose(); }}>
      <div className="card w-[70vw] max-w-[60rem] max-h-[80vh] flex flex-col" onClick={(ev) => ev.stopPropagation()}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <span className={clsx("text-xs px-2 py-0.5 rounded-md font-medium capitalize", kindBadge(e))}>
            {e.kind === "resolved" ? "resolved" : e.severity}
          </span>
          <div className="font-medium truncate">{e.ruleName}</div>
          <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose} title="Close">
            <X className="h-4 w-4" />
          </button>
        </div>

        <div className="p-4 overflow-y-auto space-y-4">
          <div>
            {row("Message", <span className="font-mono text-xs">{e.message}</span>)}
            {row("Fired at", e.createdAt.slice(0, 19).replace("T", " "))}
            {row("Lifecycle", <span className="capitalize">{e.kind || "firing"}</span>)}
            {e.durationSec > 0 && row("Condition lasted", formatDuration(e.durationSec))}
            {e.value !== null && e.value !== undefined && row("Measured value", e.value.toFixed(1))}
            {row("Host", e.hostName || "local")}
            {row(
              "Container",
              e.containerId ? (
                <Link to={`/containers/${e.containerId}`} className="text-accent hover:underline font-mono text-xs" onClick={onClose}>
                  {e.containerName || e.containerId.slice(0, 12)}
                </Link>
              ) : (
                <span className="text-muted">—</span>
              ),
            )}
            {row("Rule type", e.type || "—")}
            {row(
              "Acknowledged",
              e.acknowledged ? (
                e.acknowledgedBy ? (
                  <>
                    by <strong>{e.acknowledgedBy}</strong>
                    {e.acknowledgedAt ? ` at ${e.acknowledgedAt.slice(0, 19).replace("T", " ")}` : ""}
                  </>
                ) : (
                  // Settled without a person: a resolution needs no action.
                  <span className="text-muted">not required — the condition ended</span>
                )
              ) : (
                <span className="text-muted">no</span>
              ),
            )}
          </div>

          <div>
            <div className="text-xs uppercase tracking-wide text-muted mb-2">Delivery</div>
            {deliveries.length === 0 ? (
              <p className="text-sm text-muted">
                No webhook or e-mail delivery was attempted — the rule has neither enabled.
              </p>
            ) : (
              <div className="space-y-2">
                {deliveries.map((d) => (
                  <div key={d.id} className="rounded-lg border border-border p-2.5 text-xs space-y-1">
                    <div className="flex flex-wrap items-baseline gap-2">
                      <span className={clsx("font-medium", d.ok ? "text-ok" : "text-danger")}>
                        {d.ok ? "delivered" : "failed"}
                      </span>
                      <span className="uppercase tracking-wide text-muted">{d.channel}</span>
                      <span className="font-mono">{d.target}</span>
                      {d.status ? <span className="text-muted">HTTP {d.status}</span> : null}
                      <span className="text-muted ml-auto">{d.attemptedAt.slice(0, 19).replace("T", " ")}</span>
                    </div>
                    {d.detail && <pre className="whitespace-pre-wrap break-all text-muted">{d.detail}</pre>}
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>

        {!e.acknowledged && (
          <div className="flex justify-end gap-2 p-4 border-t border-border">
            <button className="btn-ghost px-3 py-1.5 text-sm" onClick={onClose}>
              Close
            </button>
            <button className="btn-primary px-3 py-1.5 text-sm" onClick={onAck}>
              <Check className="h-4 w-4" /> Acknowledge
            </button>
          </div>
        )}
      </div>
    </div>
  );
}

// formatDuration renders how long a condition held, matching the engine's own
// wording in resolved messages.
function formatDuration(sec: number): string {
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m ${sec % 60}s`;
  return `${Math.floor(sec / 3600)}h ${Math.floor((sec % 3600) / 60)}m`;
}

// ---- Rules ------------------------------------------------------------------

const STATE_EVENTS = [
  { id: "die", label: "Died / exited" },
  { id: "kill", label: "Killed" },
  { id: "oom", label: "Out of memory" },
  { id: "stop", label: "Stopped" },
  { id: "health_status: unhealthy", label: "Unhealthy" },
];

// Exported for Alerts.networkRule.dom.test.tsx.
export function Rules() {
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
  const { user: me } = useAuth();
  const [email, setEmail] = useState(existing?.email ?? false);
  // Recipients for THIS rule. Prefilled from the signed-in account's address the
  // first time e-mail is switched on, so the common case needs no typing; empty
  // means "use the instance-wide SMTP recipient".
  const [emails, setEmails] = useState<string>((existing?.emails ?? []).join(", "));
  const [cooldown, setCooldown] = useState(existing?.cooldownSec ?? 60);

  // type-specific
  const [events, setEvents] = useState<Set<string>>(new Set((cfg.events as string[]) ?? ["die"]));
  const [metric, setMetric] = useState<Metric>((cfg.metric as Metric) ?? "cpu");
  const [op, setOp] = useState<">" | "<">((cfg.op as ">" | "<") ?? ">");
  // A rate metric's threshold is stored in bytes/s but edited here as MB/s.
  const [threshold, setThreshold] = useState(() => {
    const raw = (cfg.threshold as number) ?? 80;
    return isRateMetric((cfg.metric as Metric) ?? "cpu") ? raw / (1024 * 1024) : raw;
  });
  const [duration, setDuration] = useState((cfg.durationSec as number) ?? 30);
  const [pattern, setPattern] = useState((cfg.pattern as string) ?? "");
  const [isRegex, setIsRegex] = useState((cfg.isRegex as boolean) ?? false);
  const [windowSec, setWindowSec] = useState((cfg.windowSec as number) ?? 60);
  const [count, setCount] = useState((cfg.count as number) ?? 3);
  // "network" type: alert on the INCREASE of a cumulative counter over a
  // window, never its absolute value — see the help text below the fields.
  const [netMetric, setNetMetric] = useState<"netdrops" | "neterrors">((cfg.metric as "netdrops" | "neterrors") ?? "netdrops");
  const [netThreshold, setNetThreshold] = useState((cfg.threshold as number) ?? 1);
  const [netWindowSec, setNetWindowSec] = useState((cfg.windowSec as number) ?? 300);
  const [busy, setBusy] = useState(false);

  const buildConfig = (): unknown => {
    switch (type) {
      case "state":
        return { events: [...events] };
      case "resource":
        return { metric, op, threshold: isRateMetric(metric) ? Math.round(threshold * 1024 * 1024) : threshold, durationSec: duration };
      case "log":
        return { pattern, isRegex };
      case "restart":
        return { windowSec, count };
      case "network":
        return { metric: netMetric, threshold: netThreshold, windowSec: netWindowSec };
    }
  };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const body = {
        name, type, target, config: buildConfig(), severity, webhookId, email,
        emails: email ? emails.split(",").map((e) => e.trim()).filter(Boolean) : [],
        cooldownSec: cooldown,
      };
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
            <option value="network">Network drops / errors</option>
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
            <select className="input" value={metric} onChange={(e) => setMetric(e.target.value as Metric)}>
              <option value="cpu">CPU % (of one core)</option>
              <option value="cpu_total">CPU % (of all cores)</option>
              <option value="mem">Memory % (of container limit)</option>
              <option value="netrx_rate">Network RX rate</option>
              <option value="nettx_rate">Network TX rate</option>
            </select>
            <span className="block text-xs text-muted mt-1">
              {metric === "cpu"
                ? "Docker's own figure: 100% is one core, so a container busy on 4 cores reads ~400%. A fixed threshold here fires constantly on multi-core hosts."
                : metric === "cpu_total"
                  ? "Normalised across the host's cores, so 0–100% whatever the core count. Usually what you want."
                  : metric === "mem"
                    ? "Share of the container's memory limit, not of host RAM."
                    : "The live per-poll rate (same figure the dashboard shows), not averaged over a window."}
            </span>
          </div>
          <div>
            <label className="label">Operator</label>
            <select className="input" value={op} onChange={(e) => setOp(e.target.value as ">" | "<")}>
              <option value=">">above</option>
              <option value="<">below</option>
            </select>
          </div>
          <div>
            <label className="label">{isRateMetric(metric) ? "Threshold (MB/s)" : "Threshold %"}</label>
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
      {type === "network" && (
        <div>
          <div className="grid grid-cols-3 gap-4">
            <div>
              <label className="label">Metric</label>
              <select className="input" value={netMetric} onChange={(e) => setNetMetric(e.target.value as "netdrops" | "neterrors")}>
                <option value="netdrops">Dropped packets</option>
                <option value="neterrors">Interface errors</option>
              </select>
            </div>
            <div>
              <label className="label">Increase by at least</label>
              <input className="input" type="number" value={netThreshold} onChange={(e) => setNetThreshold(+e.target.value)} />
            </div>
            <div>
              <label className="label">Within (seconds)</label>
              <input className="input" type="number" value={netWindowSec} onChange={(e) => setNetWindowSec(+e.target.value)} />
            </div>
          </div>
          <span className="block text-xs text-muted mt-1">
            Fires on the INCREASE within the window, never on the counter's absolute value — a container whose drops sat
            at a high total since a bad afternoon last month won't trigger this by itself.
          </span>
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
        <input type="checkbox" checked={email} onChange={(e) => {
          setEmail(e.target.checked);
          // Prefill on first enable only — never overwrite what the user typed.
          if (e.target.checked && !emails.trim() && me?.email) setEmails(me.email);
        }} />
        Also send an email (an admin configures the SMTP server under Settings → Email)
      </label>
      {email && (
        <label className="block">
          <span className="label">Recipients</span>
          <input className="input" value={emails} onChange={(e) => setEmails(e.target.value)}
            placeholder="ops@example.com, oncall@example.com" />
          <span className="block text-xs text-muted mt-1">
            {me?.email
              ? "Comma-separated. Prefilled from your account address — clear it to use the instance-wide recipient instead."
              : "Comma-separated. Leave empty to use the instance-wide recipient, or set an address on your account to prefill it here."}
          </span>
        </label>
      )}

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

// ---- Maintenance windows -----------------------------------------------------

const WEEKDAY_LABELS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

// A minimal host shape for the maintenance-window picker — deliberately
// narrower than the full Host type, since the fallback below (for a caller
// without the "hosts" section) can only ever know an id, never a name.
interface HostOption {
  id: number;
  name: string;
}

function toInputDateTime(iso: string): string {
  const d = new Date(iso);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
function toInputDate(iso: string): string {
  return toInputDateTime(iso).slice(0, 10);
}
function fromInputDateTime(value: string): string {
  return new Date(value).toISOString();
}
function fromInputDate(value: string): string {
  return new Date(`${value}T00:00`).toISOString();
}

// zonedDateString/fromZonedDate are toInputDate/fromInputDate's counterparts
// for a recurring window's SERIES start/end date, which the server
// interprets as a calendar date in the window's OWN schedule timezone, not
// the browser's. toInputDate/fromInputDate round-trip through the browser's
// local calendar date instead — harmless when they happen to match, but
// when they don't, editing and re-saving a window WITHOUT changing its date
// at all silently shifts it to a different absolute instant, which the
// server then reads back as a different calendar date in the window's own
// timezone (see the regression test for a worked example).
function zonedDateString(iso: string, tz: string): string {
  try {
    return new Intl.DateTimeFormat("en-CA", { timeZone: tz || "UTC", year: "numeric", month: "2-digit", day: "2-digit" }).format(new Date(iso));
  } catch {
    return toInputDate(iso);
  }
}
function fromZonedDate(value: string, tz: string): string {
  const zone = tz || "UTC";
  try {
    // Start from a UTC guess at midnight of the requested date, read what
    // that instant reads as IN the target zone, and correct by the
    // difference — the standard offset-free way to place a wall-clock time
    // into a named IANA zone without a date library.
    const utcGuess = new Date(`${value}T00:00:00Z`);
    const fmt = new Intl.DateTimeFormat("en-US", {
      timeZone: zone, hour12: false,
      year: "numeric", month: "2-digit", day: "2-digit",
      hour: "2-digit", minute: "2-digit", second: "2-digit",
    });
    const parts: Record<string, string> = {};
    for (const p of fmt.formatToParts(utcGuess)) if (p.type !== "literal") parts[p.type] = p.value;
    const hour = parts.hour === "24" ? 0 : Number(parts.hour); // some locales report midnight as "24"
    const asIfUTC = Date.UTC(Number(parts.year), Number(parts.month) - 1, Number(parts.day), hour, Number(parts.minute), Number(parts.second));
    const offsetMs = asIfUTC - utcGuess.getTime();
    return new Date(utcGuess.getTime() - offsetMs).toISOString();
  } catch {
    return fromInputDate(value);
  }
}

function windowStatus(w: MaintenanceWindow): { label: string; cls: string } {
  if (w.ended) return { label: "Ended", cls: "bg-panel2 text-muted" };
  if (w.recurring) return { label: "Recurring", cls: "bg-accent/15 text-accent" };
  // A one-off window always carries a real endsAt (the server requires it);
  // only a recurring series — handled above — can omit it.
  const now = Date.now();
  if (new Date(w.endsAt ?? 0).getTime() <= now) return { label: "Expired", cls: "bg-panel2 text-muted" };
  if (new Date(w.startsAt).getTime() > now) return { label: "Scheduled", cls: "bg-accent/15 text-accent" };
  return { label: "Active", cls: "bg-warn/15 text-warn" };
}

// Exported (unlike its sibling sub-views) so it can be exercised directly in
// a DOM test without mounting the whole Alerts page — the Feed tab's polling
// (useAlertPulse) has nothing to do with this surface and would only add
// unrelated setup/teardown to a maintenance-window test.
export function MaintenanceWindows() {
  const [windows, setWindows] = useState<MaintenanceWindow[] | null>(null);
  const [rules, setRules] = useState<AlertRule[]>([]);
  const [hosts, setHosts] = useState<HostOption[]>([]);
  const [showForm, setShowForm] = useState(false);
  const [editing, setEditing] = useState<MaintenanceWindow | null>(null);
  const dialogs = useDialogs();

  const load = useCallback(() => {
    api.maintenanceWindows().then(setWindows).catch(() => setWindows([]));
    api.alertRules().then(setRules).catch(() => {});
    api.hosts().then(setHosts).catch(() => {
      // No "hosts" section (e.g. the built-in Operator role, which grants
      // "alerts" but deliberately not "hosts") — fall back to just the host
      // ids this grant can reach, so the picker still works (as "host #N"
      // chips) instead of silently looking like zero hosts exist.
      api.myAccess().then((access) => {
        const g = access.effective?.find((eg) => eg.section === "alerts");
        if (g && !g.allHosts && g.hosts) {
          setHosts(g.hosts.map((id) => ({ id, name: `host #${id}` })));
        }
      }).catch(() => {});
    });
  }, []);
  useEffect(() => load(), [load]);

  const end = async (w: MaintenanceWindow) => {
    if (!(await dialogs.confirm({
      title: "End maintenance window",
      message: <>Stop silencing <code className="font-mono text-text">{w.name}</code> now? Alerts covered by it will start paging again immediately.</>,
      confirmLabel: "End now",
    }))) return;
    await api.endMaintenanceWindow(w.id);
    load();
  };
  const del = async (w: MaintenanceWindow) => {
    if (!(await dialogs.confirm({
      title: "Delete maintenance window",
      message: <>Delete <code className="font-mono text-text">{w.name}</code>? This does not undo any silencing that already happened.</>,
      danger: true, confirmLabel: "Delete",
    }))) return;
    await api.deleteMaintenanceWindow(w.id);
    load();
  };

  if (!windows) return <Loading />;

  const hostName = (id: number) => hosts.find((h) => h.id === id)?.name ?? `host #${id}`;
  const scopeSummary = (w: MaintenanceWindow): string => {
    // Defensive: the server always sends [] rather than null for an
    // unrestricted scope, but a nil Go slice marshals to JSON null, and an
    // empty scope is the COMMON case (every auto-silence window has no
    // severity restriction) — so a regression here would crash the whole
    // tab, not just show a blank cell. Tolerate null/undefined too.
    const hostIds = w.hostIds ?? [];
    const severities = w.severities ?? [];
    const parts: string[] = [];
    if (hostIds.length > 0) parts.push(hostIds.map(hostName).join(", "));
    if (w.project) parts.push(`project~"${w.project}"`);
    if (w.container) parts.push(`container~"${w.container}"`);
    if (w.ruleId != null) parts.push(rules.find((r) => r.id === w.ruleId)?.name ?? `rule #${w.ruleId}`);
    if (severities.length > 0) parts.push(severities.join("/"));
    return parts.length > 0 ? parts.join(" · ") : "everything";
  };
  const scheduleSummary = (w: MaintenanceWindow): string => {
    // A one-off window always carries a real endsAt; only a recurring
    // series (handled below) can leave it unset.
    if (!w.recurring) return `${new Date(w.startsAt).toLocaleString()} → ${new Date(w.endsAt ?? 0).toLocaleString()}`;
    const days = (w.weekdays ?? []).map((d) => WEEKDAY_LABELS[d]).join(", ") || "—";
    const until = w.endsAt ? ` until ${new Date(w.endsAt).toLocaleDateString()}` : "";
    return `Every ${days} at ${w.timeOfDay || "?"} for ${w.durationMin ?? 0}m${until}`;
  };

  return (
    <div className="space-y-4">
      <p className="text-sm text-muted">
        Suppress alert delivery for planned work — events still get recorded, just not paged. Distinct from a
        disabled host, which is not monitored at all.
      </p>
      <div className="flex justify-end">
        <button className="btn-primary" onClick={() => { setEditing(null); setShowForm((v) => !v); }}>
          <Plus className="h-4 w-4" /> New window
        </button>
      </div>
      {(showForm || editing) && (
        <MaintenanceWindowForm
          key={editing?.id ?? "new"}
          rules={rules}
          hosts={hosts}
          existing={editing}
          onDone={() => { setShowForm(false); setEditing(null); load(); }}
          onCancel={() => { setShowForm(false); setEditing(null); }}
        />
      )}
      {windows.length === 0 ? (
        <EmptyState title="No maintenance windows" hint="Create one to silence alerts during planned work." />
      ) : (
        <div className="card overflow-hidden">
          <table className="w-full text-sm">
            <thead className="text-muted text-xs uppercase tracking-wide">
              <tr className="border-b border-border">
                <th className="text-left font-medium px-4 py-3">Name</th>
                <th className="text-left font-medium px-4 py-3">Scope</th>
                <th className="text-left font-medium px-4 py-3">Schedule</th>
                <th className="text-left font-medium px-4 py-3">Status</th>
                <th className="px-4 py-3"></th>
              </tr>
            </thead>
            <tbody>
              {windows.map((w) => {
                const status = windowStatus(w);
                const canEnd = !w.ended && (w.recurring || new Date(w.endsAt ?? 0).getTime() > Date.now());
                return (
                  <tr key={w.id} className="border-b border-border/50">
                    <td className="px-4 py-2.5 font-medium">
                      {w.name}
                      <div className="text-xs text-muted font-normal">{w.reason}</div>
                    </td>
                    <td className="px-4 py-2.5 text-xs text-muted max-w-[260px] truncate" title={scopeSummary(w)}>{scopeSummary(w)}</td>
                    <td className="px-4 py-2.5 text-xs text-muted whitespace-nowrap">{scheduleSummary(w)}</td>
                    <td className="px-4 py-2.5">
                      <span className={clsx("text-xs px-2 py-0.5 rounded-md font-medium", status.cls)}>{status.label}</span>
                    </td>
                    <td className="px-4 py-2.5 text-right">
                      <div className="flex items-center justify-end gap-1">
                        <button className="btn-ghost px-2 py-1" title="Edit" onClick={() => { setShowForm(false); setEditing(w); }}><Pencil className="h-4 w-4" /></button>
                        {canEnd && (
                          <button className="btn-ghost px-2 py-1" title="End now" onClick={() => end(w)}><Ban className="h-4 w-4" /></button>
                        )}
                        <button className="btn-ghost px-2 py-1 text-danger" title="Delete" onClick={() => del(w)}><Trash2 className="h-4 w-4" /></button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function MaintenanceWindowForm({
  rules, hosts, existing, onDone, onCancel,
}: { rules: AlertRule[]; hosts: HostOption[]; existing?: MaintenanceWindow | null; onDone: () => void; onCancel: () => void }) {
  const [name, setName] = useState(existing?.name ?? "");
  const [reason, setReason] = useState(existing?.reason ?? "");
  const [hostIds, setHostIds] = useState<number[]>(existing?.hostIds ?? []);
  const [project, setProject] = useState(existing?.project ?? "");
  const [container, setContainer] = useState(existing?.container ?? "");
  const [ruleId, setRuleId] = useState<number | null>(existing?.ruleId ?? null);
  const [severities, setSeverities] = useState<Set<Severity>>(new Set(existing?.severities ?? []));
  const [recurring, setRecurring] = useState(existing?.recurring ?? false);

  const nowIso = new Date().toISOString();
  const [startLocal, setStartLocal] = useState(toInputDateTime(!existing || !existing.recurring ? existing?.startsAt ?? nowIso : nowIso));
  // A one-off existing window always carries a real endsAt (the server
  // requires it); only a recurring series can leave it unset.
  const existingDurationMin = existing && !existing.recurring
    ? Math.max(1, Math.round((new Date(existing.endsAt ?? existing.startsAt).getTime() - new Date(existing.startsAt).getTime()) / 60000))
    : existing?.durationMin ?? 60;
  const [durationMin, setDurationMin] = useState(existingDurationMin);

  // An EXISTING window's stored timezone ("" meaning UTC, same convention
  // the server uses) must be preserved as-is when merely editing it —
  // falling back to the browser's own timezone here would silently change
  // a saved window's actual wall-clock schedule. Only a brand-new window
  // defaults to the browser's timezone. Computed before the series
  // start/end date fields below, which need it to read/write the right
  // calendar date in THIS timezone, not the browser's own.
  const tz = existing ? (existing.timezone || "UTC") : Intl.DateTimeFormat().resolvedOptions().timeZone;
  const [seriesStartDate, setSeriesStartDate] = useState(zonedDateString(existing?.recurring ? existing.startsAt : nowIso, tz));
  const [seriesEndDate, setSeriesEndDate] = useState(existing?.recurring && existing.endsAt ? zonedDateString(existing.endsAt, tz) : "");
  const [weekdays, setWeekdays] = useState<Set<number>>(new Set(existing?.weekdays ?? []));
  const [timeOfDay, setTimeOfDay] = useState(existing?.timeOfDay ?? "02:00");

  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const toggleHost = (id: number) => setHostIds((prev) => (prev.includes(id) ? prev.filter((h) => h !== id) : [...prev, id]));
  const toggleSeverity = (s: Severity) => setSeverities((prev) => {
    const next = new Set(prev);
    if (next.has(s)) next.delete(s); else next.add(s);
    return next;
  });
  const toggleWeekday = (d: number) => setWeekdays((prev) => {
    const next = new Set(prev);
    if (next.has(d)) next.delete(d); else next.add(d);
    return next;
  });

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr("");
    try {
      const shared = {
        name, reason, hostIds, project, container, ruleId,
        severities: [...severities],
      };
      const body: MaintenanceWindowBody = recurring
        ? {
          ...shared, recurring: true,
          // In the window's OWN schedule timezone, not the browser's — see
          // zonedDateString/fromZonedDate's doc comment for why that matters.
          startsAt: fromZonedDate(seriesStartDate, tz),
          // undefined (never ""): Go's time.Time JSON decoder rejects an
          // empty string for an open-ended series, but a missing key leaves
          // it at its zero value, which the server treats as indefinite.
          endsAt: seriesEndDate ? fromZonedDate(seriesEndDate, tz) : undefined,
          weekdays: [...weekdays], timeOfDay, durationMin, timezone: tz,
        }
        : {
          ...shared, recurring: false,
          startsAt: fromInputDateTime(startLocal),
          endsAt: new Date(new Date(fromInputDateTime(startLocal)).getTime() + durationMin * 60000).toISOString(),
        };
      if (existing) await api.updateMaintenanceWindow(existing.id, body);
      else await api.createMaintenanceWindow(body);
      onDone();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="card p-5 space-y-4">
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div>
          <label className="label">Name</label>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} required />
        </div>
        <div>
          <label className="label">Reason</label>
          <input className="input" value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Why — this suppresses paging, so make it explainable later" required />
        </div>
      </div>

      <div>
        <label className="label">Hosts (blank = every host)</label>
        <div className="flex flex-wrap gap-1.5">
          {hosts.length === 0 ? (
            <span className="text-xs text-muted">No hosts configured.</span>
          ) : hosts.map((h) => (
            <button
              key={h.id} type="button" onClick={() => toggleHost(h.id)}
              className={clsx("text-xs px-2 py-0.5 rounded-md border",
                hostIds.includes(h.id) ? "bg-accent/20 border-accent/40 text-text" : "border-border text-muted")}
            >
              {h.name}
            </button>
          ))}
        </div>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <div>
          <label className="label">Project (compose stack, contains)</label>
          <input className="input" value={project} onChange={(e) => setProject(e.target.value)} placeholder="blank = every project" />
        </div>
        <div>
          <label className="label">Container (name contains)</label>
          <input className="input" value={container} onChange={(e) => setContainer(e.target.value)} placeholder="blank = every container" />
        </div>
        <div>
          <label className="label">Rule</label>
          <select className="input" value={ruleId ?? ""} onChange={(e) => setRuleId(e.target.value ? Number(e.target.value) : null)}>
            <option value="">Any rule</option>
            {rules.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
          </select>
        </div>
      </div>

      <div>
        <label className="label">Severities (blank = every severity)</label>
        <div className="flex flex-wrap gap-1.5">
          {(["info", "warning", "critical"] as Severity[]).map((s) => (
            <button
              key={s} type="button" onClick={() => toggleSeverity(s)}
              className={clsx("text-xs px-2 py-0.5 rounded-md border capitalize",
                severities.has(s) ? "bg-accent/20 border-accent/40 text-text" : "border-border text-muted")}
            >
              {s}
            </button>
          ))}
        </div>
      </div>

      <div className="flex gap-1 rounded-lg bg-panel2/50 p-0.5 max-w-xs">
        <button
          type="button" onClick={() => setRecurring(false)}
          className={clsx("flex-1 px-2 py-1.5 rounded-md text-sm", !recurring ? "bg-panel text-text shadow-sm" : "text-muted hover:text-text")}
        >
          One-off
        </button>
        <button
          type="button" onClick={() => setRecurring(true)}
          className={clsx("flex-1 px-2 py-1.5 rounded-md text-sm", recurring ? "bg-panel text-text shadow-sm" : "text-muted hover:text-text")}
        >
          Recurring
        </button>
      </div>

      {!recurring ? (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div>
            <label className="label">Starts</label>
            <input type="datetime-local" className="input" value={startLocal} onChange={(e) => setStartLocal(e.target.value)} required />
          </div>
          <div>
            <label className="label">Duration (minutes)</label>
            <input type="number" min={1} className="input" value={durationMin} onChange={(e) => setDurationMin(Number(e.target.value))} required />
          </div>
        </div>
      ) : (
        <div className="space-y-3">
          <div>
            <label className="label">Weekdays</label>
            <div className="flex flex-wrap gap-1.5">
              {WEEKDAY_LABELS.map((label, d) => (
                <button
                  key={d} type="button" onClick={() => toggleWeekday(d)}
                  className={clsx("text-xs px-2 py-0.5 rounded-md border",
                    weekdays.has(d) ? "bg-accent/20 border-accent/40 text-text" : "border-border text-muted")}
                >
                  {label}
                </button>
              ))}
            </div>
          </div>
          <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
            <div>
              <label className="label">Time of day</label>
              <input type="time" className="input" value={timeOfDay} onChange={(e) => setTimeOfDay(e.target.value)} required />
            </div>
            <div>
              <label className="label">Duration (minutes)</label>
              <input type="number" min={1} className="input" value={durationMin} onChange={(e) => setDurationMin(Number(e.target.value))} required />
            </div>
            <div>
              <label className="label">Series starts</label>
              <input type="date" className="input" value={seriesStartDate} onChange={(e) => setSeriesStartDate(e.target.value)} required />
            </div>
            <div>
              <label className="label">Series ends (optional)</label>
              <input type="date" className="input" value={seriesEndDate} onChange={(e) => setSeriesEndDate(e.target.value)} />
            </div>
          </div>
          <p className="text-xs text-muted">Evaluated in your browser's timezone, <span className="font-mono">{tz}</span>.</p>
        </div>
      )}

      {err && <p className="text-sm text-danger">{err}</p>}
      <div className="flex justify-end gap-2">
        <button type="button" className="btn-ghost" onClick={onCancel}>Cancel</button>
        <button className="btn-primary" disabled={busy}>{busy ? "Saving…" : existing ? "Save changes" : "Create window"}</button>
      </div>
    </form>
  );
}

// ---- Email (SMTP) -----------------------------------------------------------

function Loading() {
  return <div className="flex items-center gap-2 text-muted"><Spinner /> Loading…</div>;
}
