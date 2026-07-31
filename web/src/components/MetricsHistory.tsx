import { useEffect, useState } from "react";
import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import clsx from "clsx";
import { api } from "../lib/api";
import { bytes, netRates, rate } from "../lib/format";

const RANGES = [
  { label: "15m", value: "15m" },
  { label: "1h", value: "1h" },
  { label: "6h", value: "6h" },
];

interface Row {
  t: number;
  cpu?: number;
  mem?: number;
}

interface NetRow {
  t: number;
  rx: number;
  tx: number;
}

type View = "usage" | "network";

// MetricsHistory shows persisted CPU% and MEM% over a selectable time range,
// served from the history store (Redis or in-memory). Complements the live
// charts, which only hold the last minute or so.
export function MetricsHistory({ containerId }: { containerId: string }) {
  const [range, setRange] = useState("1h");
  const [view, setView] = useState<View>("usage");
  const [rows, setRows] = useState<Row[]>([]);
  const [netRows, setNetRows] = useState<NetRow[]>([]);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const [cpu, mem, nrx, ntx] = await Promise.all([
          api.metricsHistory(containerId, "cpu", range),
          api.metricsHistory(containerId, "mem", range),
          api.metricsHistory(containerId, "netrx", range),
          api.metricsHistory(containerId, "nettx", range),
        ]);
        if (cancelled) return;
        // Merge the two percentage series by timestamp.
        const byT = new Map<number, Row>();
        for (const p of cpu.points) byT.set(p.t, { t: p.t, cpu: p.v });
        for (const p of mem.points) byT.set(p.t, { ...(byT.get(p.t) ?? { t: p.t }), mem: p.v });
        setRows([...byT.values()].sort((a, b) => a.t - b.t));

        // Network is stored as CUMULATIVE counters, so the chart plots the
        // derived rate. Deliberately the same helper the live chart uses, so a
        // counter reset and an uneven interval are handled identically in both
        // places rather than by two rules that can drift apart.
        const rxByT = new Map<number, number>();
        for (const p of nrx.points) rxByT.set(p.t, p.v);
        const counters: { timestamp: number; netRx: number; netTx: number }[] = [];
        for (const p of ntx.points) {
          const rx = rxByT.get(p.t);
          if (rx === undefined) continue; // only points where both were recorded
          counters.push({ timestamp: p.t, netRx: rx, netTx: p.v });
        }
        counters.sort((a, b) => a.timestamp - b.timestamp);
        setNetRows(netRates(counters).map((r) => ({ t: r.t, rx: r.rx, tx: r.tx })));
      } catch {
        if (!cancelled) {
          setRows([]);
          setNetRows([]);
        }
      }
    };
    void load();
    const id = setInterval(load, 15000);
    return () => { cancelled = true; clearInterval(id); };
  }, [containerId, range]);

  const fmtTime = (t: number) => {
    const d = new Date(t);
    return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
  };

  return (
    <div className="card p-4">
      <div className="flex items-center justify-between mb-3">
        <div className="flex gap-1">
          {(["usage", "network"] as View[]).map((v) => (
            <button
              key={v}
              onClick={() => setView(v)}
              className={clsx(
                "text-xs px-2 py-1 rounded-md font-medium capitalize",
                view === v ? "bg-accent/15 text-accent" : "bg-panel2 text-muted",
              )}
            >
              {v === "usage" ? "CPU & memory" : "Network"}
            </button>
          ))}
        </div>
        <div className="flex gap-1">
          {RANGES.map((r) => (
            <button
              key={r.value}
              onClick={() => setRange(r.value)}
              className={clsx("text-xs px-2 py-1 rounded-md font-medium", range === r.value ? "bg-accent/15 text-accent" : "bg-panel2 text-muted")}
            >
              {r.label}
            </button>
          ))}
        </div>
      </div>
      {view === "usage" ? (
        rows.length === 0 ? (
          <div className="h-40 grid place-items-center text-sm text-muted">No history yet — samples are collected every few seconds.</div>
        ) : (
          <div className="h-48">
            <ResponsiveContainer width="100%" height="100%">
              <LineChart data={rows} margin={{ top: 4, right: 8, bottom: 0, left: -16 }}>
                <CartesianGrid stroke="#1a2233" vertical={false} />
                <XAxis dataKey="t" tickFormatter={fmtTime} stroke="#8b97ad" fontSize={10} minTickGap={40} />
                <YAxis domain={[0, 100]} stroke="#8b97ad" fontSize={10} unit="%" />
                <Tooltip
                  contentStyle={{ background: "#1a2233", border: "1px solid #243047", borderRadius: 8, fontSize: 12 }}
                  labelFormatter={(t) => new Date(t as number).toLocaleTimeString()}
                  formatter={(v, n) => { const x = Number(v); return [Number.isFinite(x) ? `${x.toFixed(1)} %` : "—", n === "cpu" ? "CPU" : "Memory"]; }}
                />
                <Line type="monotone" dataKey="cpu" stroke="#2496ed" strokeWidth={2} dot={false} isAnimationActive={false} />
                <Line type="monotone" dataKey="mem" stroke="#2dd4a7" strokeWidth={2} dot={false} isAnimationActive={false} />
              </LineChart>
            </ResponsiveContainer>
          </div>
        )
      ) : netRows.length === 0 ? (
        <div className="h-40 grid place-items-center text-sm text-muted text-center px-4">
          No network history yet — a rate needs at least two samples, and counters are only stored while the container
          is running.
        </div>
      ) : (
        <div className="h-48">
          <ResponsiveContainer width="100%" height="100%">
            <LineChart data={netRows} margin={{ top: 4, right: 8, bottom: 0, left: 4 }}>
              <CartesianGrid stroke="#1a2233" vertical={false} />
              <XAxis dataKey="t" tickFormatter={fmtTime} stroke="#8b97ad" fontSize={10} minTickGap={40} />
              {/* Throughput has no ceiling, so the axis follows the data instead
                  of the fixed 0–100 the percentage view uses. */}
              <YAxis stroke="#8b97ad" fontSize={10} width={64} tickFormatter={(v) => bytes(Number(v))} />
              <Tooltip
                contentStyle={{ background: "#1a2233", border: "1px solid #243047", borderRadius: 8, fontSize: 12 }}
                labelFormatter={(t) => new Date(t as number).toLocaleTimeString()}
                formatter={(v, n) => { const x = Number(v); return [Number.isFinite(x) ? rate(x) : "—", n === "rx" ? "Received" : "Sent"]; }}
              />
              <Line type="monotone" dataKey="rx" stroke="#a78bfa" strokeWidth={2} dot={false} isAnimationActive={false} />
              <Line type="monotone" dataKey="tx" stroke="#f5b14c" strokeWidth={2} dot={false} isAnimationActive={false} />
            </LineChart>
          </ResponsiveContainer>
        </div>
      )}
    </div>
  );
}
