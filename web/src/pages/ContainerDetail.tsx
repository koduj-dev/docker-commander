import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ArrowLeft, Play, RotateCw, Square } from "lucide-react";
import { api } from "../lib/api";
import type { ContainerDetail as Detail, LogLine, StatsSample } from "../lib/types";
import { live, ensureLive } from "../lib/live";
import { shortId } from "../lib/format";
import { StateBadge, Spinner } from "../components/ui";
import { StatsCharts } from "../components/StatsChart";
import { LogViewer } from "../components/LogViewer";
import { Terminal } from "../components/Terminal";

const MAX_SAMPLES = 60;
const MAX_LOGS = 2000;

export function ContainerDetail() {
  const { id = "" } = useParams();
  const [detail, setDetail] = useState<Detail | null>(null);
  const [samples, setSamples] = useState<StatsSample[]>([]);
  const [logs, setLogs] = useState<LogLine[]>([]);
  const [tab, setTab] = useState<"overview" | "logs" | "console" | "env">("overview");
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
      <div className="flex items-center justify-between h-16 px-6 border-b border-border sticky top-0 bg-bg/80 backdrop-blur z-10">
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

      <div className="p-6 space-y-6">
        <StatsCharts data={samples} />

        <div className="flex gap-1 border-b border-border">
          {(["overview", "logs", "console", "env"] as const).map((t) => (
            <button
              key={t}
              onClick={() => setTab(t)}
              className={`px-4 py-2 text-sm font-medium capitalize border-b-2 -mb-px transition-colors ${
                tab === t ? "border-accent text-accent" : "border-transparent text-muted hover:text-text"
              }`}
            >
              {t}
            </button>
          ))}
        </div>

        {tab === "overview" && <Overview detail={detail} />}
        {tab === "logs" && <LogViewer lines={logs} />}
        {tab === "console" && (running ? <Terminal containerId={id} /> : <div className="text-sm text-muted">Container is not running — start it to open a shell.</div>)}
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
      <Info title="Ports">
        {(detail.ports ?? []).length === 0 ? (
          <div className="text-sm text-muted">No published ports.</div>
        ) : (
          (detail.ports ?? []).map((p, i) => (
            <Row key={i} k={`${p.privatePort}/${p.type}`} v={p.publicPort ? `${p.ip || "0.0.0.0"}:${p.publicPort}` : "internal"} />
          ))
        )}
      </Info>
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

function Info({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="card p-4">
      <h3 className="text-xs uppercase tracking-wide text-muted mb-3">{title}</h3>
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
