import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Boxes, Cpu, HardDrive, ArrowDownUp } from "lucide-react";
import { api } from "../lib/api";
import type { ResourceOverview } from "../lib/types";
import { bytes, coresLabel, cpuCores, rate } from "../lib/format";
import { sortUsage, type UsageSortKey } from "../lib/resources";
import { Pager, SearchBar, useListControls } from "../components/ListControls";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner, StatCard } from "../components/ui";
import { SortHeader } from "../components/ResourceTable";

const REFRESH_MS = 5000;

// Resources is the full numeric view behind the dashboard's donuts: every
// running container with what it actually takes (cores, bytes, throughput),
// sortable and filterable. Deliberately numbers, not charts — the dashboard
// already shows shares graphically; this answers "how much, exactly".
//
// The figures come from the monitor's background sampler, which updates on its
// own (slower) cadence — this page re-reads that snapshot every REFRESH_MS, so
// two consecutive refreshes can legitimately show the same numbers.
export function Resources() {
  const [data, setData] = useState<ResourceOverview | null>(null);
  const [error, setError] = useState("");
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null);
  const [sort, setSort] = useState<UsageSortKey>("mem");
  const [desc, setDesc] = useState(true);

  useEffect(() => {
    let cancelled = false;
    const load = () =>
      api
        .statsOverview()
        .then((d) => { if (!cancelled) { setData(d); setError(""); setUpdatedAt(new Date()); } })
        // A transient failure keeps the last good table instead of blanking it.
        .catch((e) => { if (!cancelled) setError(e instanceof Error ? e.message : "could not read resource usage"); });
    load();
    const t = setInterval(load, REFRESH_MS);
    return () => { cancelled = true; clearInterval(t); };
  }, []);

  const onSort = (k: UsageSortKey) => {
    if (k === sort) setDesc((d) => !d);
    else { setSort(k); setDesc(k !== "name"); }
  };

  const containers = data?.containers ?? []; // Go sends null, not [], when empty
  const cpus = data?.cpus ?? 0;
  const cpuUsed = containers.reduce((n, c) => n + c.cpuPercent, 0);
  const memUsed = containers.reduce((n, c) => n + c.memBytes, 0);
  const rx = containers.reduce((n, c) => n + c.netRxRate, 0);
  const tx = containers.reduce((n, c) => n + c.netTxRate, 0);
  // Sort first, then let the shared list controls filter + paginate the sorted
  // rows — the same full-width search bar and pager every other list uses.
  const sorted = useMemo(() => sortUsage(containers, sort, desc), [containers, sort, desc]);
  const controls = useListControls(sorted, (c, q) => c.name.toLowerCase().includes(q), { storageKey: "resources" });
  const rows = controls.pageItems;

  return (
    <>
      <PageHeader
        title="Resources"
        actions={updatedAt && <span className="text-xs text-muted">Updated {updatedAt.toLocaleTimeString()} · refreshes every 5 s</span>}
      />
      <div className="p-6 space-y-4">
        {error && <div className="text-sm text-danger">Couldn't read resource usage: {error}</div>}
        {!data ? (
          !error && <div className="flex items-center gap-2 text-muted"><Spinner /> Loading…</div>
        ) : (
          <>
            <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
              <StatCard icon={<Boxes className="h-5 w-5" />} label="Running containers" value={containers.length} />
              <StatCard icon={<Cpu className="h-5 w-5" />} label="CPU" value={`${coresLabel(cpuCores(cpuUsed, cpus))} cores`} sub={`of ${cpus} · ${cpuUsed.toFixed(1)} % of host`} />
              <StatCard icon={<HardDrive className="h-5 w-5" />} label="Memory" value={bytes(memUsed)} sub={`of ${bytes(data.memTotal)} · ${data.memTotal ? ((memUsed / data.memTotal) * 100).toFixed(1) : "0.0"} %`} />
              <StatCard icon={<ArrowDownUp className="h-5 w-5" />} label="Network" value={`↓ ${rate(rx)}`} sub={`↑ ${rate(tx)}`} />
            </div>

            <SearchBar controls={controls} placeholder="Search containers by name…" />

            {containers.length === 0 ? (
              <EmptyState title="No running containers" hint="Nothing to sample on this host." />
            ) : controls.filteredCount === 0 ? (
              <EmptyState title="No container matches that name" hint="Try a shorter or different filter." />
            ) : (
              <div className="card overflow-hidden">
                <table className="w-full text-sm">
                  <thead className="text-muted text-xs">
                    <tr className="border-b border-border">
                      <SortHeader label="Container" k="name" sort={sort} desc={desc} onSort={onSort} className="text-left" />
                      <SortHeader label="CPU" k="cpu" sort={sort} desc={desc} onSort={onSort} className="text-right" />
                      <SortHeader label="Memory" k="mem" sort={sort} desc={desc} onSort={onSort} className="text-right" />
                      <SortHeader label="Received" k="rx" sort={sort} desc={desc} onSort={onSort} className="text-right" />
                      <SortHeader label="Sent" k="tx" sort={sort} desc={desc} onSort={onSort} className="text-right" />
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((c) => (
                      <tr key={c.id} className="border-b border-border/50 last:border-0">
                        <td className="px-4 py-2.5 font-medium">
                          <Link to={`/containers/${c.id}`} className="hover:underline">{c.name}</Link>
                        </td>
                        <td className="px-4 py-2.5 text-right font-mono text-xs">
                          {coresLabel(cpuCores(c.cpuPercent, cpus))} <span className="text-muted">cores · {c.cpuPercent.toFixed(1)} %</span>
                        </td>
                        <td className="px-4 py-2.5 text-right font-mono text-xs">
                          {bytes(c.memBytes)} <span className="text-muted">· {c.memPercent.toFixed(1)} %</span>
                        </td>
                        <td className="px-4 py-2.5 text-right font-mono text-xs">{rate(c.netRxRate)}</td>
                        <td className="px-4 py-2.5 text-right font-mono text-xs">{rate(c.netTxRate)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            {containers.length > 0 && <Pager controls={controls} />}
          </>
        )}
      </div>
    </>
  );
}
