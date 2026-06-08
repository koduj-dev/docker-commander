import { useDeferredValue, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Maximize2, Search, Share2, List } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { Topology as Topo } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { Spinner, EmptyState } from "../components/ui";
import { getPref, setPref } from "../lib/prefs";
import { TopoGraph, TopoList, topoStats, type TopoFilters } from "../components/TopoGraph";

type TopoPrefs = { hideEmptyNetworks?: boolean; showStopped?: boolean; stack?: string; view?: "graph" | "list" };

export function Topology() {
  const saved = getPref<TopoPrefs>("topology.prefs", {});
  const [topo, setTopo] = useState<Topo | null>(null);
  const [filters, setFilters] = useState<TopoFilters>({
    hideEmptyNetworks: saved.hideEmptyNetworks ?? true,
    showStopped: saved.showStopped ?? false,
    search: "",
    stack: saved.stack ?? "",
  });
  const [view, setView] = useState<"graph" | "list">(saved.view ?? "graph");
  const navigate = useNavigate();
  const wrapRef = useRef<HTMLDivElement>(null);

  // Remember the toggles / stack / view (not the ephemeral search) across reloads.
  useEffect(() => {
    setPref("topology.prefs", { hideEmptyNetworks: filters.hideEmptyNetworks, showStopped: filters.showStopped, stack: filters.stack, view });
  }, [filters.hideEmptyNetworks, filters.showStopped, filters.stack, view]);

  const stacks = useMemo(() => [...new Set((topo?.containers ?? []).map((c) => c.stack).filter((s): s is string => !!s))].sort(), [topo]);
  // Defer the search so typing stays responsive — the force layout only
  // recomputes once typing settles, not on every keystroke.
  const deferredSearch = useDeferredValue(filters.search);
  // Depend on the individual non-search fields (not the whole `filters` object,
  // which gets a new reference on every keystroke) so effFilters — and the force
  // layout it drives — stays stable while typing and only updates once the
  // deferred search settles.
  const effFilters = useMemo<TopoFilters>(
    () => ({ hideEmptyNetworks: filters.hideEmptyNetworks, showStopped: filters.showStopped, stack: filters.stack, search: deferredSearch }),
    [filters.hideEmptyNetworks, filters.showStopped, filters.stack, deferredSearch],
  );
  const stats = useMemo(() => (topo ? topoStats(topo, effFilters) : { networks: 0, containers: 0 }), [topo, effFilters]);

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
          <TopoList topo={topo} filters={effFilters} onOpen={(cid) => navigate(`/containers/${cid}`)} />
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
            <TopoGraph topo={topo} filters={effFilters} onContainerClick={(cid) => navigate(`/containers/${cid}`)} />
          </div>
        )}
      </div>
    </>
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
