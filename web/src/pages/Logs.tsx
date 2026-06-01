import { useEffect, useMemo, useRef, useState } from "react";
import { Search, X } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { ContainerSummary, LogLine } from "../lib/types";
import { live, ensureLive } from "../lib/live";
import { detectLevel, levelBadge, levelClass, sourcePalette, type Level } from "../lib/loglevel";
import { PageHeader } from "../layout/Shell";
import { Spinner } from "../components/ui";

const MAX_LINES = 3000;

interface Entry {
  source: string;
  color: string;
  stream: "stdout" | "stderr";
  level: Level;
  timestamp?: string;
  message: string;
}

const LEVELS: Level[] = ["error", "warn", "info", "debug", "other"];

// Aggregates live logs from several containers into one searchable, level-aware
// stream. Each source gets a stable color so lines are easy to scan.
export function Logs() {
  const [containers, setContainers] = useState<ContainerSummary[]>([]);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [entries, setEntries] = useState<Entry[]>([]);
  const [filter, setFilter] = useState("");
  const [activeLevels, setActiveLevels] = useState<Set<Level>>(new Set(LEVELS));
  const [stick, setStick] = useState(true);

  const buf = useRef<Entry[]>([]);
  const boxRef = useRef<HTMLDivElement>(null);
  const colorOf = useRef<Map<string, string>>(new Map());

  useEffect(() => {
    api.containers().then((cs) => setContainers(cs.filter((c) => c.state === "running"))).catch(() => {});
    ensureLive();
  }, []);

  // Flush buffered lines periodically to keep rendering smooth under load.
  useEffect(() => {
    const t = setInterval(() => {
      if (buf.current.length) {
        setEntries((prev) => [...prev, ...buf.current].slice(-MAX_LINES));
        buf.current = [];
      }
    }, 250);
    return () => clearInterval(t);
  }, []);

  // Reconcile subscriptions with the selected set.
  useEffect(() => {
    const byName = new Map(containers.map((c) => [c.id, c.name]));
    for (const id of selected) {
      const subId = `glog:${id}`;
      const name = byName.get(id) ?? id.slice(0, 12);
      if (!colorOf.current.has(id)) {
        colorOf.current.set(id, sourcePalette[colorOf.current.size % sourcePalette.length]);
      }
      const color = colorOf.current.get(id)!;
      live.subscribeLogs(subId, id, "100", (f) => {
        if (f.type === "log") {
          const l = f.data as LogLine;
          buf.current.push({
            source: name,
            color,
            stream: l.stream,
            level: detectLevel(l.message),
            timestamp: l.timestamp,
            message: l.message,
          });
        }
      });
    }
    return () => {
      for (const id of selected) live.unsubscribe(`glog:${id}`);
    };
  }, [selected, containers]);

  const toggle = (id: string) =>
    setSelected((prev) => {
      const next = new Set(prev);
      next.has(id) ? next.delete(id) : next.add(id);
      return next;
    });

  const toggleLevel = (lvl: Level) =>
    setActiveLevels((prev) => {
      const next = new Set(prev);
      next.has(lvl) ? next.delete(lvl) : next.add(lvl);
      return next;
    });

  const filtered = useMemo(() => {
    const f = filter.toLowerCase();
    return entries.filter(
      (e) => activeLevels.has(e.level) && (f === "" || e.message.toLowerCase().includes(f))
    );
  }, [entries, filter, activeLevels]);

  useEffect(() => {
    if (stick && boxRef.current) boxRef.current.scrollTop = boxRef.current.scrollHeight;
  }, [filtered, stick]);

  return (
    <>
      <PageHeader title="Logs" />
      <div className="p-6 grid grid-cols-[220px_1fr] gap-4 h-[calc(100vh-4rem)]">
        {/* Source picker */}
        <div className="card p-3 overflow-auto">
          <div className="text-xs uppercase tracking-wide text-muted mb-2">Sources</div>
          {containers.length === 0 ? (
            <div className="text-sm text-muted flex items-center gap-2"><Spinner className="h-4 w-4" /> Loading…</div>
          ) : (
            <div className="space-y-1">
              {containers.map((c) => {
                const on = selected.has(c.id);
                return (
                  <button
                    key={c.id}
                    onClick={() => toggle(c.id)}
                    className={clsx(
                      "w-full text-left flex items-center gap-2 px-2 py-1.5 rounded-md text-sm transition-colors",
                      on ? "bg-panel2 text-text" : "text-muted hover:bg-panel2/60"
                    )}
                  >
                    <span
                      className="h-2.5 w-2.5 rounded-full shrink-0 border border-border"
                      style={{ background: on ? (colorOf.current.get(c.id) ?? "#2496ed") : "transparent" }}
                    />
                    <span className="truncate">{c.name}</span>
                  </button>
                );
              })}
            </div>
          )}
        </div>

        {/* Log stream */}
        <div className="card flex flex-col min-w-0">
          <div className="flex items-center gap-3 p-3 border-b border-border flex-wrap">
            <div className="relative flex-1 min-w-[200px]">
              <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted" />
              <input className="input pl-8 py-1.5" placeholder="Search logs…" value={filter} onChange={(e) => setFilter(e.target.value)} />
            </div>
            {LEVELS.map((lvl) => (
              <button
                key={lvl}
                onClick={() => toggleLevel(lvl)}
                className={clsx(
                  "text-xs px-2 py-1 rounded-md font-medium capitalize transition-colors",
                  activeLevels.has(lvl) ? levelBadge[lvl] : "bg-panel2 text-muted/50"
                )}
              >
                {lvl}
              </button>
            ))}
            <button className="btn-ghost px-2 py-1.5" title="Clear" onClick={() => { setEntries([]); buf.current = []; }}>
              <X className="h-4 w-4" />
            </button>
          </div>
          <div
            ref={boxRef}
            onScroll={(e) => {
              const el = e.currentTarget;
              setStick(el.scrollHeight - el.scrollTop - el.clientHeight < 40);
            }}
            className="flex-1 overflow-auto font-mono text-xs leading-relaxed p-3 space-y-0.5"
          >
            {selected.size === 0 ? (
              <div className="text-muted">Select one or more sources on the left to start streaming.</div>
            ) : filtered.length === 0 ? (
              <div className="text-muted">Waiting for log lines…</div>
            ) : (
              filtered.map((e, i) => (
                <div key={i} className="flex gap-2">
                  <span className="shrink-0 font-medium" style={{ color: e.color }}>{e.source}</span>
                  {e.timestamp && <span className="text-muted/50 shrink-0">{e.timestamp.slice(11, 19)}</span>}
                  <span className={clsx("whitespace-pre-wrap break-all", e.stream === "stderr" ? "text-danger/90" : levelClass[e.level])}>
                    {e.message}
                  </span>
                </div>
              ))
            )}
          </div>
        </div>
      </div>
    </>
  );
}
