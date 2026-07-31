import { useEffect, useState } from "react";
import { PieChart, Pie, Cell, Tooltip, ResponsiveContainer, Legend } from "recharts";
import { api } from "../lib/api";
import type { ResourceOverview, ResourceUsage } from "../lib/types";
import { rate } from "../lib/format";
import { Spinner } from "./ui";

type Slice = { name: string; value: number };

// Palette for container slices; "Free"/"Other" get fixed muted colours.
const PALETTE = ["#2496ed", "#2dd4a7", "#f59e0b", "#a78bfa", "#f472b6", "#34d399", "#60a5fa", "#fb7185"];
const FREE_COLOR = "#243047";
const OTHER_COLOR = "#64748b";
const TOP = 6;

function build(containers: ResourceUsage[], valueOf: (c: ResourceUsage) => number): Slice[] {
  const items = containers
    .map((c) => ({ name: c.name, value: Math.max(0, valueOf(c)) }))
    .sort((a, b) => b.value - a.value);
  const slices = items.slice(0, TOP);
  const restSum = items.slice(TOP).reduce((s, x) => s + x.value, 0);
  if (restSum > 0.05) slices.push({ name: "Other", value: restSum });
  const used = items.reduce((s, x) => s + x.value, 0);
  slices.push({ name: "Free", value: Math.max(0, 100 - used) });
  return slices;
}

function colorFor(name: string, i: number): string {
  if (name === "Free") return FREE_COLOR;
  if (name === "Other") return OTHER_COLOR;
  return PALETTE[i % PALETTE.length];
}

// ResourceBreakdown shows how the running containers divide up the host's CPU
// and memory as two pie charts. It's a snapshot taken on load (sampling every
// container is not free, so it doesn't auto-poll).
export function ResourceBreakdown({ tick = 0 }: { tick?: number }) {
  const [data, setData] = useState<ResourceOverview | null>(null);
  const [error, setError] = useState("");

  // Refresh on Docker lifecycle events (`tick`) plus a slow poll for CPU/mem
  // drift. Refreshes update in place (no flicker); a transient error keeps the
  // last good data.
  useEffect(() => {
    const load = () =>
      api
        .statsOverview()
        .then((d) => {
          setData(d);
          setError("");
        })
        .catch((e) => setError(e instanceof Error ? e.message : "could not sample container resources"));
    load();
    const t = setInterval(load, 8000);
    return () => clearInterval(t);
  }, [tick]);

  const containers = data?.containers ?? []; // Go sends null, not [], when empty

  // The section always reserves the chart height so it doesn't jump when the
  // data arrives; errors/empty render in the same space instead of the pies.
  let body: React.ReactNode;
  if (error && !data) {
    body = <div className="card p-4 text-sm text-danger">Couldn't sample container resources: {error}</div>;
  } else if (!data) {
    body = (
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <PiePlaceholder loading />
        <PiePlaceholder loading />
      </div>
    );
  } else if (containers.length === 0) {
    body = <div className="card p-4 text-sm text-muted">No running containers to sample.</div>;
  } else {
    // Network is NOT drawn as a pie. A pie claims "parts of a whole", and the only
    // whole available here is the sum of what happens to be moving right now — so
    // a container at 100% of 2 KB/s would look identical to one at 100% of
    // 800 MB/s. Throughput is a magnitude, so it gets bars with the real numbers
    // on them.
    const netTotal = containers.reduce((sum, c) => sum + c.netRxRate + c.netTxRate, 0);
    body = (
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <UsagePie title={`CPU · ${data.cpus} core${data.cpus === 1 ? "" : "s"}`} slices={build(containers, (c) => c.cpuPercent)} />
        <UsagePie title="Memory" slices={build(containers, (c) => c.memPercent)} />
        <TopTalkers containers={containers} total={netTotal} />
      </div>
    );
  }

  return (
    <div>
      <h2 className="text-sm font-semibold text-muted mb-3">
        Resource usage <span className="font-normal">· CPU and memory as a share of the host; network as current throughput</span>
      </h2>
      {body}
    </div>
  );
}

// TopTalkers ranks containers by current throughput.
//
// Bars are scaled to the BUSIEST container, not to the total: the question is
// "who is moving the most and how much", and the absolute rate is on every row so
// the shape never has to carry the magnitude on its own. Single series, so no
// legend — the title names it.
function TopTalkers({ containers, total }: { containers: ResourceUsage[]; total: number }) {
  const rows = containers
    .map((c) => ({ name: c.name, value: c.netRxRate + c.netTxRate, rx: c.netRxRate, tx: c.netTxRate }))
    .filter((r) => r.value > 0)
    .sort((a, b) => b.value - a.value)
    .slice(0, 6);
  const max = rows[0]?.value ?? 0;

  return (
    <div className="card p-4">
      <div className="text-xs uppercase tracking-wide text-muted mb-2">
        Network{total > 0 ? ` · ${rate(total)}` : ""}
      </div>
      {rows.length === 0 ? (
        <div className="h-56 grid place-items-center text-sm text-muted">No traffic right now</div>
      ) : (
        <div className="h-56 flex flex-col justify-center gap-2.5">
          {rows.map((r) => (
            <div key={r.name}>
              <div className="flex items-baseline justify-between gap-2 text-xs mb-1">
                <span className="truncate font-mono">{r.name}</span>
                <span className="text-muted shrink-0" title={`↓ ${rate(r.rx)} · ↑ ${rate(r.tx)}`}>
                  {rate(r.value)}
                </span>
              </div>
              <div className="h-1.5 rounded-full bg-panel2 overflow-hidden">
                <div
                  className="h-full rounded-full bg-accent"
                  style={{ width: `${max > 0 ? Math.max(2, (r.value / max) * 100) : 0}%` }}
                />
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// PiePlaceholder reserves the same footprint as a UsagePie while loading.
function PiePlaceholder({ loading }: { loading?: boolean }) {
  return (
    <div className="card p-4">
      <div className="text-xs uppercase tracking-wide text-muted mb-2">&nbsp;</div>
      <div className="h-56 grid place-items-center text-muted">{loading && <Spinner />}</div>
    </div>
  );
}

function UsagePie({ title, slices }: { title: string; slices: Slice[] }) {
  return (
    <div className="card p-4">
      <div className="text-xs uppercase tracking-wide text-muted mb-2">{title}</div>
      <div className="h-56">
        <ResponsiveContainer width="100%" height="100%">
          <PieChart>
            <Pie data={slices} dataKey="value" nameKey="name" innerRadius={45} outerRadius={75} paddingAngle={1} isAnimationActive={false}>
              {slices.map((s, i) => (
                <Cell key={s.name} fill={colorFor(s.name, i)} stroke="#0f1623" strokeWidth={1} />
              ))}
            </Pie>
            <Tooltip
              contentStyle={{ background: "#1a2233", border: "1px solid #243047", borderRadius: 8, fontSize: 12 }}
              itemStyle={{ color: "#e5e9f0" }}
              labelStyle={{ color: "#e5e9f0" }}
              formatter={(v, n) => { const x = Number(v); return [Number.isFinite(x) ? `${x.toFixed(1)} %` : "—", String(n)]; }}
            />
            <Legend wrapperStyle={{ fontSize: 11 }} iconSize={8} />
          </PieChart>
        </ResponsiveContainer>
      </div>
    </div>
  );
}
