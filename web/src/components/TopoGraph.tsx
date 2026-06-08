import { useCallback, useEffect, useRef } from "react";
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
import { forceSimulation, forceLink, forceManyBody, forceCollide, forceX, forceY, type SimulationNodeDatum } from "d3-force";
import { Boxes, Network as NetworkIcon } from "lucide-react";
import clsx from "clsx";
import type { Topology as Topo, TopoContainer } from "../lib/types";
import { FloatingEdge } from "./FloatingEdge";

export interface TopoFilters {
  hideEmptyNetworks: boolean;
  showStopped: boolean;
  search: string;
  stack: string; // "" = all stacks
}

type SimNode = SimulationNodeDatum & { id: string };
type SimLink = { source: string; target: string };

const NET_W = 200;
const NET_H = 64;
const CON_W = 200;
const CON_H = 56;

// ---- Custom nodes -----------------------------------------------------------

function NetworkNode({ data }: NodeProps) {
  const d = data as { label: string; driver: string; subnet: string };
  return (
    <div className="rounded-xl border border-accent/40 bg-accent/10 px-3 py-2 shadow-lg" style={{ width: NET_W, height: NET_H }}>
      <Handle type="source" position={Position.Right} className="opacity-0!" />
      <div className="flex items-center gap-2">
        <NetworkIcon className="h-4 w-4 text-accent shrink-0" />
        <div className="min-w-0">
          <div className="text-sm font-semibold truncate text-text">{d.label}</div>
          <div className="text-[10px] text-muted truncate">{d.driver}{d.subnet ? ` · ${d.subnet}` : ""}</div>
        </div>
      </div>
    </div>
  );
}

function ContainerNode({ data }: NodeProps) {
  const d = data as { label: string; image: string; state: string };
  return (
    <div className={clsx("rounded-lg border bg-panel px-3 py-2 shadow-sm", d.state === "running" ? "border-ok/40" : "border-border")} style={{ width: CON_W, height: CON_H }}>
      <Handle type="target" position={Position.Left} className="opacity-0!" />
      <div className="flex items-center gap-2">
        <span className={clsx("h-2 w-2 rounded-full shrink-0", d.state === "running" ? "bg-ok" : d.state === "paused" ? "bg-warn" : "bg-danger")} />
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

// containerMatches applies the state / stack / search filters shared by the
// graph and the list view.
export function containerMatches(c: TopoContainer, filters: TopoFilters): boolean {
  if (!filters.showStopped && c.state !== "running") return false;
  if (filters.stack && (c.stack ?? "") !== filters.stack) return false;
  const q = filters.search.trim().toLowerCase();
  if (q && !(c.name.toLowerCase().includes(q) || c.image.toLowerCase().includes(q) || (c.stack ?? "").toLowerCase().includes(q))) return false;
  return true;
}

// topoStats returns how many networks/containers are visible under the filters
// (without running the layout — for a header badge).
export function topoStats(topo: Topo, filters: TopoFilters): { networks: number; containers: number } {
  const containers = (topo.containers ?? []).filter((c) => containerMatches(c, filters));
  const visible = new Set(containers.map((c) => c.id));
  const links = (topo.links ?? []).filter((l) => visible.has(l.containerId));
  const linkedContainers = new Set(links.map((l) => l.containerId));
  const linkedNetworks = new Set(links.map((l) => l.networkId));
  const hideEmpty = filters.hideEmptyNetworks || !!filters.search.trim() || !!filters.stack;
  const networks = (topo.networks ?? []).filter((n) => !hideEmpty || linkedNetworks.has(n.id));
  return { networks: networks.length, containers: containers.filter((c) => linkedContainers.has(c.id)).length };
}

function layout(topo: Topo, filters: TopoFilters): { nodes: Node[]; edges: Edge[] } {
  const containers = (topo.containers ?? []).filter((c) => containerMatches(c, filters));
  const visibleContainerIds = new Set(containers.map((c) => c.id));
  const links = (topo.links ?? []).filter((l) => visibleContainerIds.has(l.containerId));

  const linkedContainers = new Set(links.map((l) => l.containerId));
  const hideEmpty = filters.hideEmptyNetworks || !!filters.search.trim() || !!filters.stack;
  const linkedNetworks = new Set(links.map((l) => l.networkId));
  const networks = (topo.networks ?? []).filter((n) => !hideEmpty || linkedNetworks.has(n.id));
  const visibleNetworkIds = new Set(networks.map((n) => n.id));
  const shownContainers = containers.filter((c) => linkedContainers.has(c.id));

  // Force-directed layout: containers cluster around the networks they're on and
  // the graph spreads across 2D. A fixed tick count keeps it deterministic.
  const simNodes: SimNode[] = [
    ...networks.map((n) => ({ id: `n:${n.id}` })),
    ...shownContainers.map((c) => ({ id: `c:${c.id}` })),
  ];
  const simLinks: SimLink[] = links
    .filter((l) => visibleNetworkIds.has(l.networkId))
    .map((l) => ({ source: `n:${l.networkId}`, target: `c:${l.containerId}` }));

  forceSimulation(simNodes)
    .force("link", forceLink<SimNode, SimLink>(simLinks).id((d) => d.id).distance(180).strength(0.6))
    .force("charge", forceManyBody().strength(-1100))
    .force("collide", forceCollide(115))
    .force("x", forceX(0).strength(0.05))
    .force("y", forceY(0).strength(0.07))
    .stop()
    .tick(340);

  const pos = new Map(simNodes.map((n) => [n.id, { x: n.x ?? 0, y: n.y ?? 0 }]));

  const nodes: Node[] = [];
  for (const n of networks) {
    const p = pos.get(`n:${n.id}`);
    if (!p) continue;
    nodes.push({ id: `n:${n.id}`, type: "network", position: { x: p.x - NET_W / 2, y: p.y - NET_H / 2 }, data: { label: n.name, driver: n.driver, subnet: (n.subnets ?? [])[0] ?? "" } });
  }
  for (const c of shownContainers) {
    const p = pos.get(`c:${c.id}`);
    if (!p) continue;
    nodes.push({ id: `c:${c.id}`, type: "container", position: { x: p.x - CON_W / 2, y: p.y - CON_H / 2 }, data: { label: c.name, image: c.image, state: c.state, cid: c.id } });
  }

  const edges: Edge[] = links
    .filter((l) => visibleNetworkIds.has(l.networkId))
    .map((l, i) => ({ id: `e:${i}`, type: "floating", source: `n:${l.networkId}`, target: `c:${l.containerId}`, style: { stroke: "#3a4a66", strokeWidth: 1.5 } }));

  return { nodes, edges };
}

// ---- Component --------------------------------------------------------------

const FIT = { minZoom: 0.2, maxZoom: 1.2, padding: 0.15 };

// TopoGraph renders the container↔network graph with React Flow. Used by the
// Topology page and the Networks detail modal so both share one look.
export function TopoGraph({ topo, filters, onContainerClick, minimap = true }: {
  topo: Topo;
  filters: TopoFilters;
  onContainerClick?: (id: string) => void;
  minimap?: boolean;
}) {
  const [nodes, setNodes, onNodesChange] = useNodesState<Node>([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([]);
  const rfRef = useRef<ReactFlowInstance<Node, Edge> | null>(null);

  useEffect(() => {
    const { nodes: n, edges: e } = layout(topo, filters);
    setNodes(n);
    setEdges(e);
  }, [topo, filters, setNodes, setEdges]);

  // Refit when entering/leaving fullscreen, once the container has resized.
  useEffect(() => {
    const onFs = () => setTimeout(() => rfRef.current?.fitView(FIT), 120);
    document.addEventListener("fullscreenchange", onFs);
    return () => document.removeEventListener("fullscreenchange", onFs);
  }, []);

  const onNodeClick = useCallback((_: unknown, node: Node) => {
    if (node.type === "container") onContainerClick?.((node.data as { cid: string }).cid);
  }, [onContainerClick]);

  return (
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
      {minimap && (
        <MiniMap pannable zoomable className="bg-panel2!" nodeColor={(n) => (n.type === "network" ? "#2496ed" : "#2dd4a7")} maskColor="rgba(11,15,23,0.7)" />
      )}
    </ReactFlow>
  );
}
