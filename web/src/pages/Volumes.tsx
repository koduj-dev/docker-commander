import { useCallback, useEffect, useState } from "react";
import { Database, Trash2, Loader2, Eraser, FileSearch, Plus, Boxes, FolderOpen, X } from "lucide-react";
import { api, fileApiForVolume } from "../lib/api";
import type { VolumeSummary } from "../lib/types";
import { relTime } from "../lib/format";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";
import { InspectModal } from "../components/InspectModal";
import { FileBrowser } from "../components/FileBrowser";
import { useListControls, SearchBar, Pager, type StatusOption } from "../components/ListControls";

const VOLUME_STATUSES: StatusOption<VolumeSummary>[] = [
  { value: "all", label: "All volumes" },
  { value: "used", label: "In use", test: (v) => (v.inUseBy ?? []).length > 0 },
  { value: "unused", label: "Unused", test: (v) => (v.inUseBy ?? []).length === 0 },
];

function matchVolume(v: VolumeSummary, q: string): boolean {
  return v.name.toLowerCase().includes(q) || v.driver.toLowerCase().includes(q) || (v.mountpoint ?? "").toLowerCase().includes(q);
}

function parseCreated(s: string): number {
  const ms = Date.parse(s);
  return Number.isNaN(ms) ? 0 : ms / 1000;
}

export function Volumes() {
  const [vols, setVols] = useState<VolumeSummary[] | null>(null);
  const [busy, setBusy] = useState<Record<string, boolean>>({});
  const [err, setErr] = useState<Record<string, string>>({});
  const [notice, setNotice] = useState("");
  const [pruning, setPruning] = useState(false);
  const [inspect, setInspect] = useState<VolumeSummary | null>(null);
  const [browse, setBrowse] = useState<VolumeSummary | null>(null);
  const [showForm, setShowForm] = useState(false);

  // Closing the browser also tears down the helper container DC spun up.
  const closeBrowse = () => {
    if (browse) api.closeVolumeBrowser(browse.name).catch(() => {});
    setBrowse(null);
  };

  const load = useCallback(() => {
    api.volumes().then(setVols).catch(() => setVols([]));
  }, []);
  useEffect(() => load(), [load]);

  const controls = useListControls(vols ?? [], matchVolume, { storageKey: "volumes", statuses: VOLUME_STATUSES });

  const remove = async (v: VolumeSummary, force = false) => {
    setBusy((b) => ({ ...b, [v.name]: true }));
    setErr((e) => ({ ...e, [v.name]: "" }));
    try {
      const r = await api.deleteVolume(v.name, force);
      if (r.ok) load();
      else setErr((e) => ({ ...e, [v.name]: r.error ?? "could not remove volume" }));
    } catch {
      setErr((e) => ({ ...e, [v.name]: "request failed" }));
    } finally {
      setBusy((b) => ({ ...b, [v.name]: false }));
    }
  };

  const prune = async () => {
    setPruning(true);
    setNotice("");
    try {
      const r = await api.pruneVolumes();
      const n = r.deleted?.length ?? 0;
      setNotice(`Pruned ${n} volume${n === 1 ? "" : "s"}.`);
      load();
    } catch {
      setNotice("Prune failed");
    } finally {
      setPruning(false);
    }
  };

  if (!vols) return (<><PageHeader title="Volumes" /><div className="p-6 flex items-center gap-2 text-muted"><Spinner /> Loading…</div></>);

  return (
    <>
      <PageHeader
        title="Volumes"
        actions={
          <>
            <button className="btn-ghost" onClick={() => setShowForm((v) => !v)}><Plus className="h-4 w-4" /> Create</button>
            <button className="btn-ghost" onClick={prune} disabled={pruning} title="Remove all unused volumes">
              {pruning ? <Loader2 className="h-4 w-4 animate-spin" /> : <Eraser className="h-4 w-4" />} Prune unused
            </button>
          </>
        }
      />
      <div className="p-6 space-y-4">
        {showForm && <VolumeForm onDone={() => { setShowForm(false); load(); }} />}
        {notice && <div className="text-xs text-muted">{notice}</div>}
        {vols.length === 0 ? (
          <EmptyState title="No volumes" hint="Create one above or pull a stack that declares volumes." />
        ) : (
          <>
            <SearchBar controls={controls} placeholder="Search volumes by name, driver, mountpoint…" />
            <div className="card divide-y divide-border">
              {controls.pageItems.map((v) => {
                const e = err[v.name];
                const inUse = (v.inUseBy ?? []).length > 0;
                return (
                  <div key={v.name} className="p-4 flex items-start gap-4">
                    <Database className="h-5 w-5 text-accent shrink-0 mt-0.5" />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2 flex-wrap">
                        <span className="font-medium text-sm break-all">{v.name}</span>
                        <span className="text-[10px] bg-panel2 rounded-sm px-1.5 py-0.5 text-muted">{v.driver}</span>
                        {inUse && (
                          <span className="text-[10px] bg-ok/15 text-ok rounded-sm px-1.5 py-0.5 inline-flex items-center gap-1">
                            <Boxes className="h-3 w-3" /> in use by {(v.inUseBy ?? []).join(", ")}
                          </span>
                        )}
                      </div>
                      <div className="text-xs text-muted font-mono mt-1 break-all">{v.mountpoint}</div>
                      {v.createdAt && <div className="text-xs text-muted mt-0.5">{relTime(parseCreated(v.createdAt))}</div>}
                      {e && (
                        <div className="mt-2 text-xs text-danger flex items-center gap-2 flex-wrap">
                          <span className="break-all">{e}</span>
                          <button className="btn-ghost px-2 py-0.5 text-danger border border-danger/40" onClick={() => remove(v, true)}>Force remove</button>
                        </div>
                      )}
                    </div>
                    <div className="flex items-center gap-1 shrink-0">
                      <button className="btn-ghost px-2 py-1" title="Browse files" onClick={() => setBrowse(v)}><FolderOpen className="h-4 w-4" /></button>
                      <button className="btn-ghost px-2 py-1" title="Inspect (raw JSON)" onClick={() => setInspect(v)}><FileSearch className="h-4 w-4" /></button>
                      <button className="btn-ghost px-2 py-1 text-danger" title="Remove volume" disabled={busy[v.name]} onClick={() => remove(v)}>
                        {busy[v.name] ? <Loader2 className="h-4 w-4 animate-spin" /> : <Trash2 className="h-4 w-4" />}
                      </button>
                    </div>
                  </div>
                );
              })}
            </div>
            <Pager controls={controls} />
          </>
        )}
      </div>
      {inspect && <InspectModal kind="volume" id={inspect.name} title={inspect.name} onClose={() => setInspect(null)} />}

      {browse && (
        <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-6" onClick={closeBrowse}>
          <div className="w-[80vw] max-h-[85vh] flex flex-col" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center gap-2 mb-2">
              <Database className="h-4 w-4 text-accent shrink-0" />
              <span className="font-medium break-all">{browse.name}</span>
              <span className="text-xs text-muted">— volume files</span>
              <button className="btn-ghost px-2 py-1.5 ml-auto" title="Close" onClick={closeBrowse}><X className="h-4 w-4" /></button>
            </div>
            <FileBrowser fs={fileApiForVolume(browse.name)} />
          </div>
        </div>
      )}
    </>
  );
}

function VolumeForm({ onDone }: { onDone: () => void }) {
  const [name, setName] = useState("");
  const [driver, setDriver] = useState("local");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr(""); setBusy(true);
    try {
      const r = await api.createVolume({ name, driver });
      if (r.ok) onDone();
      else setErr(r.error ?? "failed");
    } catch {
      setErr("request failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="card p-5 space-y-4">
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div>
          <label className="label">Name</label>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="my-data" required />
        </div>
        <div>
          <label className="label">Driver</label>
          <input className="input font-mono" value={driver} onChange={(e) => setDriver(e.target.value)} placeholder="local" />
        </div>
      </div>
      {err && <p className="text-sm text-danger">{err}</p>}
      <div className="flex justify-end gap-2">
        <button type="button" className="btn-ghost" onClick={onDone}>Cancel</button>
        <button className="btn-primary" disabled={busy || !name.trim()}>{busy ? "Creating…" : "Create volume"}</button>
      </div>
    </form>
  );
}
