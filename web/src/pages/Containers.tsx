import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Play, Plus, RotateCw, Square, Pause } from "lucide-react";
import { api } from "../lib/api";
import type { ContainerSummary } from "../lib/types";
import { shortId } from "../lib/format";
import { StateBadge, EmptyState, Spinner } from "../components/ui";
import { PageHeader } from "../layout/Shell";
import { useListControls, SearchBar, Pager, type StatusOption } from "../components/ListControls";
import { CreateContainerModal } from "../components/CreateContainerModal";

const CONTAINER_STATUSES: StatusOption<ContainerSummary>[] = [
  { value: "all", label: "All states" },
  { value: "running", label: "Running", test: (c) => c.state === "running" },
  { value: "stopped", label: "Stopped", test: (c) => c.state !== "running" },
];

function matchContainer(c: ContainerSummary, q: string): boolean {
  return (
    c.name.toLowerCase().includes(q) ||
    c.image.toLowerCase().includes(q) ||
    c.id.toLowerCase().includes(q) ||
    c.state.toLowerCase().includes(q) ||
    (c.status ?? "").toLowerCase().includes(q)
  );
}

// ContainerTable is shared by the dashboard and the dedicated Containers page.
// With runningOnly it hides stopped containers (handy on the dashboard when a
// host has many idle containers); withControls adds search + pagination.
export function ContainerTable({ runningOnly = false, withControls = false, refreshSignal = 0 }: { runningOnly?: boolean; withControls?: boolean; refreshSignal?: number }) {
  const [list, setList] = useState<ContainerSummary[] | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const all = await api.containers();
      setList(runningOnly ? all.filter((c) => c.state === "running") : all);
    } catch {
      setList([]);
    }
  }, [runningOnly]);

  // refreshSignal (Docker events) triggers an immediate reload on top of the poll.
  useEffect(() => {
    void load();
    const t = setInterval(load, 4000);
    return () => clearInterval(t);
  }, [load, refreshSignal]);

  const controls = useListControls(
    list ?? [],
    matchContainer,
    withControls ? { storageKey: "containers", statuses: CONTAINER_STATUSES } : {},
  );

  const act = async (id: string, action: string) => {
    setBusyId(id);
    try {
      await api.containerAction(id, action);
      await load();
    } finally {
      setBusyId(null);
    }
  };

  if (!list) return <div className="flex items-center gap-2 text-muted"><Spinner /> Loading…</div>;
  if (list.length === 0)
    return (
      <EmptyState
        title={runningOnly ? "No running containers" : "No containers found"}
        hint={runningOnly ? "Nothing is running on this host right now." : "Start a container on this host to see it here."}
      />
    );

  const rows = withControls ? controls.pageItems : list;

  return (
   <div className="space-y-3">
    {withControls && <SearchBar controls={controls} placeholder="Search containers by name, image, id, state…" />}
    <div className="card overflow-hidden">
      <table className="w-full text-sm">
        <thead className="text-muted text-xs uppercase tracking-wide">
          <tr className="border-b border-border">
            <th className="text-left font-medium px-4 py-3">Name</th>
            <th className="text-left font-medium px-4 py-3">State</th>
            <th className="text-left font-medium px-4 py-3 hidden lg:table-cell">Image</th>
            <th className="text-left font-medium px-4 py-3 hidden md:table-cell">Ports</th>
            <th className="text-left font-medium px-4 py-3 hidden xl:table-cell">Networks</th>
            <th className="text-right font-medium px-4 py-3">Actions</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((c) => {
            const running = c.state === "running";
            return (
              <tr key={c.id} className="border-b border-border/50 hover:bg-panel2/40">
                <td className="px-4 py-3">
                  <Link to={`/containers/${c.id}`} className="font-medium hover:text-accent">
                    {c.name}
                  </Link>
                  <div className="text-xs text-muted font-mono">{shortId(c.id)}</div>
                </td>
                <td className="px-4 py-3">
                  <StateBadge state={c.state} />
                  <div className="text-xs text-muted mt-0.5">{c.status}</div>
                </td>
                <td className="px-4 py-3 hidden lg:table-cell text-muted">{c.image}</td>
                <td className="px-4 py-3 hidden md:table-cell">
                  <div className="flex flex-wrap gap-1">
                    {(c.ports ?? [])
                      .filter((p) => p.publicPort)
                      .map((p, i) => (
                        <span key={i} className="text-xs font-mono bg-panel2 rounded px-1.5 py-0.5">
                          {p.publicPort}:{p.privatePort}
                        </span>
                      ))}
                  </div>
                </td>
                <td className="px-4 py-3 hidden xl:table-cell">
                  <div className="flex flex-wrap gap-1">
                    {(c.networks ?? []).map((n) => (
                      <span key={n} className="text-xs bg-panel2 rounded px-1.5 py-0.5 text-muted">{n}</span>
                    ))}
                  </div>
                </td>
                <td className="px-4 py-3">
                  <div className="flex items-center justify-end gap-1">
                    {busyId === c.id ? (
                      <Spinner className="h-4 w-4" />
                    ) : running ? (
                      <>
                        <IconBtn title="Restart" onClick={() => act(c.id, "restart")}><RotateCw className="h-4 w-4" /></IconBtn>
                        <IconBtn title="Pause" onClick={() => act(c.id, "pause")}><Pause className="h-4 w-4" /></IconBtn>
                        <IconBtn title="Stop" danger onClick={() => act(c.id, "stop")}><Square className="h-4 w-4" /></IconBtn>
                      </>
                    ) : c.state === "paused" ? (
                      <IconBtn title="Unpause" onClick={() => act(c.id, "unpause")}><Play className="h-4 w-4" /></IconBtn>
                    ) : (
                      <IconBtn title="Start" onClick={() => act(c.id, "start")}><Play className="h-4 w-4" /></IconBtn>
                    )}
                  </div>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
    {withControls && <Pager controls={controls} />}
   </div>
  );
}

function IconBtn({ children, onClick, title, danger }: { children: React.ReactNode; onClick: () => void; title: string; danger?: boolean }) {
  return (
    <button
      title={title}
      onClick={onClick}
      className={`p-1.5 rounded-md transition-colors ${danger ? "text-danger hover:bg-danger/15" : "text-muted hover:bg-panel2 hover:text-text"}`}
    >
      {children}
    </button>
  );
}

export function Containers() {
  const [showCreate, setShowCreate] = useState(false);
  const [reloadKey, setReloadKey] = useState(0);
  return (
    <>
      <PageHeader title="Containers" actions={<button className="btn-primary" onClick={() => setShowCreate(true)}><Plus className="h-4 w-4" /> Create container</button>} />
      <div className="p-6">
        <ContainerTable withControls key={reloadKey} />
      </div>
      {showCreate && <CreateContainerModal onClose={() => setShowCreate(false)} onDone={() => { setShowCreate(false); setReloadKey((k) => k + 1); }} />}
    </>
  );
}
