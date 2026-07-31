import { useEffect, useState } from "react";
import { Area, AreaChart, Cell, Legend, Pie, PieChart, ResponsiveContainer, Tooltip, YAxis } from "recharts";
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
  // A short rolling window of host-wide throughput, so the dashboard can show a
  // trend rather than a single flickering number. Throughput is bursty: a
  // point-in-time ranking of containers reorders itself on every poll and is
  // unreadable, which is why the detail page carries the per-container series and
  // this is only a summary.
  const [netWindow, setNetWindow] = useState<{ t: number; rx: number; tx: number }[]>([]);

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
          const rx = (d.containers ?? []).reduce((n, c) => n + c.netRxRate, 0);
          const tx = (d.containers ?? []).reduce((n, c) => n + c.netTxRate, 0);
          setNetWindow((w) => [...w, { t: Date.now(), rx, tx }].slice(-30));
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
    // Network gets neither a pie nor a ranking here. A pie claims "parts of a
    // whole" and the only whole available is whatever happens to be moving, so a
    // container at 100% of 2 KB/s would look identical to one at 100% of 800 MB/s.
    // A live ranking is no better: throughput is bursty, so the order changes on
    // every poll. It is a time series — which is how Portainer and the standard
    // cAdvisor/Grafana panels present it — so the per-container series lives on
    // the container detail and the dashboard shows the host-wide summary.
    const netRx = containers.reduce((sum, c) => sum + c.netRxRate, 0);
    const netTx = containers.reduce((sum, c) => sum + c.netTxRate, 0);
    body = (
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <UsagePie title={`CPU · ${data.cpus} core${data.cpus === 1 ? "" : "s"}`} slices={build(containers, (c) => c.cpuPercent)} />
        <UsagePie title="Memory" slices={build(containers, (c) => c.memPercent)} />
        <NetworkSummary window={netWindow} rx={netRx} tx={netTx} />
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

// NetworkSummary is the dashboard's whole network story: what the host is moving
// right now, and how that has been trending over the last few minutes.
//
// Summed across the running containers, so container-to-container traffic counts
// twice — once as one side's TX and once as the other's RX. Said out loud rather
// than left for someone to discover when the numbers don't add up against an
// external measurement.
function NetworkSummary({ window: w, rx, tx }: { window: { t: number; rx: number; tx: number }[]; rx: number; tx: number }) {
  const max = Math.max(1, ...w.map((p) => Math.max(p.rx, p.tx)));
  return (
    <div className="card p-4">
      <div className="text-xs uppercase tracking-wide text-muted mb-2">Network · all containers</div>
      <div className="h-56 flex flex-col">
        <div className="flex gap-6 mb-3">
          <div>
            <div className="text-xs text-muted">Received</div>
            <div className="text-lg font-semibold">{rate(rx)}</div>
          </div>
          <div>
            <div className="text-xs text-muted">Sent</div>
            <div className="text-lg font-semibold">{rate(tx)}</div>
          </div>
        </div>
        {w.length < 2 ? (
          <div className="flex-1 grid place-items-center text-sm text-muted">
            Collecting — a rate needs two samples.
          </div>
        ) : (
          <div className="flex-1">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={w} margin={{ top: 4, right: 0, bottom: 0, left: 0 }}>
                <defs>
                  <linearGradient id="g-net-rx" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="0%" stopColor="#a78bfa" stopOpacity={0.35} />
                    <stop offset="100%" stopColor="#a78bfa" stopOpacity={0} />
                  </linearGradient>
                </defs>
                <YAxis domain={[0, max]} hide />
                <Tooltip
                  contentStyle={{ background: "#1a2233", border: "1px solid #243047", borderRadius: 8, fontSize: 12 }}
                  labelFormatter={(t) => new Date(t as number).toLocaleTimeString()}
                  formatter={(v, n) => [rate(Number(v)), n === "rx" ? "Received" : "Sent"]}
                />
                <Area type="monotone" dataKey="rx" stroke="#a78bfa" strokeWidth={2} fill="url(#g-net-rx)" dot={false} isAnimationActive={false} />
                <Area type="monotone" dataKey="tx" stroke="#f5b14c" strokeWidth={2} fill="none" dot={false} isAnimationActive={false} />
              </AreaChart>
            </ResponsiveContainer>
          </div>
        )}
        <p className="text-xs text-muted mt-1">
          Summed across containers, so traffic between two of them counts twice. Per-container history is on the
          container page.
        </p>
      </div>
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
