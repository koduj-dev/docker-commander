import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  Background,
  BackgroundVariant,
  Controls,
  Handle,
  MiniMap,
  Position,
  ReactFlow,
  useEdgesState,
  useNodesState,
  type Edge,
  type Node,
  type NodeProps,
  type ReactFlowInstance,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import dagre from "@dagrejs/dagre";
import { Boxes, Maximize2, Network as NetworkIcon, Search } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { Topology as Topo } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { Spinner, EmptyState } from "../components/ui";
import { FloatingEdge } from "../components/FloatingEdge";

interface TopoFilters {
  hideEmptyNetworks: boolean;
  showStopped: boolean;
  search: string;
  stack: string; // "" = all stacks
}

const NET_W = 200;
const NET_H = 64;
const CON_W = 200;
const CON_H = 56;

// ---- Custom nodes -----------------------------------------------------------

function NetworkNode({ data }: NodeProps) {
  const d = data as { label: string; driver: string; subnet: string };
  return (
    <div
      className="rounded-xl border border-accent/40 bg-accent/10 px-3 py-2 shadow-lg"
      style={{ width: NET_W, height: NET_H }}
    >
      <Handle type="source" position={Position.Right} className="opacity-0!" />
      <div className="flex items-center gap-2">
        <NetworkIcon className="h-4 w-4 text-accent shrink-0" />
        <div className="min-w-0">
          <div className="text-sm font-semibold truncate text-text">{d.label}</div>
          <div className="text-[10px] text-muted truncate">
            {d.driver}
            {d.subnet ? ` · ${d.subnet}` : ""}
          </div>
        </div>
      </div>
    </div>
  );
}

function ContainerNode({ data }: NodeProps) {
  const d = data as { label: string; image: string; state: string };
  return (
    <div
      className={clsx(
        "rounded-lg border bg-panel px-3 py-2 shadow-sm",
        d.state === "running" ? "border-ok/40" : "border-border"
      )}
      style={{ width: CON_W, height: CON_H }}
    >
      <Handle type="target" position={Position.Left} className="opacity-0!" />
      <div className="flex items-center gap-2">
        <span
          className={clsx(
            "h-2 w-2 rounded-full shrink-0",
            d.state === "running" ? "bg-ok" : d.state === "paused" ? "bg-warn" : "bg-danger"
          )}
        />
        <Boxes className="h-4 w-4 text-muted shrink-0" />
        <div className="min-w-0">
          <div className="text-sm font-medium truncate text-text">{d.label}</div>
          <div className="text-[10px] text-muted truncate">{d.image}</div>
        </div>
      </div>
    </div>
  );
}

const nodeTypes = { network: NetworkNode, container: ContainerNode };
const edgeTypes = { floating: FloatingEdge };

// ---- Layout -----------------------------------------------------------------

function layout(topo: Topo, filters: TopoFilters): { nodes: Node[]; edges: Edge[] } {
  const g = new dagre.graphlib.Graph();
  g.setDefaultEdgeLabel(() => ({}));
  g.setGraph({ rankdir: "LR", nodesep: 22, ranksep: 240, marginx: 20, marginy: 20 });

  const q = filters.search.trim().toLowerCase();
  const containers = (topo.containers ?? []).filter((c) => {
    if (!filters.showStopped && c.state !== "running") return false;
    if (filters.stack && (c.stack ?? "") !== filters.stack) return false;
    if (q && !(c.name.toLowerCase().includes(q) || c.image.toLowerCase().includes(q) || (c.stack ?? "").toLowerCase().includes(q))) return false;
    return true;
  });
  const visibleContainerIds = new Set(containers.map((c) => c.id));
  // Keep only links whose container is visible after the filters.
  const links = (topo.links ?? []).filter((l) => visibleContainerIds.has(l.containerId));

  // Only show containers that are actually attached to something — isolated
  // containers add noise to a connectivity diagram.
  const linkedContainers = new Set(links.map((l) => l.containerId));
  // A network with no visible attached containers is "empty"; hide when asked,
  // and always while a search/stack filter is narrowing the view.
  const hideEmpty = filters.hideEmptyNetworks || !!q || !!filters.stack;
  const linkedNetworks = new Set(links.map((l) => l.networkId));
  const networks = (topo.networks ?? []).filter((n) => !hideEmpty || linkedNetworks.has(n.id));
  const visibleNetworkIds = new Set(networks.map((n) => n.id));

  for (const n of networks) {
    g.setNode(`n:${n.id}`, { width: NET_W, height: NET_H });
  }
  for (const c of containers) {
    if (linkedContainers.has(c.id)) g.setNode(`c:${c.id}`, { width: CON_W, height: CON_H });
  }
  for (const l of links) {
    if (visibleNetworkIds.has(l.networkId)) g.setEdge(`n:${l.networkId}`, `c:${l.containerId}`);
  }

  dagre.layout(g);

  const nodes: Node[] = [];
  for (const n of networks) {
    const p = g.node(`n:${n.id}`);
    if (!p) continue;
    nodes.push({
      id: `n:${n.id}`,
      type: "network",
      position: { x: p.x - NET_W / 2, y: p.y - NET_H / 2 },
      data: { label: n.name, driver: n.driver, subnet: (n.subnets ?? [])[0] ?? "" },
    });
  }
  for (const c of containers) {
    const p = g.node(`c:${c.id}`);
    if (!p) continue;
    nodes.push({
      id: `c:${c.id}`,
      type: "container",
      position: { x: p.x - CON_W / 2, y: p.y - CON_H / 2 },
      data: { label: c.name, image: c.image, state: c.state, cid: c.id },
    });
  }

  const edges: Edge[] = links
    .filter((l) => visibleNetworkIds.has(l.networkId))
    .map((l, i) => ({
      id: `e:${i}`,
      type: "floating",
      source: `n:${l.networkId}`,
      target: `c:${l.containerId}`,
      animated: true,
      style: { stroke: "#3a4a66", strokeWidth: 1.5 },
    }));

  return { nodes, edges };
}

// ---- Page -------------------------------------------------------------------

export function Topology() {
  const [topo, setTopo] = useState<Topo | null>(null);
  const [nodes, setNodes, onNodesChange] = useNodesState<Node>([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([]);
  const [filters, setFilters] = useState<TopoFilters>({ hideEmptyNetworks: false, showStopped: true, search: "", stack: "" });
  const stacks = useMemo(() => [...new Set((topo?.containers ?? []).map((c) => c.stack).filter((s): s is string => !!s))].sort(), [topo]);
  const counts = useMemo(() => ({
    networks: nodes.filter((n) => n.type === "network").length,
    containers: nodes.filter((n) => n.type === "container").length,
  }), [nodes]);
  const navigate = useNavigate();
  const wrapRef = useRef<HTMLDivElement>(null);
  const rfRef = useRef<ReactFlowInstance<Node, Edge> | null>(null);

  // Keep the fit readable: a 50-container bipartite graph is very tall, so an
  // unclamped fitView shrinks nodes to dust. minZoom floors the fit zoom; users
  // pan / use the minimap / zoom out (down to the lower ReactFlow minZoom).
  const FIT = { minZoom: 0.5, maxZoom: 1.2, padding: 0.12 };

  useEffect(() => {
    api.topology().then(setTopo).catch(() => setTopo({ networks: [], containers: [], links: [] }));
  }, []);

  // Refit when entering/leaving fullscreen, once the container has resized.
  useEffect(() => {
    const onFs = () => setTimeout(() => rfRef.current?.fitView(FIT), 120);
    document.addEventListener("fullscreenchange", onFs);
    return () => document.removeEventListener("fullscreenchange", onFs);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Recompute the dagre layout whenever the topology or filters change, then
  // hand the nodes to React Flow's state so the user can drag them afterwards.
  useEffect(() => {
    if (!topo) return;
    const { nodes: n, edges: e } = layout(topo, filters);
    setNodes(n);
    setEdges(e);
  }, [topo, filters, setNodes, setEdges]);

  const onNodeClick = useCallback(
    (_: unknown, node: Node) => {
      if (node.type === "container") {
        const cid = (node.data as { cid: string }).cid;
        navigate(`/containers/${cid}`);
      }
    },
    [navigate]
  );

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
            <FilterToggle label="Hide empty" active={filters.hideEmptyNetworks} onClick={() => setFilters((f) => ({ ...f, hideEmptyNetworks: !f.hideEmptyNetworks }))} />
            <FilterToggle label="Show stopped" active={filters.showStopped} onClick={() => setFilters((f) => ({ ...f, showStopped: !f.showStopped }))} />
          </div>
        }
      />
      <div className="p-6">
        {!topo ? (
          <div className="flex items-center gap-2 text-muted"><Spinner /> Building graph…</div>
        ) : nodes.length === 0 ? (
          <EmptyState title="Nothing to show" hint="No networks with attached containers match the current filters." />
        ) : (
          <div ref={wrapRef} className="dc-topo card overflow-hidden relative bg-bg" style={{ height: "calc(100vh - 9rem)" }}>
            <div className="absolute top-3 left-3 z-10 text-[11px] text-muted bg-panel2/80 backdrop-blur-sm rounded-md px-2 py-1 border border-border">
              {counts.containers} container{counts.containers === 1 ? "" : "s"} · {counts.networks} network{counts.networks === 1 ? "" : "s"}
            </div>
            <button
              className="btn-ghost absolute top-3 right-3 z-10 px-2 py-1.5"
              title="Toggle fullscreen"
              onClick={toggleFullscreen}
            >
              <Maximize2 className="h-4 w-4" />
            </button>
            <ReactFlow
              nodes={nodes}
              edges={edges}
              onNodesChange={onNodesChange}
              onEdgesChange={onEdgesChange}
              nodeTypes={nodeTypes}
              edgeTypes={edgeTypes}
              onNodeClick={onNodeClick}
              onInit={(inst) => { rfRef.current = inst; }}
              fitView
              fitViewOptions={FIT}
              proOptions={{ hideAttribution: true }}
              minZoom={0.15}
            >
              <Background variant={BackgroundVariant.Dots} gap={20} size={1} color="#243047" />
              <Controls className="dc-flow-controls" />
              <MiniMap
                pannable
                zoomable
                className="bg-panel2!"
                nodeColor={(n) => (n.type === "network" ? "#2496ed" : "#2dd4a7")}
                maskColor="rgba(11,15,23,0.7)"
              />
            </ReactFlow>
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
