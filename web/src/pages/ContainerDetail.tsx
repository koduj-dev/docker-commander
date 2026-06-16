import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ArrowLeft, Camera, Download, FileSearch, Play, Radar, RotateCw, Settings, Square, X, Loader2 } from "lucide-react";
import { api, fileApiForContainer } from "../lib/api";
import type { ContainerDetail as Detail, DiffEntry, LogLine, PortMapping, PortProbe, StatsSample, TopResult } from "../lib/types";
import { live, ensureLive } from "../lib/live";
import { shortId } from "../lib/format";
import { StateBadge, Spinner } from "../components/ui";
import { Tabs } from "../components/Tabs";
import { StatsCharts } from "../components/StatsChart";
import { MetricsHistory } from "../components/MetricsHistory";
import { LogViewer } from "../components/LogViewer";
import { Terminal } from "../components/Terminal";
import { InspectModal } from "../components/InspectModal";
import { triggerDownload } from "../components/LoadModal";
import { FileBrowser } from "../components/FileBrowser";

const MAX_SAMPLES = 60;
const MAX_LOGS = 2000;

export function ContainerDetail() {
  const { id = "" } = useParams();
  const [detail, setDetail] = useState<Detail | null>(null);
  const [samples, setSamples] = useState<StatsSample[]>([]);
  const [logs, setLogs] = useState<LogLine[]>([]);
  const [tab, setTab] = useState<"overview" | "logs" | "console" | "env" | "processes" | "files" | "changes">("overview");
  const [inspecting, setInspecting] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [commitOpen, setCommitOpen] = useState(false);
  const logBuf = useRef<LogLine[]>([]);

  const load = () => api.container(id).then(setDetail).catch(() => {});

  useEffect(() => {
    void load();
    ensureLive();

    const statsSub = `stats:${id}`;
    const logsSub = `logs:${id}`;

    live.subscribeStats(statsSub, id, (f) => {
      if (f.type === "stats") {
        setSamples((prev) => [...prev, f.data as StatsSample].slice(-MAX_SAMPLES));
      }
    });
    live.subscribeLogs(logsSub, id, "300", (f) => {
      if (f.type === "log") {
        logBuf.current.push(f.data as LogLine);
      }
    });

    // Flush buffered log lines on an interval to avoid re-rendering per line.
    // Capture and clear the buffer up-front: the setState updater must close
    // over a stable snapshot, never read the mutable ref (it runs after this
    // tick — and twice under StrictMode — so reading logBuf.current would lose
    // lines once we reset it).
    const flush = setInterval(() => {
      if (logBuf.current.length === 0) return;
      const pending = logBuf.current;
      logBuf.current = [];
      setLogs((prev) => [...prev, ...pending].slice(-MAX_LOGS));
    }, 250);

    return () => {
      live.unsubscribe(statsSub);
      live.unsubscribe(logsSub);
      clearInterval(flush);
    };
  }, [id]);

  const act = async (action: string) => {
    await api.containerAction(id, action);
    await load();
  };

  if (!detail) return <div className="p-6 flex items-center gap-2 text-muted"><Spinner /> Loading…</div>;

  const running = detail.state === "running";

  return (
    <>
      <div className="flex items-center justify-between h-16 px-6 border-b border-border sticky top-0 bg-bg/80 backdrop-blur-sm z-10">
        <div className="flex items-center gap-3 min-w-0">
          <Link to="/containers" className="btn-ghost px-2 py-2"><ArrowLeft className="h-4 w-4" /></Link>
          <div className="min-w-0">
            <div className="font-semibold truncate flex items-center gap-3">
              {detail.name}
              <StateBadge state={detail.state} />
            </div>
            <div className="text-xs text-muted font-mono">{shortId(detail.id)} · {detail.image}</div>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <button className="btn-ghost" onClick={() => setCommitOpen(true)} title="Commit to a new image"><Camera className="h-4 w-4" /> Commit</button>
          <button className="btn-ghost" onClick={() => setSettingsOpen(true)} title="Rename / limits / restart policy"><Settings className="h-4 w-4" /></button>
          <button className="btn-ghost" onClick={() => triggerDownload(api.exportContainerUrl(id))} title="Export filesystem (download tar)"><Download className="h-4 w-4" /> Export</button>
          <button className="btn-ghost" onClick={() => setInspecting(true)}><FileSearch className="h-4 w-4" /> Inspect</button>
          {running ? (
            <>
              <button className="btn-ghost" onClick={() => act("restart")}><RotateCw className="h-4 w-4" /> Restart</button>
              <button className="btn-danger" onClick={() => act("stop")}><Square className="h-4 w-4" /> Stop</button>
            </>
          ) : (
            <button className="btn-primary" onClick={() => act("start")}><Play className="h-4 w-4" /> Start</button>
          )}
        </div>
      </div>
      {inspecting && <InspectModal kind="container" id={id} title={detail.name} onClose={() => setInspecting(false)} />}
      {settingsOpen && <SettingsModal detail={detail} onClose={() => setSettingsOpen(false)} onDone={() => { setSettingsOpen(false); load(); }} />}
      {commitOpen && <CommitModal id={id} name={detail.name} onClose={() => setCommitOpen(false)} />}

      <div className="p-6 space-y-6">
        <StatsCharts data={samples} />
        <MetricsHistory containerId={id} />

        <Tabs
          active={tab}
          onChange={setTab}
          tabs={[
            { key: "overview", label: "Overview" },
            { key: "logs", label: "Logs" },
            { key: "console", label: "Console" },
            { key: "processes", label: "Processes" },
            { key: "files", label: "Files" },
            { key: "changes", label: "Changes" },
            { key: "env", label: "Env" },
          ]}
        />

        {tab === "overview" && <Overview detail={detail} />}
        {tab === "logs" && <LogViewer lines={logs} />}
        {tab === "console" && (running ? <Terminal containerId={id} /> : <div className="text-sm text-muted">Container is not running — start it to open a shell.</div>)}
        {tab === "processes" && (running ? <ProcessTable id={id} /> : <div className="text-sm text-muted">Container is not running — no processes.</div>)}
        {tab === "files" && (running ? <FileBrowser fs={fileApiForContainer(id)} /> : <div className="text-sm text-muted">Container is not running — start it to browse its filesystem.</div>)}
        {tab === "changes" && <DiffList id={id} />}
        {tab === "env" && <EnvList env={detail.env ?? []} />}
      </div>
    </>
  );
}

function Overview({ detail }: { detail: Detail }) {
  return (
    <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
      <Info title="General">
        <Row k="Status" v={detail.status} />
        {detail.health && <Row k="Health" v={detail.health} />}
        <Row k="Restart count" v={String(detail.restartCount)} />
        <Row k="Restart policy" v={detail.restartPolicy || "—"} />
        <Row k="Command" v={<code className="font-mono text-xs">{detail.command.join(" ")}</code>} />
        <Row k="Created" v={detail.created.slice(0, 19).replace("T", " ")} />
      </Info>
      <Info title="Networks">
        {(detail.networks ?? []).length === 0 ? (
          <div className="text-sm text-muted">No networks.</div>
        ) : (
          (detail.networks ?? []).map((n) => (
            <Row key={n.name} k={n.name} v={<code className="font-mono text-xs">{n.ipAddress || "—"}</code>} />
          ))
        )}
      </Info>
      <PortsPanel id={detail.id} ports={detail.ports ?? []} />
      <Info title="Mounts">
        {(detail.mounts ?? []).length === 0 ? (
          <div className="text-sm text-muted">No mounts.</div>
        ) : (
          (detail.mounts ?? []).map((m, i) => (
            <Row key={i} k={m.destination} v={<span className="text-xs font-mono text-muted">{m.source} {m.rw ? "(rw)" : "(ro)"}</span>} />
          ))
        )}
      </Info>
    </div>
  );
}

// ProcessTable shows the live process list inside the container (docker top).
function ProcessTable({ id }: { id: string }) {
  const [top, setTop] = useState<TopResult | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let alive = true;
    const load = () => api.containerTop(id).then((t) => alive && setTop(t)).catch((e) => alive && setError(e instanceof Error ? e.message : "failed"));
    load();
    const t = setInterval(load, 3000); // refresh periodically
    return () => { alive = false; clearInterval(t); };
  }, [id]);

  if (error) return <div className="text-sm text-danger">{error}</div>;
  if (!top) return <div className="flex items-center gap-2 text-muted text-sm"><Spinner className="h-4 w-4" /> Loading…</div>;

  return (
    <div className="card overflow-auto">
      <table className="w-full text-xs font-mono">
        <thead>
          <tr className="text-muted text-left border-b border-border">
            {top.titles.map((t) => <th key={t} className="px-3 py-2 font-medium whitespace-nowrap">{t}</th>)}
          </tr>
        </thead>
        <tbody>
          {top.processes.map((row, i) => (
            <tr key={i} className="border-b border-border/50 hover:bg-panel2/50">
              {row.map((cell, j) => <td key={j} className="px-3 py-1.5 whitespace-nowrap break-all">{cell}</td>)}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// DiffList shows filesystem changes since the container started (docker diff).
function DiffList({ id }: { id: string }) {
  const [diff, setDiff] = useState<DiffEntry[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.containerDiff(id).then(setDiff).catch((e) => setError(e instanceof Error ? e.message : "failed"));
  }, [id]);

  if (error) return <div className="text-sm text-danger">{error}</div>;
  if (!diff) return <div className="flex items-center gap-2 text-muted text-sm"><Spinner className="h-4 w-4" /> Loading…</div>;
  if (diff.length === 0) return <div className="text-sm text-muted">No filesystem changes since the container started.</div>;

  const mark = { added: { c: "text-ok", s: "A" }, modified: { c: "text-warn", s: "C" }, deleted: { c: "text-danger", s: "D" }, unknown: { c: "text-muted", s: "?" } } as const;
  return (
    <div className="card p-3 font-mono text-xs space-y-0.5 max-h-112 overflow-auto">
      {diff.map((d, i) => (
        <div key={i} className="flex gap-2">
          <span className={`w-4 shrink-0 font-bold ${mark[d.kind].c}`}>{mark[d.kind].s}</span>
          <span className="break-all">{d.path}</span>
        </div>
      ))}
    </div>
  );
}

function EnvList({ env }: { env: string[] }) {
  if (env.length === 0) return <div className="text-sm text-muted">No environment variables.</div>;
  return (
    <div className="card p-4 font-mono text-xs space-y-1">
      {env.map((e, i) => {
        const eq = e.indexOf("=");
        const key = eq > 0 ? e.slice(0, eq) : e;
        const val = eq > 0 ? e.slice(eq + 1) : "";
        return (
          <div key={i} className="flex gap-2">
            <span className="text-accent">{key}</span>
            <span className="text-muted">=</span>
            <span className="text-text/90 break-all">{val}</span>
          </div>
        );
      })}
    </div>
  );
}

// PortsPanel lists the container's ports and can actively probe the published
// ones to fingerprint what's really listening (port numbers often lie).
function PortsPanel({ id, ports }: { id: string; ports: PortMapping[] }) {
  const [probes, setProbes] = useState<PortProbe[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const hasPublished = ports.some((p) => p.publicPort);
  const probe = async () => {
    setBusy(true);
    setErr("");
    try {
      setProbes(await api.probePorts(id));
    } catch (e) {
      setErr(e instanceof Error ? e.message : "probe failed");
    } finally {
      setBusy(false);
    }
  };
  const byKey = new Map((probes ?? []).map((p) => [`${p.publicPort}/${p.type}`, p]));

  const action = hasPublished ? (
    <button className="btn-ghost px-2 py-0.5 text-xs" onClick={probe} disabled={busy} title="Connect to each published port and detect the service">
      {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Radar className="h-3.5 w-3.5" />} Probe
    </button>
  ) : undefined;

  return (
    <Info title="Ports" action={action}>
      {ports.length === 0 ? (
        <div className="text-sm text-muted">No published ports.</div>
      ) : (
        ports.map((p, i) => {
          const pr = p.publicPort ? byKey.get(`${p.publicPort}/${p.type}`) : undefined;
          return (
            <Row
              key={i}
              k={`${p.privatePort}/${p.type}`}
              v={
                <span className="inline-flex items-center gap-2 flex-wrap justify-end">
                  <span className="font-mono text-xs">{p.publicPort ? `${p.ip || "0.0.0.0"}:${p.publicPort}` : "internal"}</span>
                  <ProbeTag port={p} probe={pr} />
                </span>
              }
            />
          );
        })
      )}
      {err && <p className="text-xs text-danger">{err}</p>}
    </Info>
  );
}

// ProbeTag shows the passive guess (from the port number) and, once probed, the
// actively detected service.
function ProbeTag({ port, probe }: { port: PortMapping; probe?: PortProbe }) {
  if (probe) {
    if (!probe.open) {
      return <span className="text-[11px] rounded-sm px-1.5 py-0.5 bg-danger/15 text-danger" title={probe.error}>{probe.error || "closed"}</span>;
    }
    const label = probe.detected || "open · unknown";
    return (
      <span className="text-[11px] rounded-sm px-1.5 py-0.5 bg-ok/15 text-ok" title={probe.info || probe.guessByPort}>
        {label}
        {probe.tls && " 🔒"}
        {probe.info ? ` · ${probe.info}` : ""}
      </span>
    );
  }
  const guess = guessService(port.privatePort, port.type);
  if (guess) return <span className="text-[11px] text-muted" title="guess from the port number — click Probe to verify">~ {guess}</span>;
  return null;
}

// guessService is a small client-side passive hint from the port number. The
// authoritative guess + active detection come from the backend on Probe.
function guessService(port: number, type: string): string {
  if (type === "udp") return { 53: "DNS", 123: "NTP", 161: "SNMP", 514: "syslog" }[port] ?? "";
  const map: Record<number, string> = {
    21: "FTP", 22: "SSH", 23: "Telnet", 25: "SMTP", 53: "DNS", 80: "HTTP", 110: "POP3",
    143: "IMAP", 443: "HTTPS", 465: "SMTP", 587: "SMTP", 1433: "MSSQL", 3000: "HTTP",
    3306: "MySQL", 5432: "PostgreSQL", 5672: "AMQP", 6379: "Redis", 8000: "HTTP",
    8080: "HTTP", 8443: "HTTPS", 9000: "HTTP", 9200: "Elasticsearch", 11211: "Memcached",
    15672: "RabbitMQ", 27017: "MongoDB",
  };
  return map[port] ?? "";
}

function Info({ title, action, children }: { title: string; action?: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="card p-4">
      <div className="flex items-center justify-between mb-3">
        <h3 className="text-xs uppercase tracking-wide text-muted">{title}</h3>
        {action}
      </div>
      <div className="space-y-2">{children}</div>
    </div>
  );
}

function Row({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div className="flex justify-between gap-4 text-sm">
      <span className="text-muted shrink-0">{k}</span>
      <span className="text-right break-all">{v}</span>
    </div>
  );
}

const RESTART = ["", "no", "on-failure", "always", "unless-stopped"];

// SettingsModal renames the container and updates its resource limits and
// restart policy at runtime.
function SettingsModal({ detail, onClose, onDone }: { detail: Detail; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState(detail.name);
  const [memoryMb, setMemoryMb] = useState("");
  const [cpus, setCpus] = useState("");
  const [restartPolicy, setRestartPolicy] = useState(detail.restartPolicy ?? "");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr("");
    try {
      if (name.trim() && name.trim() !== detail.name) {
        const r = await api.renameContainer(detail.id, name.trim());
        if (!r.ok) { setErr(r.error ?? "rename failed"); setBusy(false); return; }
      }
      const r = await api.updateContainer(detail.id, {
        memory: memoryMb ? Math.round(Number(memoryMb) * 1024 * 1024) : 0,
        nanoCpus: cpus ? Math.round(Number(cpus) * 1e9) : 0,
        restartPolicy,
      });
      if (!r.ok) { setErr(r.error ?? "update failed"); setBusy(false); return; }
      onDone();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "request failed"); setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <form className="card w-full max-w-lg" onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <Settings className="h-4 w-4 text-accent" /><div className="font-medium">Container settings</div>
          <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 space-y-3">
          <div>
            <label className="label">Name</label>
            <input className="input" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
            <div>
              <label className="label">Memory (MB)</label>
              <input className="input" type="number" min="0" value={memoryMb} onChange={(e) => setMemoryMb(e.target.value)} placeholder="unchanged" />
            </div>
            <div>
              <label className="label">CPUs</label>
              <input className="input" type="number" min="0" step="0.1" value={cpus} onChange={(e) => setCpus(e.target.value)} placeholder="unchanged" />
            </div>
            <div>
              <label className="label">Restart policy</label>
              <select className="input" value={restartPolicy} onChange={(e) => setRestartPolicy(e.target.value)}>
                {RESTART.map((p) => <option key={p} value={p}>{p || "(unchanged)"}</option>)}
              </select>
            </div>
          </div>
          <p className="text-[11px] text-muted">Limits left blank are sent as 0 (unlimited). Restart policy "(unchanged)" leaves it as-is.</p>
          {err && <p className="text-sm text-danger break-all">{err}</p>}
        </div>
        <div className="flex justify-end gap-2 p-4 border-t border-border">
          <button type="button" className="btn-ghost" onClick={onClose}>Cancel</button>
          <button className="btn-primary" disabled={busy}>{busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Settings className="h-4 w-4" />} Save</button>
        </div>
      </form>
    </div>
  );
}

// CommitModal snapshots the container into a new image.
function CommitModal({ id, name, onClose }: { id: string; name: string; onClose: () => void }) {
  const [ref, setRef] = useState(`${name}:committed`);
  const [comment, setComment] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState("");
  const [err, setErr] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!ref.trim() || busy) return;
    setBusy(true); setErr(""); setResult("");
    try {
      const r = await api.commitContainer(id, { ref: ref.trim(), comment: comment.trim() });
      if (r.ok) setResult(`Created image ${(r.imageId ?? "").slice(7, 19)} as ${ref.trim()}`);
      else setErr(r.error ?? "commit failed");
    } catch (e) {
      setErr(e instanceof Error ? e.message : "request failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <form className="card w-full max-w-lg" onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <Camera className="h-4 w-4 text-accent" /><div className="font-medium">Commit container to image</div>
          <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 space-y-3">
          <div>
            <label className="label">New image reference</label>
            <input className="input font-mono" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="repo:tag" required />
          </div>
          <div>
            <label className="label">Comment (optional)</label>
            <input className="input" value={comment} onChange={(e) => setComment(e.target.value)} />
          </div>
          {result && <p className="text-sm text-ok break-all">{result}</p>}
          {err && <p className="text-sm text-danger break-all">{err}</p>}
        </div>
        <div className="flex justify-end gap-2 p-4 border-t border-border">
          <button type="button" className="btn-ghost" onClick={onClose}>{result ? "Close" : "Cancel"}</button>
          <button className="btn-primary" disabled={!ref.trim() || busy}>{busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Camera className="h-4 w-4" />} Commit</button>
        </div>
      </form>
    </div>
  );
}
