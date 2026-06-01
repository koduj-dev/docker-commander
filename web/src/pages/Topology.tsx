import { useCallback, useEffect, useState } from "react";
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
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import dagre from "@dagrejs/dagre";
import { Boxes, Network as NetworkIcon } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { Topology as Topo } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { Spinner, EmptyState } from "../components/ui";

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
      <Handle type="source" position={Position.Right} className="!bg-accent" />
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
        "rounded-lg border bg-panel px-3 py-2 shadow",
        d.state === "running" ? "border-ok/40" : "border-border"
      )}
      style={{ width: CON_W, height: CON_H }}
    >
      <Handle type="target" position={Position.Left} className="!bg-muted" />
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

// ---- Layout -----------------------------------------------------------------

function layout(topo: Topo): { nodes: Node[]; edges: Edge[] } {
  const g = new dagre.graphlib.Graph();
  g.setDefaultEdgeLabel(() => ({}));
  g.setGraph({ rankdir: "LR", nodesep: 24, ranksep: 140, marginx: 20, marginy: 20 });

  const networks = topo.networks ?? [];
  const containers = topo.containers ?? [];
  const links = topo.links ?? [];

  // Only show containers that are actually attached to something — isolated
  // containers add noise to a connectivity diagram.
  const linkedContainers = new Set(links.map((l) => l.containerId));

  for (const n of networks) {
    g.setNode(`n:${n.id}`, { width: NET_W, height: NET_H });
  }
  for (const c of containers) {
    if (linkedContainers.has(c.id)) g.setNode(`c:${c.id}`, { width: CON_W, height: CON_H });
  }
  for (const l of links) {
    if (linkedContainers.has(l.containerId)) g.setEdge(`n:${l.networkId}`, `c:${l.containerId}`);
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
    .filter((l) => linkedContainers.has(l.containerId))
    .map((l, i) => ({
      id: `e:${i}`,
      source: `n:${l.networkId}`,
      target: `c:${l.containerId}`,
      label: l.ipAddress || undefined,
      animated: true,
      style: { stroke: "#243047" },
      labelStyle: { fill: "#8b97ad", fontSize: 10 },
      labelBgStyle: { fill: "#0b0f17" },
    }));

  return { nodes, edges };
}

// ---- Page -------------------------------------------------------------------

export function Topology() {
  const [topo, setTopo] = useState<Topo | null>(null);
  const [nodes, setNodes, onNodesChange] = useNodesState<Node>([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([]);
  const navigate = useNavigate();

  useEffect(() => {
    api.topology().then(setTopo).catch(() => setTopo({ networks: [], containers: [], links: [] }));
  }, []);

  // Compute the dagre layout once per topology load, then hand the nodes to
  // React Flow's state so the user can freely drag them around afterwards.
  useEffect(() => {
    if (!topo) return;
    const { nodes: n, edges: e } = layout(topo);
    setNodes(n);
    setEdges(e);
  }, [topo, setNodes, setEdges]);

  const onNodeClick = useCallback(
    (_: unknown, node: Node) => {
      if (node.type === "container") {
        const cid = (node.data as { cid: string }).cid;
        navigate(`/containers/${cid}`);
      }
    },
    [navigate]
  );

  return (
    <>
      <PageHeader title="Topology" />
      <div className="p-6">
        {!topo ? (
          <div className="flex items-center gap-2 text-muted"><Spinner /> Building graph…</div>
        ) : nodes.length === 0 ? (
          <EmptyState title="Nothing to show" hint="No networks with attached containers were found." />
        ) : (
          <div className="card overflow-hidden" style={{ height: "calc(100vh - 9rem)" }}>
            <ReactFlow
              nodes={nodes}
              edges={edges}
              onNodesChange={onNodesChange}
              onEdgesChange={onEdgesChange}
              nodeTypes={nodeTypes}
              onNodeClick={onNodeClick}
              fitView
              proOptions={{ hideAttribution: true }}
              minZoom={0.2}
            >
              <Background variant={BackgroundVariant.Dots} gap={20} size={1} color="#243047" />
              <Controls className="!bg-panel2 !border-border" />
              <MiniMap
                pannable
                zoomable
                className="!bg-panel2"
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
