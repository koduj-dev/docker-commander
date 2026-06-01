import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Boxes, Network as NetworkIcon, X } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { NetworkSummary, Topology } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";
import { shortId } from "../lib/format";

export function Networks() {
  const [nets, setNets] = useState<NetworkSummary[] | null>(null);
  const [topo, setTopo] = useState<Topology | null>(null);
  const [active, setActive] = useState<NetworkSummary | null>(null);

  useEffect(() => {
    api.networks().then(setNets).catch(() => setNets([]));
    api.topology().then(setTopo).catch(() => setTopo(null));
  }, []);

  return (
    <>
      <PageHeader title="Networks" />
      <div className="p-6">
        {!nets ? (
          <div className="flex items-center gap-2 text-muted"><Spinner /> Loading…</div>
        ) : nets.length === 0 ? (
          <EmptyState title="No networks found" />
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
            {nets.map((n) => (
              <button
                key={n.id}
                onClick={() => setActive(n)}
                className="card p-4 text-left hover:border-accent/50 transition-colors"
              >
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2 font-medium">
                    <NetworkIcon className="h-4 w-4 text-accent" /> {n.name}
                  </div>
                  <span className="text-xs bg-panel2 rounded px-2 py-0.5 text-muted">{n.driver}</span>
                </div>
                <div className="text-xs text-muted font-mono mt-1">{shortId(n.id)}</div>
                <div className="mt-3 space-y-1 text-sm">
                  <Row k="Scope" v={n.scope + (n.internal ? " · internal" : "")} />
                  <Row k="Subnets" v={(n.subnets ?? []).join(", ") || "—"} mono />
                  <Row k="Containers" v={String((n.containers ?? []).length)} />
                </div>
                <div className="text-xs text-accent mt-3">View topology →</div>
              </button>
            ))}
          </div>
        )}
      </div>
      {active && <NetworkModal net={active} topo={topo} onClose={() => setActive(null)} />}
    </>
  );
}

function Row({ k, v, mono }: { k: string; v: string; mono?: boolean }) {
  return (
    <div className="flex justify-between gap-3">
      <span className="text-muted">{k}</span>
      <span className={clsx("text-right truncate", mono && "font-mono text-xs")}>{v}</span>
    </div>
  );
}

// NetworkModal renders a star graph: the network in the centre with its attached
// containers around it, joined by edges labelled with the container's IP.
function NetworkModal({ net, topo, onClose }: { net: NetworkSummary; topo: Topology | null; onClose: () => void }) {
  const members = useMemo(() => {
    if (!topo) return [];
    const byId = new Map((topo.containers ?? []).map((c) => [c.id, c]));
    return (topo.links ?? [])
      .filter((l) => l.networkId === net.id)
      .map((l) => {
        const c = byId.get(l.containerId);
        return c ? { id: c.id, name: c.name, state: c.state, ip: l.ipAddress } : null;
      })
      .filter(Boolean) as { id: string; name: string; state: string; ip: string }[];
  }, [topo, net.id]);

  // Star layout geometry.
  const W = 720, H = 460, cx = W / 2, cy = H / 2;
  const r = Math.min(180, 90 + members.length * 8);
  const CARD_W = 150, CARD_H = 46;

  const positions = members.map((_, i) => {
    const angle = (i / Math.max(members.length, 1)) * Math.PI * 2 - Math.PI / 2;
    return { x: cx + r * Math.cos(angle), y: cy + r * Math.sin(angle) };
  });

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-black/60 p-4" onClick={onClose}>
      <div className="card w-full max-w-3xl" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center justify-between px-5 h-14 border-b border-border">
          <div className="flex items-center gap-2 font-semibold">
            <NetworkIcon className="h-4 w-4 text-accent" />
            {net.name}
            <span className="text-xs text-muted font-normal">
              {net.driver} · {(net.subnets ?? []).join(", ") || "no subnet"} · {members.length} containers
            </span>
          </div>
          <button className="btn-ghost px-2 py-1.5" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>

        <div className="p-4">
          {!topo ? (
            <div className="flex items-center gap-2 text-muted py-10 justify-center"><Spinner /> Loading…</div>
          ) : members.length === 0 ? (
            <EmptyState title="No containers attached" hint="This network has no connected containers." />
          ) : (
            <div className="relative mx-auto" style={{ width: W, height: H, maxWidth: "100%" }}>
              {/* edges */}
              <svg className="absolute inset-0" width={W} height={H}>
                {positions.map((p, i) => (
                  <g key={i}>
                    <line x1={cx} y1={cy} x2={p.x} y2={p.y} stroke="#243047" strokeWidth={2} />
                    <text
                      x={(cx + p.x) / 2}
                      y={(cy + p.y) / 2 - 4}
                      fill="#8b97ad"
                      fontSize={10}
                      textAnchor="middle"
                    >
                      {members[i].ip}
                    </text>
                  </g>
                ))}
              </svg>

              {/* centre: the network */}
              <div
                className="absolute rounded-xl border border-accent/50 bg-accent/15 grid place-items-center text-center"
                style={{ width: 130, height: 56, left: cx - 65, top: cy - 28 }}
              >
                <div>
                  <div className="text-sm font-semibold flex items-center gap-1.5 justify-center">
                    <NetworkIcon className="h-3.5 w-3.5 text-accent" /> {net.name}
                  </div>
                  <div className="text-[10px] text-muted">{net.driver}</div>
                </div>
              </div>

              {/* containers around the ring */}
              {members.map((m, i) => (
                <Link
                  key={m.id}
                  to={`/containers/${m.id}`}
                  className="absolute rounded-lg border bg-panel px-2.5 py-1.5 hover:border-accent/60 transition-colors"
                  style={{
                    width: CARD_W, height: CARD_H,
                    left: positions[i].x - CARD_W / 2, top: positions[i].y - CARD_H / 2,
                    borderColor: m.state === "running" ? "rgba(45,212,167,0.4)" : "#243047",
                  }}
                  title={`${m.name} (${m.ip})`}
                >
                  <div className="flex items-center gap-1.5">
                    <span className={clsx("h-2 w-2 rounded-full shrink-0", m.state === "running" ? "bg-ok" : m.state === "paused" ? "bg-warn" : "bg-danger")} />
                    <Boxes className="h-3.5 w-3.5 text-muted shrink-0" />
                    <span className="text-xs font-medium truncate">{m.name}</span>
                  </div>
                  <div className="text-[10px] text-muted font-mono truncate">{m.ip || "—"}</div>
                </Link>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
