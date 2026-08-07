import { useEffect, useMemo, useRef, useState } from "react";
import { Pause, Play, Search, X } from "lucide-react";
import clsx from "clsx";
import type { EventMsg } from "../lib/types";
import { hostParam } from "../lib/host";
import { PageHeader } from "../layout/Shell";

const MAX_EVENTS = 2000;

// Color-code event object types so the stream is scannable at a glance.
const typeColor: Record<string, string> = {
  container: "text-accent",
  image: "text-ok",
  network: "text-warn",
  volume: "text-purple-400",
  daemon: "text-muted",
};

// Actions that usually signal trouble, highlighted in the feed.
const dangerActions = new Set(["die", "kill", "oom", "destroy", "delete", "remove"]);

interface Row extends EventMsg {
  seq: number;
}

export function Events() {
  const [events, setEvents] = useState<Row[]>([]);
  const [paused, setPaused] = useState(false);
  const [connected, setConnected] = useState(false);
  const [filter, setFilter] = useState("");
  const buf = useRef<Row[]>([]);
  const seq = useRef(0);
  const pausedRef = useRef(false);
  pausedRef.current = paused;
  const boxRef = useRef<HTMLDivElement>(null);

  // The feed reconnects, and says so when it is not connected.
  //
  // Neither used to be true: a dropped socket — a server restart, a proxy idle
  // timeout, a laptop waking up — left the page showing a pulsing green "Live"
  // badge above a list that would never move again. A feed that lies about being
  // live is worse than one that is visibly broken, because the absence of events
  // reads as "nothing is happening".
  useEffect(() => {
    let ws: WebSocket | null = null;
    let retry: number | null = null;
    let closedByUnmount = false;

    const open = () => {
      const proto = location.protocol === "https:" ? "wss" : "ws";
      ws = new WebSocket(`${proto}://${location.host}/api/events${hostParam("?")}`);
      ws.onopen = () => setConnected(true);
      ws.onmessage = (ev) => {
        try {
          const e = JSON.parse(ev.data as string) as EventMsg;
          if ((e as { error?: string }).error) return;
          buf.current.push({ ...e, seq: seq.current++ });
        } catch {
          /* ignore malformed frame */
        }
      };
      ws.onclose = () => {
        setConnected(false);
        if (closedByUnmount || retry != null) return;
        // The same 1.5s the shared stats/logs socket uses (lib/ws.ts), so the two
        // behave alike after a server restart.
        retry = window.setTimeout(() => {
          retry = null;
          open();
        }, 1500);
      };
      ws.onerror = () => ws?.close();
    };
    open();

    return () => {
      closedByUnmount = true;
      if (retry != null) window.clearTimeout(retry);
      ws?.close();
    };
  }, []);

  // Flush buffered events into the view unless paused (snapshot-then-clear).
  useEffect(() => {
    const t = setInterval(() => {
      if (pausedRef.current || buf.current.length === 0) return;
      const pending = buf.current;
      buf.current = [];
      setEvents((prev) => [...prev, ...pending].slice(-MAX_EVENTS));
    }, 300);
    return () => clearInterval(t);
  }, []);

  const filtered = useMemo(() => {
    const f = filter.toLowerCase();
    if (!f) return events;
    return events.filter((e) => `${e.type} ${e.action} ${e.name} ${e.id}`.toLowerCase().includes(f));
  }, [events, filter]);

  useEffect(() => {
    if (!paused && boxRef.current) boxRef.current.scrollTop = boxRef.current.scrollHeight;
  }, [filtered, paused]);

  return (
    <>
      <PageHeader title="Events" />
      <div className="p-6 flex flex-col h-[calc(100vh-4rem)] min-h-0">
        <div className="card flex flex-col flex-1 min-h-0">
          <div className="flex items-center gap-3 p-3 border-b border-border">
            <button
              onClick={() => setPaused((v) => !v)}
              className={clsx("inline-flex items-center gap-1.5 text-xs px-2 py-1 rounded-md font-medium",
                paused ? "bg-panel2 text-muted" : connected ? "bg-ok/15 text-ok" : "bg-warn/15 text-warn")}
            >
              {paused ? <Play className="h-3.5 w-3.5" /> : <Pause className="h-3.5 w-3.5" />}
              {/* The badge reports the CONNECTION, not just the pause toggle. It
                  used to say "Live" over a dead socket. */}
              {paused
                ? "Paused"
                : connected
                  ? <><span className="h-2 w-2 rounded-full bg-ok animate-pulse" /> Live</>
                  : "Reconnecting…"}
            </button>
            <div className="relative flex-1">
              <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted" />
              <input className="input pl-8 py-1.5" placeholder="Filter by type, action, name…" value={filter} onChange={(e) => setFilter(e.target.value)} />
            </div>
            <span className="text-xs text-muted">{filtered.length}</span>
            <button className="btn-ghost px-2 py-1.5" title="Clear" onClick={() => { setEvents([]); buf.current = []; }}><X className="h-4 w-4" /></button>
          </div>
          <div ref={boxRef} className="flex-1 min-h-0 overflow-auto font-mono text-xs p-3 space-y-0.5">
            {filtered.length === 0 ? (
              <div className="text-muted">Waiting for Docker events…</div>
            ) : (
              filtered.map((e) => {
                // Exec events pack the whole command into the action string
                // ("exec_start: python -c …"). Split it: the verb gets its own
                // column, the command spans the rest of the width on one line
                // (white-space collapses its newlines). Full text on hover.
                const colon = e.action.indexOf(":");
                const verb = colon >= 0 ? e.action.slice(0, colon) : e.action;
                const detail = colon >= 0 ? e.action.slice(colon + 1).trim() : "";
                return (
                <div key={e.seq} className="flex gap-3 items-baseline whitespace-nowrap">
                  <span className="text-muted/60 shrink-0 w-24">{e.time ? new Date(e.time * 1000).toLocaleTimeString() : ""}</span>
                  <span className={clsx("shrink-0 w-20 truncate", typeColor[e.type] ?? "text-text")}>{e.type}</span>
                  <span className={clsx("shrink-0 w-28 truncate font-medium", dangerActions.has(verb) && "text-danger")}>{verb}</span>
                  <span className="shrink-0 w-48 truncate text-text/90" title={e.name || e.id}>{e.name || (e.id ? e.id.slice(0, 12) : "—")}</span>
                  {detail && <span className="flex-1 min-w-0 truncate text-muted" title={detail}>{detail}</span>}
                </div>
                );
              })
            )}
          </div>
        </div>
      </div>
    </>
  );
}
