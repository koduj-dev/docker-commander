import { useEffect, useState } from "react";
import { PieChart, Pie, Cell, Tooltip, ResponsiveContainer, Legend } from "recharts";
import { api } from "../lib/api";
import type { ResourceOverview, ResourceUsage } from "../lib/types";
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
export function ResourceBreakdown() {
  const [data, setData] = useState<ResourceOverview | null>(null);
  const [error, setError] = useState("");

  // Poll so the breakdown follows containers starting/stopping. Refreshes
  // update in place (no flicker); a transient error keeps the last good data.
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
  }, []);

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
    body = (
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <UsagePie title={`CPU · ${data.cpus} core${data.cpus === 1 ? "" : "s"}`} slices={build(containers, (c) => c.cpuPercent)} />
        <UsagePie title="Memory" slices={build(containers, (c) => c.memPercent)} />
      </div>
    );
  }

  return (
    <div>
      <h2 className="text-sm font-semibold text-muted mb-3">Resource usage · share of host</h2>
      {body}
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
              formatter={(v: number, n: string) => [`${v.toFixed(1)} %`, n]}
            />
            <Legend wrapperStyle={{ fontSize: 11 }} iconSize={8} />
          </PieChart>
        </ResponsiveContainer>
      </div>
    </div>
  );
}
