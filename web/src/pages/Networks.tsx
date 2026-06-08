import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { FileSearch, Network as NetworkIcon, Trash2, X, Plus, Eraser, Loader2, Share2, List, Plug } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { NetworkSummary, Topology } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";
import { useDialogs } from "../components/Dialog";
import { InspectModal } from "../components/InspectModal";
import { TopoGraph, TopoList } from "../components/TopoGraph";
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
  const [showCreate, setShowCreate] = useState(false);
  const [pruning, setPruning] = useState(false);
  const [notice, setNotice] = useState("");
  const dialogs = useDialogs();

  const load = useCallback(() => {
    api.networks().then(setNets).catch(() => setNets([]));
    api.topology().then(setTopo).catch(() => setTopo(null));
  }, []);
  useEffect(() => load(), [load]);

  const controls = useListControls(nets ?? [], matchNetwork, { storageKey: "networks", statuses: NETWORK_STATUSES });

  const prune = async () => {
    if (!(await dialogs.confirm({ title: "Prune unused networks", message: "Remove every network not used by any container?", danger: true, confirmLabel: "Prune" }))) return;
    setPruning(true); setNotice("");
    try {
      const r = await api.pruneNetworks();
      const n = r.deleted?.length ?? 0;
      setNotice(`Pruned ${n} network${n === 1 ? "" : "s"}.`);
      load();
    } catch {
      setNotice("Prune failed");
    } finally {
      setPruning(false);
    }
  };

  return (
    <>
      <PageHeader
        title="Networks"
        actions={
          <>
            <button className="btn-ghost" onClick={() => setShowCreate(true)}><Plus className="h-4 w-4" /> Create</button>
            <button className="btn-ghost" onClick={prune} disabled={pruning} title="Remove all unused networks">
              {pruning ? <Loader2 className="h-4 w-4 animate-spin" /> : <Eraser className="h-4 w-4" />} Prune unused
            </button>
          </>
        }
      />
      <div className="p-6 space-y-4">
        {notice && <div className="text-xs text-muted">{notice}</div>}
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
      {showCreate && <NewNetworkModal onClose={() => setShowCreate(false)} onCreated={() => { setShowCreate(false); load(); }} />}
      {active && <NetworkModal net={active} topo={topo} onClose={() => setActive(null)} onChanged={() => { setActive(null); load(); }} onReload={load} />}
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

// NetworkModal shows a network's attached containers (graph or list, the same
// renderers as the Topology page) and lets you connect/disconnect/remove it.
function NetworkModal({ net, topo, onClose, onChanged, onReload }: { net: NetworkSummary; topo: Topology | null; onClose: () => void; onChanged: () => void; onReload: () => void }) {
  const [inspecting, setInspecting] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [delErr, setDelErr] = useState("");
  const [view, setView] = useState<"graph" | "list">("list");
  const [connectOpen, setConnectOpen] = useState(false);
  const [connectId, setConnectId] = useState("");
  const [busyConn, setBusyConn] = useState(false);
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

  const memberIds = useMemo(() => new Set(members.map((m) => m.id)), [members]);
  const available = useMemo(() => (topo?.containers ?? []).filter((c) => !memberIds.has(c.id)), [topo, memberIds]);

  const disconnect = async (cid: string, name: string) => {
    if (!(await dialogs.confirm({ title: "Disconnect container", message: <>Disconnect <code className="font-mono text-text">{name}</code> from <code className="font-mono text-text">{net.name}</code>?</>, danger: true, confirmLabel: "Disconnect" }))) return;
    try {
      const r = await api.disconnectNetwork(net.id, cid);
      if (!r.ok) setDelErr(r.error ?? "could not disconnect");
      else onReload();
    } catch { setDelErr("request failed"); }
  };

  const connect = async () => {
    if (!connectId) return;
    setBusyConn(true); setDelErr("");
    try {
      const r = await api.connectNetwork(net.id, connectId);
      if (!r.ok) setDelErr(r.error ?? "could not connect");
      else { setConnectOpen(false); setConnectId(""); onReload(); }
    } catch { setDelErr("request failed"); } finally { setBusyConn(false); }
  };

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
            {available.length > 0 && (
              <button className="btn-ghost px-2 py-1.5 text-xs" title="Connect a container to this network" onClick={() => setConnectOpen((v) => !v)}><Plug className="h-4 w-4" /> Connect</button>
            )}
            <div className="flex rounded-md border border-border overflow-hidden">
              <button className={clsx("px-2 py-1.5", view === "graph" ? "bg-accent/15 text-accent" : "text-muted hover:text-text")} title="Graph view" onClick={() => setView("graph")}><Share2 className="h-3.5 w-3.5" /></button>
              <button className={clsx("px-2 py-1.5", view === "list" ? "bg-accent/15 text-accent" : "text-muted hover:text-text")} title="List view" onClick={() => setView("list")}><List className="h-3.5 w-3.5" /></button>
            </div>
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
        {connectOpen && (
          <div className="flex items-center gap-2 px-5 py-2 border-b border-border">
            <Plug className="h-4 w-4 text-muted shrink-0" />
            <select className="input py-1.5 h-8 text-sm flex-1" value={connectId} onChange={(e) => setConnectId(e.target.value)}>
              <option value="">Select a container to connect…</option>
              {available.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
            </select>
            <button className="btn-primary px-3 h-8 text-sm disabled:opacity-40" disabled={!connectId || busyConn} onClick={connect}>
              {busyConn ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plug className="h-4 w-4" />} Connect
            </button>
          </div>
        )}
        {delErr && <div className="px-5 py-2 text-xs text-danger border-b border-border break-all">{delErr}</div>}
        {inspecting && <InspectModal kind="network" id={net.id} title={net.name} onClose={() => setInspecting(false)} />}

        <div className="p-4">
          {!topo ? (
            <div className="flex items-center gap-2 text-muted py-10 justify-center"><Spinner /> Loading…</div>
          ) : members.length === 0 ? (
            <EmptyState title="No containers attached" hint="Use Connect to attach a container to this network." />
          ) : view === "list" ? (
            <TopoList topo={subTopo} filters={{ hideEmptyNetworks: false, showStopped: true, search: "", stack: "" }} onOpen={(id) => navigate(`/containers/${id}`)} onDisconnect={disconnect} />
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

// NewNetworkModal creates a user-defined network.
function NewNetworkModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState("");
  const [driver, setDriver] = useState("bridge");
  const [subnet, setSubnet] = useState("");
  const [gateway, setGateway] = useState("");
  const [internal, setInternal] = useState(false);
  const [attachable, setAttachable] = useState(true);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setErr(""); setBusy(true);
    try {
      const r = await api.createNetwork({ name: name.trim(), driver: driver.trim() || "bridge", subnet: subnet.trim(), gateway: gateway.trim(), internal, attachable });
      if (r.ok) onCreated();
      else { setErr(r.error ?? "could not create network"); setBusy(false); }
    } catch {
      setErr("request failed"); setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-[55] bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <form className="card w-full max-w-md flex flex-col" onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <NetworkIcon className="h-4 w-4 text-accent" />
          <div className="font-medium">Create network</div>
          <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 space-y-3">
          <label className="block"><span className="label">Name</span><input autoFocus className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="my-net" required /></label>
          <label className="block"><span className="label">Driver</span><input className="input font-mono" value={driver} onChange={(e) => setDriver(e.target.value)} placeholder="bridge" /></label>
          <div className="grid grid-cols-2 gap-3">
            <label className="block"><span className="label">Subnet (optional)</span><input className="input font-mono" value={subnet} onChange={(e) => setSubnet(e.target.value)} placeholder="172.28.0.0/16" /></label>
            <label className="block"><span className="label">Gateway (optional)</span><input className="input font-mono" value={gateway} onChange={(e) => setGateway(e.target.value)} placeholder="172.28.0.1" /></label>
          </div>
          <div className="flex items-center gap-5 text-sm pt-1">
            <label className="flex items-center gap-2 cursor-pointer"><input type="checkbox" checked={internal} onChange={(e) => setInternal(e.target.checked)} /> Internal</label>
            <label className="flex items-center gap-2 cursor-pointer"><input type="checkbox" checked={attachable} onChange={(e) => setAttachable(e.target.checked)} /> Attachable</label>
          </div>
          {err && <p className="text-sm text-danger break-all">{err}</p>}
        </div>
        <div className="flex justify-end gap-2 p-4 border-t border-border">
          <button type="button" className="btn-ghost px-3 py-1.5 text-sm" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn-primary px-3 py-1.5 text-sm disabled:opacity-40" disabled={!name.trim() || busy}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />} Create
          </button>
        </div>
      </form>
    </div>
  );
}
