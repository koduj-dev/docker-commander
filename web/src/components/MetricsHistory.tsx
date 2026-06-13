import { useEffect, useState } from "react";
import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import clsx from "clsx";
import { api } from "../lib/api";

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

// MetricsHistory shows persisted CPU% and MEM% over a selectable time range,
// served from the history store (Redis or in-memory). Complements the live
// charts, which only hold the last minute or so.
export function MetricsHistory({ containerId }: { containerId: string }) {
  const [range, setRange] = useState("1h");
  const [rows, setRows] = useState<Row[]>([]);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const [cpu, mem] = await Promise.all([
          api.metricsHistory(containerId, "cpu", range),
          api.metricsHistory(containerId, "mem", range),
        ]);
        if (cancelled) return;
        // Merge the two series by timestamp.
        const byT = new Map<number, Row>();
        for (const p of cpu.points) byT.set(p.t, { t: p.t, cpu: p.v });
        for (const p of mem.points) byT.set(p.t, { ...(byT.get(p.t) ?? { t: p.t }), mem: p.v });
        setRows([...byT.values()].sort((a, b) => a.t - b.t));
      } catch {
        if (!cancelled) setRows([]);
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
        <span className="text-xs uppercase tracking-wide text-muted">History</span>
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
      {rows.length === 0 ? (
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
      )}
    </div>
  );
}
