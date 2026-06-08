import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { FileSearch, Network as NetworkIcon, Trash2, X } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { NetworkSummary, Topology } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";
import { useDialogs } from "../components/Dialog";
import { InspectModal } from "../components/InspectModal";
import { TopoGraph } from "../components/TopoGraph";
import { shortId } from "../lib/format";
import { useListControls, SearchBar, Pager, type StatusOption } from "../components/ListControls";

// Predefined networks the daemon won't let you remove.
const PREDEFINED = new Set(["bridge", "host", "none"]);

const NETWORK_STATUSES: StatusOption<NetworkSummary>[] = [
  { value: "all", label: "All networks" },
  { value: "used", label: "In use", test: (n) => (n.containers ?? []).length > 0 },
  { value: "unused", label: "Unused", test: (n) => (n.containers ?? []).length === 0 },
  { value: "internal", label: "Internal", test: (n) => n.internal },
];

function matchNetwork(n: NetworkSummary, q: string): boolean {
  return n.name.toLowerCase().includes(q) || n.driver.toLowerCase().includes(q) ||
    (n.subnets ?? []).some((s) => s.toLowerCase().includes(q)) || n.scope.toLowerCase().includes(q);
}

export function Networks() {
  const [nets, setNets] = useState<NetworkSummary[] | null>(null);
  const [topo, setTopo] = useState<Topology | null>(null);
  const [active, setActive] = useState<NetworkSummary | null>(null);

  const load = useCallback(() => {
    api.networks().then(setNets).catch(() => setNets([]));
    api.topology().then(setTopo).catch(() => setTopo(null));
  }, []);
  useEffect(() => load(), [load]);

  const controls = useListControls(nets ?? [], matchNetwork, { storageKey: "networks", statuses: NETWORK_STATUSES });

  return (
    <>
      <PageHeader title="Networks" />
      <div className="p-6 space-y-4">
        {!nets ? (
          <div className="flex items-center gap-2 text-muted"><Spinner /> Loading…</div>
        ) : nets.length === 0 ? (
          <EmptyState title="No networks found" />
        ) : (
          <>
            <SearchBar controls={controls} placeholder="Search networks by name, driver, subnet, scope…" />
            <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
            {controls.pageItems.map((n) => (
              <button
                key={n.id}
                onClick={() => setActive(n)}
                className="card p-4 text-left hover:border-accent/50 transition-colors"
              >
                <div className="flex items-center justify-between gap-2">
                  <div className="flex items-center gap-2 font-medium min-w-0">
                    <NetworkIcon className="h-4 w-4 text-accent shrink-0" /> <span className="truncate">{n.name}</span>
                  </div>
                  <div className="flex items-center gap-1 shrink-0">
                    {n.internal ? (
                      <span className="text-[10px] bg-warn/15 text-warn rounded-sm px-1.5 py-0.5">internal</span>
                    ) : (
                      <span className="text-[10px] bg-ok/15 text-ok rounded-sm px-1.5 py-0.5">external</span>
                    )}
                    <span className="text-xs bg-panel2 rounded-sm px-2 py-0.5 text-muted">{n.driver}</span>
                  </div>
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
            <Pager controls={controls} />
          </>
        )}
      </div>
      {active && <NetworkModal net={active} topo={topo} onClose={() => setActive(null)} onChanged={() => { setActive(null); load(); }} />}
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
function NetworkModal({ net, topo, onClose, onChanged }: { net: NetworkSummary; topo: Topology | null; onClose: () => void; onChanged: () => void }) {
  const [inspecting, setInspecting] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [delErr, setDelErr] = useState("");
  const isPredefined = PREDEFINED.has(net.name);
  const dialogs = useDialogs();
  const navigate = useNavigate();

  const del = async () => {
    if (!(await dialogs.confirm({ title: "Remove network", message: <>Remove the network <code className="font-mono text-text">{net.name}</code>?</>, danger: true, confirmLabel: "Remove" }))) return;
    setDeleting(true);
    setDelErr("");
    try {
      const r = await api.deleteNetwork(net.id);
      if (r.ok) onChanged();
      else setDelErr(r.error ?? "could not remove network");
    } catch {
      setDelErr("request failed");
    } finally {
      setDeleting(false);
    }
  };

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

  // A topology subset scoped to this network — fed to the same TopoGraph the
  // Topology page uses, so the detail view renders identically.
  const subTopo = useMemo<Topology>(() => {
    if (!topo) return { networks: [], containers: [], links: [] };
    const memberIds = new Set((topo.links ?? []).filter((l) => l.networkId === net.id).map((l) => l.containerId));
    return {
      networks: (topo.networks ?? []).filter((n) => n.id === net.id),
      containers: (topo.containers ?? []).filter((c) => memberIds.has(c.id)),
      links: (topo.links ?? []).filter((l) => l.networkId === net.id),
    };
  }, [topo, net.id]);

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-black/60 p-4" onClick={onClose}>
      <div className="card w-[92vw] max-w-[1400px]" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center justify-between px-5 h-14 border-b border-border">
          <div className="flex items-center gap-2 font-semibold">
            <NetworkIcon className="h-4 w-4 text-accent" />
            {net.name}
            <span className="text-xs text-muted font-normal">
              {net.driver} · {(net.subnets ?? []).join(", ") || "no subnet"} · {members.length} containers
            </span>
          </div>
          <div className="flex items-center gap-1">
            <button className="btn-ghost px-2 py-1.5" title="Inspect (raw JSON)" onClick={() => setInspecting(true)}><FileSearch className="h-4 w-4" /></button>
            {!isPredefined && (
              <button
                className="btn-ghost px-2 py-1.5 text-danger"
                title={members.length > 0 ? "Disconnect all containers first" : "Remove network"}
                disabled={deleting}
                onClick={del}
              >
                {deleting ? <Spinner className="h-4 w-4" /> : <Trash2 className="h-4 w-4" />}
              </button>
            )}
            <button className="btn-ghost px-2 py-1.5" onClick={onClose}><X className="h-4 w-4" /></button>
          </div>
        </div>
        {delErr && <div className="px-5 py-2 text-xs text-danger border-b border-border break-all">{delErr}</div>}
        {inspecting && <InspectModal kind="network" id={net.id} title={net.name} onClose={() => setInspecting(false)} />}

        <div className="p-4">
          {!topo ? (
            <div className="flex items-center gap-2 text-muted py-10 justify-center"><Spinner /> Loading…</div>
          ) : members.length === 0 ? (
            <EmptyState title="No containers attached" hint="This network has no connected containers." />
          ) : (
            <div className="dc-topo bg-bg rounded-lg border border-border overflow-hidden" style={{ height: "70vh" }}>
              <TopoGraph topo={subTopo} filters={{ hideEmptyNetworks: false, showStopped: true, search: "", stack: "" }} onContainerClick={(id) => navigate(`/containers/${id}`)} minimap={false} />
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
