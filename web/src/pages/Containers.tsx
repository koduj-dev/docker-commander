import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { Play, Plus, RotateCw, Square, Pause, Zap, X, Check, Download, Loader2 } from "lucide-react";
import { api } from "../lib/api";
import type { BulkActionResult, BulkPullFrame, ContainerSummary, PullProgress } from "../lib/types";
import { shortId } from "../lib/format";
import { StateBadge, EmptyState, Spinner } from "../components/ui";
import { PageHeader } from "../layout/Shell";
import { useListControls, SearchBar, Pager, type StatusOption } from "../components/ListControls";
import { CreateContainerModal } from "../components/CreateContainerModal";
import { useDialogs } from "../components/Dialog";
import { LayerRow } from "./Images";

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
  const dialogs = useDialogs();
  const [list, setList] = useState<ContainerSummary[] | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  // Bulk select is gated behind withControls (the standalone Containers page)
  // so embeds like the dashboard's ContainerTable (withControls=false) never
  // grow checkboxes or a bulk toolbar they weren't asked for.
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [bulkBusy, setBulkBusy] = useState(false);
  // Set while a bulk pull's progress modal is open; null otherwise.
  const [pullTargets, setPullTargets] = useState<ContainerSummary[] | null>(null);

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

  // Drop selected ids that no longer exist (removed/renamed away) so a stale
  // selection can't be bulk-acted on.
  useEffect(() => {
    if (!list) return;
    setSelected((prev) => {
      const ids = new Set(list.map((c) => c.id));
      const next = new Set([...prev].filter((id) => ids.has(id)));
      return next.size === prev.size ? prev : next;
    });
  }, [list]);

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

  const toggleOne = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  // Bulk restart/stop/start: preview exactly which containers are targeted
  // inside the confirm dialog (never a one-click bulk action — matches every
  // other destructive-ish action in this app), then fire the request and show
  // a per-container success/failure summary rather than a single toast.
  const bulkAct = async (action: "restart" | "stop" | "start") => {
    const targets = (list ?? []).filter((c) => selected.has(c.id));
    if (targets.length === 0) return;
    const label = action === "restart" ? "Restart" : action === "stop" ? "Stop" : "Start";
    const ok = await dialogs.confirm({
      title: `${label} ${targets.length} container${targets.length === 1 ? "" : "s"}?`,
      message: (
        <div className="space-y-2">
          <div>This will {action} the following container{targets.length === 1 ? "" : "s"}:</div>
          <ul className="max-h-48 overflow-y-auto space-y-0.5 text-sm font-mono bg-panel2/50 rounded-md p-2">
            {targets.map((c) => <li key={c.id}>{c.name}</li>)}
          </ul>
        </div>
      ),
      danger: action === "stop",
      confirmLabel: label,
    });
    if (!ok) return;

    setBulkBusy(true);
    try {
      const resp = await api.bulkContainerAction(targets.map((c) => c.id), action);
      const nameFor = (id: string) => targets.find((c) => c.id === id)?.name ?? shortId(id);
      await dialogs.alert({
        title: `${label}: ${resp.succeeded} succeeded, ${resp.failed} failed`,
        message: <BulkResultList results={resp.results} nameFor={nameFor} />,
      });
      setSelected(new Set());
      await load();
    } finally {
      setBulkBusy(false);
    }
  };

  // Bulk pull: preview the DISTINCT images behind the selection (several
  // containers often share one image), then hand the ids to BulkPullModal,
  // which opens the WebSocket and owns the rest of the flow. Containers are
  // not restarted or recreated — this only downloads the new image, same as
  // pulling one image on the Images page.
  const bulkPull = async () => {
    const targets = (list ?? []).filter((c) => selected.has(c.id));
    if (targets.length === 0) return;
    const images = [...new Set(targets.map((c) => c.image))];
    const ok = await dialogs.confirm({
      title: `Pull ${images.length} image${images.length === 1 ? "" : "s"}?`,
      message: (
        <div className="space-y-2">
          <div>
            This will pull the current image for {targets.length} selected container{targets.length === 1 ? "" : "s"}
            {images.length !== targets.length ? ` (${images.length} distinct image${images.length === 1 ? "" : "s"})` : ""}:
          </div>
          <ul className="max-h-48 overflow-y-auto space-y-0.5 text-sm font-mono bg-panel2/50 rounded-md p-2">
            {images.map((img) => <li key={img}>{img}</li>)}
          </ul>
          <div className="text-xs text-muted">Containers are not restarted or recreated — this only downloads the image.</div>
        </div>
      ),
      confirmLabel: "Pull",
    });
    if (!ok) return;
    setPullTargets(targets);
  };

  // Kill is SIGKILL: no shutdown handler runs, nothing is flushed. Stop asks
  // politely first and waits, so the two are not interchangeable and the
  // difference is exactly what a confirmation is for. Through the app's own
  // dialog, like every other destructive action here.
  const kill = async (c: ContainerSummary) => {
    const ok = await dialogs.confirm({
      title: `Kill ${c.name}?`,
      message: "SIGKILL, immediately — the process gets no chance to shut down cleanly or "
        + "flush anything in flight. Use Stop unless it is already unresponsive.",
      danger: true,
      confirmLabel: "Kill",
    });
    if (ok) await act(c.id, "kill");
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
  const allVisibleSelected = withControls && rows.length > 0 && rows.every((c) => selected.has(c.id));
  const toggleAllVisible = () => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (allVisibleSelected) rows.forEach((c) => next.delete(c.id));
      else rows.forEach((c) => next.add(c.id));
      return next;
    });
  };

  return (
   <div className="space-y-3">
    {withControls && <SearchBar controls={controls} placeholder="Search containers by name, image, id, state…" />}
    {withControls && selected.size > 0 && (
      <div className="card p-2 flex items-center gap-2">
        <span className="text-sm text-muted pl-1">{selected.size} selected</span>
        <button className="btn-ghost px-2 py-1.5 text-sm ml-auto" disabled={bulkBusy} onClick={() => setSelected(new Set())}>
          Clear
        </button>
        <button className="btn-ghost px-2 py-1.5 text-sm" disabled={bulkBusy} onClick={() => bulkAct("start")}>
          {bulkBusy ? <Spinner className="h-3.5 w-3.5" /> : <Play className="h-3.5 w-3.5" />} Start
        </button>
        <button className="btn-ghost px-2 py-1.5 text-sm" disabled={bulkBusy} onClick={() => bulkAct("restart")}>
          {bulkBusy ? <Spinner className="h-3.5 w-3.5" /> : <RotateCw className="h-3.5 w-3.5" />} Restart
        </button>
        <button className="btn-ghost px-2 py-1.5 text-sm text-danger hover:bg-danger/15" disabled={bulkBusy} onClick={() => bulkAct("stop")}>
          {bulkBusy ? <Spinner className="h-3.5 w-3.5" /> : <Square className="h-3.5 w-3.5" />} Stop
        </button>
        <button className="btn-ghost px-2 py-1.5 text-sm" disabled={bulkBusy} onClick={bulkPull}>
          <Download className="h-3.5 w-3.5" /> Pull
        </button>
      </div>
    )}
    <div className="card overflow-hidden">
      <table className="w-full text-sm">
        <thead className="text-muted text-xs uppercase tracking-wide">
          <tr className="border-b border-border">
            {withControls && (
              <th className="text-left px-4 py-3 w-8">
                <input
                  type="checkbox"
                  checked={allVisibleSelected}
                  onChange={toggleAllVisible}
                  aria-label="Select all containers"
                />
              </th>
            )}
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
                {withControls && (
                  <td className="px-4 py-3">
                    <input
                      type="checkbox"
                      checked={selected.has(c.id)}
                      disabled={bulkBusy}
                      onChange={() => toggleOne(c.id)}
                      aria-label={`Select ${c.name}`}
                    />
                  </td>
                )}
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
                        <span key={i} className="text-xs font-mono bg-panel2 rounded-sm px-1.5 py-0.5">
                          {p.publicPort}:{p.privatePort}
                        </span>
                      ))}
                  </div>
                </td>
                <td className="px-4 py-3 hidden xl:table-cell">
                  <div className="flex flex-wrap gap-1">
                    {(c.networks ?? []).map((n) => (
                      <span key={n} className="text-xs bg-panel2 rounded-sm px-1.5 py-0.5 text-muted">{n}</span>
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
                        <IconBtn title="Kill (SIGKILL)" danger onClick={() => kill(c)}><Zap className="h-4 w-4" /></IconBtn>
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
    {pullTargets && (
      <BulkPullModal
        ids={pullTargets.map((c) => c.id)}
        nameFor={(id) => pullTargets.find((c) => c.id === id)?.name ?? shortId(id)}
        onClose={() => setPullTargets(null)}
      />
    )}
   </div>
  );
}

// BulkResultList is the post-action summary: one line per container, ok or
// failed (with its error), instead of a single pass/fail toast — so a bulk
// action across several containers still tells you exactly which ones need a
// second look.
function BulkResultList({ results, nameFor }: { results: BulkActionResult[]; nameFor: (id: string) => string }) {
  return (
    <ul className="max-h-64 overflow-y-auto space-y-1 text-sm">
      {results.map((r) => (
        <li key={r.id} className="flex items-start gap-1.5">
          {r.ok ? <Check className="h-4 w-4 text-accent shrink-0 mt-0.5" /> : <X className="h-4 w-4 text-danger shrink-0 mt-0.5" />}
          <span>
            <span className={r.ok ? "" : "text-danger"}>{nameFor(r.id)}</span>
            {!r.ok && r.error && <span className="text-muted"> — {r.error}</span>}
          </span>
        </li>
      ))}
    </ul>
  );
}

// BulkPullModal owns the /containers/bulk-pull WebSocket for a set of
// container ids: one card per distinct image, with the same per-layer
// progress rows as the Images page's single pull, and a final per-container
// success/failure summary once every image is done.
function BulkPullModal({ ids, nameFor, onClose }: { ids: string[]; nameFor: (id: string) => string; onClose: () => void }) {
  const [frames, setFrames] = useState<Map<string, BulkPullFrame>>(new Map());
  const [layers, setLayers] = useState<Map<string, Map<string, PullProgress>>>(new Map());
  const [refOrder, setRefOrder] = useState<string[]>([]);
  const [total, setTotal] = useState(0);
  const [finished, setFinished] = useState(false);
  const [results, setResults] = useState<BulkPullFrame["results"]>(undefined);
  const [connError, setConnError] = useState("");
  const wsRef = useRef<WebSocket | null>(null);
  const doneRef = useRef(false);

  useEffect(() => {
    const ws = new WebSocket(api.bulkPullImagesUrl(ids));
    wsRef.current = ws;
    ws.onmessage = (ev) => {
      let f: BulkPullFrame;
      try {
        f = JSON.parse(ev.data as string);
      } catch {
        return;
      }
      if (f.allDone) {
        doneRef.current = true;
        setFinished(true);
        setResults(f.results);
        return;
      }
      if (f.count) setTotal(f.count);
      setFrames((prev) => {
        const next = new Map(prev);
        next.set(f.ref, f);
        return next;
      });
      setRefOrder((prev) => (prev.includes(f.ref) ? prev : [...prev, f.ref]));
      if (f.progress?.id) {
        setLayers((prev) => {
          const next = new Map(prev);
          const forRef = new Map(next.get(f.ref) ?? []);
          forRef.set(f.progress!.id!, f.progress!);
          next.set(f.ref, forRef);
          return next;
        });
      }
    };
    ws.onerror = () => setConnError("connection failed");
    ws.onclose = () => {
      setFinished(true);
      if (!doneRef.current) setConnError((prev) => prev || "connection closed before finishing");
    };
    return () => ws.close();
    // ids identifies one bulk-pull run; it does not change while this modal is open.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ids.join(",")]);

  const cancel = () => wsRef.current?.close();
  const flatResults: BulkActionResult[] = (results ?? []).flatMap((r) => r.containerIds.map((id) => ({ id, ok: r.ok, error: r.error })));

  return (
    <div className="fixed inset-0 z-[55] bg-black/60 grid place-items-center p-6" onClick={finished ? onClose : undefined}>
      <div className="card w-[70vw] max-h-[80vh] flex flex-col" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <Download className="h-4 w-4 text-accent" />
          <div className="font-medium">
            {total > 0
              ? `Pulling ${total} image${total === 1 ? "" : "s"}`
              : "Connecting…"}
          </div>
          {finished ? (
            <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
          ) : (
            <button type="button" className="btn-ghost px-2 py-1.5 ml-auto text-sm" onClick={cancel}><X className="h-4 w-4" /> Cancel</button>
          )}
        </div>
        <div className="p-4 space-y-3 overflow-y-auto">
          {connError && <div className="text-danger text-sm">{connError}</div>}
          {refOrder.map((ref) => {
            const f = frames.get(ref);
            const ls = [...(layers.get(ref)?.values() ?? [])];
            return (
              <div key={ref} className="rounded-md bg-panel2 p-3 text-xs font-mono space-y-1.5">
                <div className="flex items-center gap-2 text-text">
                  {f?.refDone ? (
                    f.ok ? <Check className="h-3.5 w-3.5 text-accent shrink-0" /> : <X className="h-3.5 w-3.5 text-danger shrink-0" />
                  ) : (
                    <Loader2 className="h-3.5 w-3.5 animate-spin shrink-0" />
                  )}
                  <span className="font-medium truncate">{ref}</span>
                  {f?.index && f?.count && <span className="text-muted shrink-0">({f.index}/{f.count})</span>}
                </div>
                {f?.error && <div className="text-danger">{f.error}</div>}
                {ls.map((l) => <LayerRow key={l.id} p={l} />)}
              </div>
            );
          })}
        </div>
        {finished && results && (
          <div className="p-4 border-t border-border">
            <BulkResultList results={flatResults} nameFor={nameFor} />
          </div>
        )}
      </div>
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
