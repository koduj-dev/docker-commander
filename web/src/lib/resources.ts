import type { ResourceUsage } from "./types";

export type UsageSortKey = "name" | "cpu" | "mem" | "rx" | "tx";

const value: Record<Exclude<UsageSortKey, "name">, (c: ResourceUsage) => number> = {
  cpu: (c) => c.cpuPercent,
  mem: (c) => c.memBytes,
  rx: (c) => c.netRxRate,
  tx: (c) => c.netTxRate,
};

// sortUsage returns a sorted COPY. Ties fall back to name so the order is
// stable between refreshes — a table that reshuffles equal rows every 5s is
// unreadable.
export function sortUsage(rows: ResourceUsage[], key: UsageSortKey, desc: boolean): ResourceUsage[] {
  const dir = desc ? -1 : 1;
  return [...rows].sort((a, b) => {
    const d = key === "name" ? a.name.localeCompare(b.name) : value[key](a) - value[key](b);
    return d !== 0 ? d * dir : a.name.localeCompare(b.name);
  });
}

export function filterUsage(rows: ResourceUsage[], query: string): ResourceUsage[] {
  const q = query.trim().toLowerCase();
  return q ? rows.filter((c) => c.name.toLowerCase().includes(q)) : rows;
}
