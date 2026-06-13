import { Area, AreaChart, ResponsiveContainer, Tooltip, YAxis } from "recharts";
import type { StatsSample } from "../lib/types";
import { bytes } from "../lib/format";

interface Props {
  data: StatsSample[];
}

// Two compact real-time area charts: CPU% and memory%. The data array is a
// rolling window maintained by the parent from the live WebSocket stream.
export function StatsCharts({ data }: Props) {
  const latest = data[data.length - 1];
  return (
    <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
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
    </div>
  );
}

function Metric({
  title,
  value,
  sub,
  data,
  dataKey,
  color,
  domainMax,
}: {
  title: string;
  value: string;
  sub?: string;
  data: StatsSample[];
  dataKey: keyof StatsSample;
  color: string;
  domainMax: number;
}) {
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
            </defs>
            <YAxis domain={[0, domainMax]} hide />
            <Tooltip
              contentStyle={{ background: "#1a2233", border: "1px solid #243047", borderRadius: 8, fontSize: 12 }}
              labelFormatter={() => ""}
              formatter={(v) => [`${Number(v).toFixed(1)} %`, title]}
            />
            <Area
              type="monotone"
              dataKey={dataKey as string}
              stroke={color}
              strokeWidth={2}
              fill={`url(#g-${title})`}
              isAnimationActive={false}
              dot={false}
            />
          </AreaChart>
        </ResponsiveContainer>
      </div>
    </div>
  );
}
