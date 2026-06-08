import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Maximize2, Search, Share2, List } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { Topology as Topo } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { Spinner, EmptyState } from "../components/ui";
import { TopoGraph, topoStats, containerMatches, type TopoFilters } from "../components/TopoGraph";

export function Topology() {
  const [topo, setTopo] = useState<Topo | null>(null);
  const [filters, setFilters] = useState<TopoFilters>({ hideEmptyNetworks: true, showStopped: true, search: "", stack: "" });
  const [view, setView] = useState<"graph" | "list">("graph");
  const navigate = useNavigate();
  const wrapRef = useRef<HTMLDivElement>(null);

  const stacks = useMemo(() => [...new Set((topo?.containers ?? []).map((c) => c.stack).filter((s): s is string => !!s))].sort(), [topo]);
  const stats = useMemo(() => (topo ? topoStats(topo, filters) : { networks: 0, containers: 0 }), [topo, filters]);

  useEffect(() => {
    api.topology().then(setTopo).catch(() => setTopo({ networks: [], containers: [], links: [] }));
  }, []);

  const toggleFullscreen = () => {
    const el = wrapRef.current;
    if (!el) return;
    if (document.fullscreenElement) document.exitFullscreen();
    else el.requestFullscreen?.();
  };

  return (
    <>
      <PageHeader
        title="Topology"
        actions={
          <div className="flex items-center gap-2">
            <div className="relative">
              <Search className="h-3.5 w-3.5 text-muted absolute left-2 top-1/2 -translate-y-1/2 pointer-events-none" />
              <input className="input pl-7 py-1.5 h-8 w-44 text-sm" placeholder="Find container…" value={filters.search} onChange={(e) => setFilters((f) => ({ ...f, search: e.target.value }))} />
            </div>
            {stacks.length > 0 && (
              <select className="input py-1.5 h-8 text-sm w-36" value={filters.stack} onChange={(e) => setFilters((f) => ({ ...f, stack: e.target.value }))} title="Filter by compose stack">
                <option value="">All stacks</option>
                {stacks.map((s) => <option key={s} value={s}>{s}</option>)}
              </select>
            )}
            {view === "graph" && <FilterToggle label="Hide empty" active={filters.hideEmptyNetworks} onClick={() => setFilters((f) => ({ ...f, hideEmptyNetworks: !f.hideEmptyNetworks }))} />}
            <FilterToggle label="Show stopped" active={filters.showStopped} onClick={() => setFilters((f) => ({ ...f, showStopped: !f.showStopped }))} />
            <div className="flex rounded-md border border-border overflow-hidden">
              <button className={clsx("px-2 py-1.5", view === "graph" ? "bg-accent/15 text-accent" : "bg-panel2 text-muted hover:text-text")} title="Graph view" onClick={() => setView("graph")}><Share2 className="h-3.5 w-3.5" /></button>
              <button className={clsx("px-2 py-1.5", view === "list" ? "bg-accent/15 text-accent" : "bg-panel2 text-muted hover:text-text")} title="List view" onClick={() => setView("list")}><List className="h-3.5 w-3.5" /></button>
            </div>
          </div>
        }
      />
      <div className="p-6">
        {!topo ? (
          <div className="flex items-center gap-2 text-muted"><Spinner /> Building graph…</div>
        ) : view === "list" ? (
          <TopoList topo={topo} filters={filters} onOpen={(cid) => navigate(`/containers/${cid}`)} />
        ) : stats.networks === 0 && stats.containers === 0 ? (
          <EmptyState title="Nothing to show" hint="No networks with attached containers match the current filters." />
        ) : (
          <div ref={wrapRef} className="dc-topo card overflow-hidden relative bg-bg" style={{ height: "calc(100vh - 9rem)" }}>
            <div className="absolute top-3 left-3 z-10 text-[11px] text-muted bg-panel2/80 backdrop-blur-sm rounded-md px-2 py-1 border border-border">
              {stats.containers} container{stats.containers === 1 ? "" : "s"} · {stats.networks} network{stats.networks === 1 ? "" : "s"}
            </div>
            <button className="btn-ghost absolute top-3 right-3 z-10 px-2 py-1.5" title="Toggle fullscreen" onClick={toggleFullscreen}>
              <Maximize2 className="h-4 w-4" />
            </button>
            <TopoGraph topo={topo} filters={filters} onContainerClick={(cid) => navigate(`/containers/${cid}`)} />
          </div>
        )}
      </div>
    </>
  );
}

// TopoList is the compact, dense alternative to the graph: a filterable table of
// containers with the networks (and IPs) each is attached to.
function TopoList({ topo, filters, onOpen }: { topo: Topo; filters: TopoFilters; onOpen: (cid: string) => void }) {
  const netById = useMemo(() => new Map((topo.networks ?? []).map((n) => [n.id, n.name])), [topo]);
  const linksByContainer = useMemo(() => {
    const m = new Map<string, { net: string; ip: string }[]>();
    for (const l of topo.links ?? []) {
      const arr = m.get(l.containerId) ?? [];
      arr.push({ net: netById.get(l.networkId) ?? l.networkId.slice(0, 12), ip: l.ipAddress });
      m.set(l.containerId, arr);
    }
    return m;
  }, [topo, netById]);

  const rows = (topo.containers ?? []).filter((c) => containerMatches(c, filters));
  if (rows.length === 0) return <EmptyState title="No containers match" hint="Adjust the search, stack or state filters." />;

  return (
    <div className="card overflow-hidden">
      <div className="px-3 py-2 text-xs text-muted border-b border-border">{rows.length} container{rows.length === 1 ? "" : "s"}</div>
      <table className="w-full text-sm">
        <thead className="text-xs uppercase tracking-wide text-muted bg-panel2">
          <tr>
            <th className="text-left font-medium px-3 py-2">Container</th>
            <th className="text-left font-medium px-3 py-2">Image</th>
            <th className="text-left font-medium px-3 py-2">Stack</th>
            <th className="text-left font-medium px-3 py-2">Networks</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((c) => {
            const links = linksByContainer.get(c.id) ?? [];
            return (
              <tr key={c.id} className="border-t border-border hover:bg-panel2/40 cursor-pointer" onClick={() => onOpen(c.id)}>
                <td className="px-3 py-2">
                  <div className="flex items-center gap-2 min-w-0">
                    <span className={clsx("h-2 w-2 rounded-full shrink-0", c.state === "running" ? "bg-ok" : c.state === "paused" ? "bg-warn" : "bg-danger")} />
                    <span className="font-medium truncate">{c.name}</span>
                  </div>
                </td>
                <td className="px-3 py-2 text-muted font-mono text-xs truncate max-w-[14rem]">{c.image}</td>
                <td className="px-3 py-2 text-muted text-xs">{c.stack || "—"}</td>
                <td className="px-3 py-2">
                  <div className="flex flex-wrap gap-1">
                    {links.length === 0 ? <span className="text-[10px] text-muted">—</span> : links.map((l, i) => (
                      <span key={i} className="text-[10px] font-mono bg-accent/10 text-accent rounded px-1.5 py-0.5" title={l.ip || undefined}>{l.net}{l.ip ? ` · ${l.ip}` : ""}</span>
                    ))}
                  </div>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function FilterToggle({ label, active, onClick }: { label: string; active: boolean; onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      className={clsx(
        "text-xs px-2.5 py-1.5 rounded-md font-medium border transition-colors",
        active ? "bg-accent/15 text-accent border-accent/40" : "bg-panel2 text-muted border-border hover:text-text"
      )}
    >
      {label}
    </button>
  );
}
