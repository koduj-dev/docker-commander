import { useEffect, useMemo, useRef, useState } from "react";
import { Pause, Play, Search, X } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { ContainerSummary, LogLine } from "../lib/types";
import { live, ensureLive } from "../lib/live";
import { detectLevel, levelBadge, levelClass, sourcePalette, type Level } from "../lib/loglevel";
import { PageHeader } from "../layout/Shell";
import { Spinner } from "../components/ui";

const MAX_LINES = 3000;
const STORAGE_KEY = "dc.logs.selected";

interface Entry {
  containerId: string;
  source: string;
  color: string;
  stream: "stdout" | "stderr";
  level: Level;
  timestamp?: string;
  t: number; // epoch ms, for chronological interleaving across sources
  message: string;
}

// parseTs converts Docker's RFC3339Nano timestamp to epoch ms. The 9-digit
// fraction is trimmed to 3 so Date.parse stays happy; missing/invalid → now.
let arrivalSeq = 0;
function parseTs(ts?: string): number {
  if (ts) {
    const ms = Date.parse(ts.replace(/(\.\d{3})\d+/, "$1"));
    if (!Number.isNaN(ms)) return ms;
  }
  // Keep relative order for lines without a usable timestamp.
  return Date.now() + (arrivalSeq++ % 1000) / 1000;
}

const LEVELS: Level[] = ["error", "warn", "info", "debug", "other"];

function loadSelected(): Set<string> {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    return new Set(raw ? (JSON.parse(raw) as string[]) : []);
  } catch {
    return new Set();
  }
}

// Aggregates live logs from several containers into one searchable, level-aware
// stream. Each source gets a stable color; the selection persists across visits.
export function Logs() {
  const [containers, setContainers] = useState<ContainerSummary[]>([]);
  const [selected, setSelected] = useState<Set<string>>(loadSelected);
  const [entries, setEntries] = useState<Entry[]>([]);
  const [filter, setFilter] = useState("");
  const [useRegex, setUseRegex] = useState(false);
  const [activeLevels, setActiveLevels] = useState<Set<Level>>(new Set(LEVELS));
  const [paused, setPaused] = useState(false);
  const [stick, setStick] = useState(true);

  const buf = useRef<Entry[]>([]);
  const boxRef = useRef<HTMLDivElement>(null);
  const colorOf = useRef<Map<string, string>>(new Map());
  const subscribed = useRef<Set<string>>(new Set());
  const pausedRef = useRef(false);
  pausedRef.current = paused;

  useEffect(() => {
    api.containers().then((cs) => setContainers(cs.filter((c) => c.state === "running"))).catch(() => {});
    ensureLive();
  }, []);

  // Persist the selection so it survives navigation / reloads.
  useEffect(() => {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify([...selected]));
    } catch {
      /* ignore quota errors */
    }
  }, [selected]);

  // Flush buffered lines into the view (unless paused). Snapshot-then-clear so
  // the state updater never reads the mutable ref after we reset it.
  useEffect(() => {
    const t = setInterval(() => {
      if (pausedRef.current || buf.current.length === 0) return;
      const pending = buf.current;
      buf.current = [];
      // Merge and keep chronological order so multiple sources interleave by
      // time rather than clumping per container (backlog arrives in bursts).
      setEntries((prev) => [...prev, ...pending].sort((a, b) => a.t - b.t).slice(-MAX_LINES));
    }, 250);
    return () => clearInterval(t);
  }, []);

  // Reconcile subscriptions incrementally: only (un)subscribe the containers
  // that actually changed, so re-selecting never re-streams existing sources.
  useEffect(() => {
    const runningIds = new Set(containers.map((c) => c.id));
    const byName = new Map(containers.map((c) => [c.id, c.name]));
    const desired = new Set([...selected].filter((id) => runningIds.has(id)));

    // Subscribe newly added sources.
    for (const id of desired) {
      if (subscribed.current.has(id)) continue;
      if (!colorOf.current.has(id)) {
        colorOf.current.set(id, sourcePalette[colorOf.current.size % sourcePalette.length]);
      }
      const color = colorOf.current.get(id)!;
      const name = byName.get(id) ?? id.slice(0, 12);
      subscribed.current.add(id);
      live.subscribeLogs(`glog:${id}`, id, "100", (f) => {
        if (f.type === "log") {
          const l = f.data as LogLine;
          buf.current.push({ containerId: id, source: name, color, stream: l.stream, level: detectLevel(l.message), timestamp: l.timestamp, t: parseTs(l.timestamp), message: l.message });
        }
      });
    }
    // Unsubscribe removed sources and drop their lines.
    for (const id of [...subscribed.current]) {
      if (desired.has(id)) continue;
      live.unsubscribe(`glog:${id}`);
      subscribed.current.delete(id);
      buf.current = buf.current.filter((e) => e.containerId !== id);
      setEntries((prev) => prev.filter((e) => e.containerId !== id));
    }
  }, [selected, containers]);

  // Tear everything down on unmount.
  useEffect(() => {
    return () => {
      for (const id of subscribed.current) live.unsubscribe(`glog:${id}`);
      subscribed.current.clear();
    };
  }, []);

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

  // Build the message matcher: substring (case-insensitive) or, when regex mode
  // is on, a compiled RegExp. An invalid pattern matches nothing and flags an
  // error rather than throwing.
  const { match, regexError } = useMemo(() => {
    if (filter === "") return { match: () => true, regexError: "" };
    if (useRegex) {
      try {
        const re = new RegExp(filter, "i");
        return { match: (m: string) => re.test(m), regexError: "" };
      } catch (e) {
        return { match: () => false, regexError: e instanceof Error ? e.message : "invalid regex" };
      }
    }
    const f = filter.toLowerCase();
    return { match: (m: string) => m.toLowerCase().includes(f), regexError: "" };
  }, [filter, useRegex]);

  const filtered = useMemo(
    () => entries.filter((e) => activeLevels.has(e.level) && match(e.message)),
    [entries, activeLevels, match]
  );

  useEffect(() => {
    if (stick && !paused && boxRef.current) boxRef.current.scrollTop = boxRef.current.scrollHeight;
  }, [filtered, stick, paused]);

  return (
    <>
      <PageHeader title="Logs" />
      <div className="p-6 grid grid-cols-[220px_1fr] gap-4 h-[calc(100vh-4rem)] min-h-0">
        {/* Source picker */}
        <div className="card p-3 overflow-auto min-h-0">
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
        <div className="card flex flex-col min-w-0 min-h-0">
          <div className="flex items-center gap-3 p-3 border-b border-border flex-wrap">
            {/* live / pause indicator */}
            <button
              onClick={() => setPaused((v) => !v)}
              className={clsx("inline-flex items-center gap-1.5 text-xs px-2 py-1 rounded-md font-medium", paused ? "bg-panel2 text-muted" : "bg-ok/15 text-ok")}
              title={paused ? "Resume live tail" : "Pause live tail"}
            >
              {paused ? <Play className="h-3.5 w-3.5" /> : <Pause className="h-3.5 w-3.5" />}
              {paused ? "Paused" : <><span className="h-2 w-2 rounded-full bg-ok animate-pulse" /> Live</>}
            </button>
            <div className="relative flex-1 min-w-[160px]">
              <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted" />
              <input
                className={clsx("input pl-8 pr-12 py-1.5", regexError && "ring-2 ring-danger/60")}
                placeholder={useRegex ? "Search logs by regex…" : "Search logs…"}
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
              />
              <button
                onClick={() => setUseRegex((v) => !v)}
                title={useRegex ? "Regex mode on" : "Use regular expression"}
                className={clsx(
                  "absolute right-1.5 top-1 px-1.5 py-1 rounded text-[11px] font-mono font-semibold transition-colors",
                  useRegex ? "bg-accent/20 text-accent" : "text-muted hover:bg-panel2"
                )}
              >
                .*
              </button>
            </div>
            {LEVELS.map((lvl) => (
              <button
                key={lvl}
                onClick={() => toggleLevel(lvl)}
                className={clsx("text-xs px-2 py-1 rounded-md font-medium capitalize transition-colors", activeLevels.has(lvl) ? levelBadge[lvl] : "bg-panel2 text-muted/50")}
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
            className="flex-1 min-h-0 overflow-auto font-mono text-xs leading-relaxed p-3 space-y-0.5"
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
