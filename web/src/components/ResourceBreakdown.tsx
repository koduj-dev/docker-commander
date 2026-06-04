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
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    api.statsOverview().then(setData).catch(() => setFailed(true));
  }, []);

  if (failed) return null; // host unreachable / no permission — just hide the section
  if (!data) {
    return (
      <div className="flex items-center gap-2 text-muted text-sm">
        <Spinner /> Sampling container resources…
      </div>
    );
  }
  const containers = data.containers ?? []; // Go sends null, not [], when empty
  if (containers.length === 0) return null; // nothing running

  return (
    <div>
      <h2 className="text-sm font-semibold text-muted mb-3">Resource usage · share of host</h2>
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <UsagePie title={`CPU · ${data.cpus} core${data.cpus === 1 ? "" : "s"}`} slices={build(containers, (c) => c.cpuPercent)} />
        <UsagePie title="Memory" slices={build(containers, (c) => c.memPercent)} />
      </div>
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
