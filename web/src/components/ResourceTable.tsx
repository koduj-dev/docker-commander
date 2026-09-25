import { useState } from "react";
import { Link } from "react-router-dom";
import { ArrowDown, ArrowUp } from "lucide-react";
import clsx from "clsx";
import type { ResourceUsage } from "../lib/types";
import { bytes, coresLabel, cpuCores } from "../lib/format";
import { sortUsage, type UsageSortKey } from "../lib/resources";

const LIMIT = 10;

// SortHeader is a clickable column header. The active column is highlighted
// (accent colour + arrow) and exposes its direction via aria-sort.
export function SortHeader<K extends string>({
  label, k, sort, desc, onSort, className, title,
}: {
  label: string; k: K; sort: K; desc: boolean; onSort: (k: K) => void; className?: string; title?: string;
}) {
  const on = sort === k;
  const Arrow = desc ? ArrowDown : ArrowUp;
  return (
    <th className={clsx("font-medium px-4 py-3", className)} aria-sort={on ? (desc ? "descending" : "ascending") : undefined}>
      <button
        type="button"
        title={title}
        className={clsx("inline-flex items-center gap-1 uppercase tracking-wide whitespace-nowrap", on ? "text-accent font-semibold" : "hover:text-text")}
        onClick={() => onSort(k)}
      >
        {label}
        {on && <Arrow className="h-3 w-3" />}
      </button>
    </th>
  );
}

// ResourceTable is the dashboard's numeric companion to the CPU/memory donuts:
// the donut says who takes what SHARE, this says how much that actually is.
// Only the top LIMIT rows (the card is as tall as its rows, not a fixed box);
// the full, filterable list lives on the Resources page.
export function ResourceTable({ containers, cpus }: { containers: ResourceUsage[]; cpus: number }) {
  const [sort, setSort] = useState<UsageSortKey>("mem");
  const [desc, setDesc] = useState(true);
  const onSort = (k: UsageSortKey) => {
    if (k === sort) setDesc((d) => !d);
    else { setSort(k); setDesc(true); }
  };
  const rows = sortUsage(containers, sort, desc).slice(0, LIMIT);
  return (
    <div className="card overflow-hidden">
      <div className="flex items-baseline justify-between px-4 pt-4">
        <div className="text-xs uppercase tracking-wide text-muted">
          Top consumers{containers.length > LIMIT ? ` · ${LIMIT} of ${containers.length}` : ""}
        </div>
        <Link to="/resources" className="text-xs text-accent hover:underline">View all →</Link>
      </div>
      <table className="w-full text-sm mt-2">
        <thead className="text-muted text-xs">
          <tr className="border-b border-border">
            <th className="text-left font-medium px-4 py-3 uppercase tracking-wide">Container</th>
            <SortHeader label="CPU" k="cpu" sort={sort} desc={desc} onSort={onSort} className="text-right" />
            <SortHeader label="Memory" k="mem" sort={sort} desc={desc} onSort={onSort} className="text-right" />
          </tr>
        </thead>
        <tbody>
          {rows.map((c) => (
            <tr key={c.id} className="border-b border-border/50 last:border-0">
              {/* w-full + max-w-0 lets a long name truncate instead of pushing the numbers off a half-width card. */}
              <td className="px-4 py-2 font-medium w-full max-w-0 truncate">
                <Link to={`/containers/${c.id}`} className="hover:underline" title={c.name}>{c.name}</Link>
              </td>
              {/* The percentages are the first thing to go when the card is narrow. */}
              <td className="px-4 py-2 text-right font-mono text-xs whitespace-nowrap">
                {coresLabel(cpuCores(c.cpuPercent, cpus))} <span className="text-muted">cores<span className="hidden 2xl:inline"> · {c.cpuPercent.toFixed(1)} %</span></span>
              </td>
              <td className="px-4 py-2 text-right font-mono text-xs whitespace-nowrap">
                {bytes(c.memBytes)} <span className="text-muted hidden 2xl:inline">· {c.memPercent.toFixed(1)} %</span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
