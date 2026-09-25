import { Fragment, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { ChevronDown, ChevronRight } from "lucide-react";
import { api } from "../../lib/api";
import type { ResourceOverview, ResourceUsage, Stack } from "../../lib/types";
import { bytes, coresLabel, cpuCores, rate } from "../../lib/format";
import { sortUsage, type UsageSortKey } from "../../lib/resources";
import { Pager, SearchBar, useListControls, type StatusOption } from "../ListControls";
import { EmptyState, Spinner } from "../ui";
import { SortHeader } from "../ResourceTable";

// One row per compose stack: the sum over its running containers. The row
// borrows ResourceUsage's shape (id/name = the project) so it sorts with the
// same helper as the Containers tab.
export type StackRow = ResourceUsage & { running: number; total: number; members: ResourceUsage[] };

// stackRows joins the stack list with the sampler snapshot by container id. A
// stopped container has no sample, so it simply contributes nothing.
export function stackRows(stacks: Stack[], usage: ResourceUsage[]): StackRow[] {
  const byId = new Map(usage.map((u) => [u.id, u]));
  return stacks.map((s) => {
    const members = s.containers.map((c) => byId.get(c.id)).filter((u): u is ResourceUsage => !!u);
    const sum = (f: (u: ResourceUsage) => number) => members.reduce((n, u) => n + f(u), 0);
    return {
      id: s.project, name: s.project, running: s.running, total: s.containers.length,
      cpuPercent: sum((u) => u.cpuPercent), memBytes: sum((u) => u.memBytes), memPercent: sum((u) => u.memPercent),
      netRxRate: sum((u) => u.netRxRate), netTxRate: sum((u) => u.netTxRate),
      members: [...members].sort((a, b) => b.memBytes - a.memBytes),
    };
  });
}

// "Running" comes first so it is the default view; the option order is the
// only thing that makes it the default (see useListControls).
const STATUSES: StatusOption<StackRow>[] = [
  { value: "running", label: "Running stacks", test: (r) => r.running > 0 },
  { value: "all", label: "All stacks" },
];

// The Stacks tab: what each compose stack takes in total, expandable to its
// containers. Stack membership is re-read every few seconds alongside the
// sampler snapshot passed in by the page.
export function StacksTab({ data }: { data: ResourceOverview }) {
  const [stacks, setStacks] = useState<Stack[] | null>(null);
  const [error, setError] = useState("");
  const [sort, setSort] = useState<UsageSortKey>("mem");
  const [desc, setDesc] = useState(true);
  const [open, setOpen] = useState<Set<string>>(new Set());

  useEffect(() => {
    let cancelled = false;
    let latest = 0; // only the most recently started poll may apply its result
    const load = () => {
      const mine = ++latest;
      const current = () => !cancelled && mine === latest;
      return api.stacks()
        .then((s) => { if (current()) { setStacks(s ?? []); setError(""); } })
        .catch((e) => { if (current()) setError(e instanceof Error ? e.message : "could not list stacks"); });
    };
    load();
    const t = setInterval(load, 5000);
    return () => { cancelled = true; clearInterval(t); };
  }, []);

  const onSort = (k: UsageSortKey) => {
    if (k === sort) setDesc((d) => !d);
    else { setSort(k); setDesc(k !== "name"); }
  };
  const cpus = data.cpus;
  const rows = useMemo(() => sortUsage(stackRows(stacks ?? [], data.containers ?? []), sort, desc) as StackRow[], [stacks, data, sort, desc]);
  const controls = useListControls(rows, (r, q) => r.name.toLowerCase().includes(q), { storageKey: "resources-stacks", statuses: STATUSES });

  if (!stacks) {
    return error
      ? <div className="text-sm text-danger">Couldn't list stacks: {error}</div>
      : <div className="flex items-center gap-2 text-muted"><Spinner /> Loading…</div>;
  }
  const toggle = (name: string) => setOpen((o) => { const n = new Set(o); if (n.has(name)) n.delete(name); else n.add(name); return n; });

  return (
    <div className="space-y-4">
      {error && <div className="text-sm text-danger">Couldn't refresh stacks: {error}</div>}
      <SearchBar controls={controls} placeholder="Search stacks by name…" />
      {controls.filteredCount === 0 ? (
        <EmptyState
          title={controls.totalCount === 0 ? "No stacks" : "No stack matches"}
          hint={controls.totalCount === 0 ? "Compose projects on this host show up here." : "Try another filter, or switch to All stacks."}
        />
      ) : (
        <div className="card overflow-hidden">
          <table className="w-full text-sm">
            <thead className="text-muted text-xs">
              <tr className="border-b border-border">
                <SortHeader label="Stack" k="name" sort={sort} desc={desc} onSort={onSort} className="text-left" />
                <th className="font-medium px-4 py-3 text-right uppercase tracking-wide">Running</th>
                <SortHeader label="CPU" k="cpu" sort={sort} desc={desc} onSort={onSort} className="text-right" />
                <SortHeader label="Memory" k="mem" sort={sort} desc={desc} onSort={onSort} className="text-right" />
                <SortHeader label="Received" k="rx" sort={sort} desc={desc} onSort={onSort} className="text-right" />
                <SortHeader label="Sent" k="tx" sort={sort} desc={desc} onSort={onSort} className="text-right" />
              </tr>
            </thead>
            <tbody>
              {controls.pageItems.map((r) => {
                const isOpen = open.has(r.name);
                const Chev = isOpen ? ChevronDown : ChevronRight;
                return (
                  <Fragment key={r.name}>
                    <tr className="border-b border-border/50 cursor-pointer hover:bg-panel2/40" onClick={() => toggle(r.name)}>
                      <td className="px-4 py-2.5 font-medium">
                        {/* A real button: keyboard-focusable and announces its state. The row's own
                            click stays as a bigger mouse target, so this one stops the bubble
                            (otherwise a click would toggle twice). */}
                        <button
                          type="button"
                          aria-expanded={isOpen}
                          className="inline-flex items-center gap-1.5 text-left"
                          onClick={(e) => { e.stopPropagation(); toggle(r.name); }}
                        >
                          <Chev className="h-3.5 w-3.5 text-muted" />{r.name}
                        </button>
                      </td>
                      <td className="px-4 py-2.5 whitespace-nowrap text-right text-xs text-muted">{r.running}/{r.total}</td>
                      <td className="px-4 py-2.5 whitespace-nowrap text-right font-mono text-xs">
                        {coresLabel(cpuCores(r.cpuPercent, cpus))} <span className="text-muted">cores · {r.cpuPercent.toFixed(1)} %</span>
                      </td>
                      <td className="px-4 py-2.5 whitespace-nowrap text-right font-mono text-xs">
                        {bytes(r.memBytes)} <span className="text-muted">· {r.memPercent.toFixed(1)} %</span>
                      </td>
                      <td className="px-4 py-2.5 whitespace-nowrap text-right font-mono text-xs">{rate(r.netRxRate)}</td>
                      <td className="px-4 py-2.5 whitespace-nowrap text-right font-mono text-xs">{rate(r.netTxRate)}</td>
                    </tr>
                    {isOpen && r.members.map((m) => (
                      <tr key={m.id} className="border-b border-border/30 bg-panel2/20 text-xs">
                        <td className="pl-10 pr-4 py-1.5"><Link to={`/containers/${m.id}`} className="hover:underline">{m.name}</Link></td>
                        <td />
                        <td className="px-4 py-1.5 text-right font-mono">{coresLabel(cpuCores(m.cpuPercent, cpus))} <span className="text-muted">cores</span></td>
                        <td className="px-4 py-1.5 text-right font-mono">{bytes(m.memBytes)}</td>
                        <td className="px-4 py-1.5 text-right font-mono">{rate(m.netRxRate)}</td>
                        <td className="px-4 py-1.5 text-right font-mono">{rate(m.netTxRate)}</td>
                      </tr>
                    ))}
                    {isOpen && r.members.length === 0 && (
                      <tr className="border-b border-border/30 bg-panel2/20 text-xs text-muted"><td className="pl-10 py-1.5" colSpan={6}>No running containers.</td></tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {controls.totalCount > 0 && <Pager controls={controls} />}
    </div>
  );
}
