import { useState } from "react";
import { Link } from "react-router-dom";
import { ArrowDown, ArrowUp } from "lucide-react";
import clsx from "clsx";
import type { ResourceUsage } from "../lib/types";
import { bytes, coresLabel, cpuCores } from "../lib/format";
import { sortUsage, type UsageSortKey } from "../lib/resources";

const LIMIT = 10;

// SortHeader is a clickable column header; it shows the active direction.
export function SortHeader({
  label, k, sort, desc, onSort, className,
}: {
  label: string; k: UsageSortKey; sort: UsageSortKey; desc: boolean; onSort: (k: UsageSortKey) => void; className?: string;
}) {
  const on = sort === k;
  const Arrow = desc ? ArrowDown : ArrowUp;
  return (
    <th className={clsx("font-medium px-4 py-3", className)} aria-sort={on ? (desc ? "descending" : "ascending") : undefined}>
      <button type="button" className={clsx("inline-flex items-center gap-1 uppercase tracking-wide", on ? "text-text" : "hover:text-text")} onClick={() => onSort(k)}>
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
              <td className="px-4 py-2 font-medium">
                <Link to={`/containers/${c.id}`} className="hover:underline">{c.name}</Link>
              </td>
              <td className="px-4 py-2 text-right font-mono text-xs">
                {coresLabel(cpuCores(c.cpuPercent, cpus))} <span className="text-muted">cores · {c.cpuPercent.toFixed(1)} %</span>
              </td>
              <td className="px-4 py-2 text-right font-mono text-xs">
                {bytes(c.memBytes)} <span className="text-muted">· {c.memPercent.toFixed(1)} %</span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
