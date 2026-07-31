import { Area, AreaChart, ResponsiveContainer, Tooltip, YAxis } from "recharts";
import type { StatsSample } from "../lib/types";
import { bytes, netRates, rate } from "../lib/format";

interface Props {
  data: StatsSample[];
}

// Compact real-time area charts: CPU%, memory% and network throughput. The data
// array is a rolling window maintained by the parent from the live WebSocket
// stream.
//
// Network is the odd one out: Docker reports cumulative byte counters, so the
// chart plots the DERIVED rate between consecutive samples rather than the raw
// value — a chart of a monotonically rising counter tells you nothing useful.
export function StatsCharts({ data }: Props) {
  const latest = data[data.length - 1];
  const rates = netRates(data);
  const lastRate = rates[rates.length - 1];
  return (
    <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
      <Metric
        title="CPU"
        value={latest ? `${latest.cpuPercent.toFixed(1)} %` : "—"}
        data={data}
        dataKey="cpuPercent"
        color="#2496ed"
        domainMax={100}
      />
      <Metric
        title="Memory"
        value={latest ? `${bytes(latest.memUsage)} / ${bytes(latest.memLimit)}` : "—"}
        sub={latest ? `${latest.memPercent.toFixed(1)} %` : undefined}
        data={data}
        dataKey="memPercent"
        color="#2dd4a7"
        domainMax={100}
      />
      <Metric
        title="Network"
        value={lastRate ? `↓ ${rate(lastRate.rx)}` : "—"}
        sub={lastRate ? `↑ ${rate(lastRate.tx)}` : undefined}
        data={rates}
        dataKey="rx"
        secondKey="tx"
        color="#a78bfa"
        secondColor="#f5b14c"
        formatValue={rate}
      />
    </div>
  );
}

function Metric({
  title,
  value,
  sub,
  data,
  dataKey,
  secondKey,
  color,
  secondColor,
  domainMax,
  formatValue,
}: {
  title: string;
  value: string;
  sub?: string;
  // Rows are whatever the caller plots — raw samples for CPU/memory, derived
  // rates for network — so the keys are checked by the caller, not here.
  data: object[];
  dataKey: string;
  /** Optional second series on the same axes — used to draw TX beside RX. */
  secondKey?: string;
  color: string;
  secondColor?: string;
  /** Fixed axis ceiling for percentages; omit to let the data set the scale. */
  domainMax?: number;
  formatValue?: (v: number) => string;
}) {
  const fmt = formatValue ?? ((v: number) => `${v.toFixed(1)} %`);
  return (
    <div className="card p-4">
      <div className="flex items-baseline justify-between mb-2">
        <span className="text-xs uppercase tracking-wide text-muted">{title}</span>
        <span className="text-sm font-semibold">
          {value} {sub && <span className="text-muted font-normal">· {sub}</span>}
        </span>
      </div>
      <div className="h-28">
        <ResponsiveContainer width="100%" height="100%">
          <AreaChart data={data} margin={{ top: 4, right: 0, bottom: 0, left: 0 }}>
            <defs>
              <linearGradient id={`g-${title}`} x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor={color} stopOpacity={0.35} />
                <stop offset="100%" stopColor={color} stopOpacity={0} />
              </linearGradient>
              {secondColor && (
                <linearGradient id={`g2-${title}`} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor={secondColor} stopOpacity={0.3} />
                  <stop offset="100%" stopColor={secondColor} stopOpacity={0} />
                </linearGradient>
              )}
            </defs>
            {/* Percentages get a fixed 0–100 axis so the shape means something;
                throughput has no ceiling, so it scales to what happened. */}
            <YAxis domain={domainMax !== undefined ? [0, domainMax] : [0, "auto"]} hide />
            <Tooltip
              contentStyle={{ background: "#1a2233", border: "1px solid #243047", borderRadius: 8, fontSize: 12 }}
              labelFormatter={() => ""}
              formatter={(v, name) => {
                const x = Number(v);
                return [Number.isFinite(x) ? fmt(x) : "—", name === secondKey ? "TX" : name === dataKey && secondKey ? "RX" : title];
              }}
            />
            <Area
              type="monotone"
              dataKey={dataKey}
              stroke={color}
              strokeWidth={2}
              fill={`url(#g-${title})`}
              isAnimationActive={false}
              dot={false}
            />
            {secondKey && (
              <Area
                type="monotone"
                dataKey={secondKey}
                stroke={secondColor ?? color}
                strokeWidth={2}
                fill={secondColor ? `url(#g2-${title})` : "none"}
                isAnimationActive={false}
                dot={false}
              />
            )}
          </AreaChart>
        </ResponsiveContainer>
      </div>
    </div>
  );
}
