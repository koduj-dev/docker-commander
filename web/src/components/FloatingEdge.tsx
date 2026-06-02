import { getBezierPath, useInternalNode, type EdgeProps, type InternalNode, type Node } from "@xyflow/react";

// Floating edges anchor to the *boundary* of each node along the line joining
// the two node centres, instead of to a fixed Left/Right handle. That keeps the
// connections tidy no matter where the user drags a node (the default fixed
// handles produce the "spider" tangle when a node is moved past its peer).

function nodeCenter(node: InternalNode<Node>) {
  return {
    x: node.internals.positionAbsolute.x + (node.measured.width ?? 0) / 2,
    y: node.internals.positionAbsolute.y + (node.measured.height ?? 0) / 2,
  };
}

// Intersection of the line (other → node centre) with node's rectangle border.
function intersection(node: InternalNode<Node>, other: InternalNode<Node>) {
  const w = (node.measured.width ?? 0) / 2;
  const h = (node.measured.height ?? 0) / 2;
  const c = nodeCenter(node);
  const o = nodeCenter(other);

  const dx = o.x - c.x;
  const dy = o.y - c.y;
  if (dx === 0 && dy === 0) return c;

  // Scale the direction vector so it just touches the nearest rectangle edge.
  const scale = 1 / Math.max(Math.abs(dx) / w, Math.abs(dy) / h);
  return { x: c.x + dx * scale, y: c.y + dy * scale };
}

export function FloatingEdge({ id, source, target, markerEnd, style }: EdgeProps) {
  const sourceNode = useInternalNode(source);
  const targetNode = useInternalNode(target);
  if (!sourceNode || !targetNode) return null;

  const s = intersection(sourceNode, targetNode);
  const t = intersection(targetNode, sourceNode);

  const [path] = getBezierPath({ sourceX: s.x, sourceY: s.y, targetX: t.x, targetY: t.y });

  return <path id={id} className="react-flow__edge-path" d={path} markerEnd={markerEnd} style={style} />;
}
