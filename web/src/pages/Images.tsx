import { useCallback, useEffect, useRef, useState } from "react";
import { Download, Trash2, Layers, Loader2, X, Boxes, Eraser, FileSearch, History, Upload, Hammer, HardDriveDownload, ShieldAlert } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { HistoryEntry, ImageSummary, PullProgress, ScanResult } from "../lib/types";
import { hostParam } from "../lib/host";
import { bytes, relTime, shortId } from "../lib/format";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";
import { InspectModal } from "../components/InspectModal";
import { PushModal } from "../components/PushModal";
import { BuildModal } from "../components/BuildModal";
import { LoadModal, triggerDownload } from "../components/LoadModal";
import { useDialogs } from "../components/Dialog";
import { useListControls, SearchBar, Pager, type StatusOption } from "../components/ListControls";

const IMAGE_STATUSES: StatusOption<ImageSummary>[] = [
  { value: "all", label: "All images" },
  { value: "used", label: "In use", test: (i) => i.inUse },
  { value: "unused", label: "Unused", test: (i) => !i.inUse },
];

function matchImage(img: ImageSummary, q: string): boolean {
  if ((img.repoTags ?? []).some((t) => t.toLowerCase().includes(q))) return true;
  if (img.id.toLowerCase().includes(q)) return true;
  if (q === "dangling" && img.dangling) return true;
  if ((q === "in use" || q === "inuse") && img.inUse) return true;
  return false;
}

export function Images() {
  const [images, setImages] = useState<ImageSummary[] | null>(null);
  const [busy, setBusy] = useState<Record<string, boolean>>({});
  const [err, setErr] = useState<Record<string, string>>({});
  const [notice, setNotice] = useState("");
  const [pruning, setPruning] = useState(false);
  const [inspect, setInspect] = useState<ImageSummary | null>(null);
  const [history, setHistory] = useState<ImageSummary | null>(null);
  const [push, setPush] = useState<ImageSummary | null>(null);
  const [scan, setScan] = useState<ImageSummary | null>(null);
  const [showBuild, setShowBuild] = useState(false);
  const [showLoad, setShowLoad] = useState(false);
  const dialogs = useDialogs();

  const load = useCallback(() => {
    api.images().then(setImages).catch(() => setImages([]));
  }, []);
  useEffect(() => load(), [load]);

  const controls = useListControls(images ?? [], matchImage, { storageKey: "images", statuses: IMAGE_STATUSES });

  const remove = async (img: ImageSummary, force = false) => {
    const label = (img.repoTags ?? []).find((t) => t && t !== "<none>:<none>") ?? shortId(img.id);
    if (!force && !(await dialogs.confirm({ title: "Remove image", message: <>Remove the image <code className="font-mono text-text">{label}</code>?</>, danger: true, confirmLabel: "Remove" }))) return;
    const id = img.id;
    setBusy((b) => ({ ...b, [id]: true }));
    setErr((e) => ({ ...e, [id]: "" }));
    try {
      const r = await api.removeImage(id, force);
      if (r.ok) {
        load();
      } else {
        setErr((e) => ({ ...e, [id]: r.error ?? "could not remove image" }));
      }
    } catch {
      setErr((e) => ({ ...e, [id]: "request failed" }));
    } finally {
      setBusy((b) => ({ ...b, [id]: false }));
    }
  };

  const prune = async () => {
    if (!(await dialogs.confirm({ title: "Prune images", message: "Remove all dangling (untagged) images?", danger: true, confirmLabel: "Prune" }))) return;
    setPruning(true);
    setNotice("");
    try {
      const r = await api.pruneImages();
      const n = r.deleted?.length ?? 0;
      setNotice(`Pruned ${n} image${n === 1 ? "" : "s"} · reclaimed ${bytes(r.spaceReclaimed)}`);
      load();
    } catch {
      setNotice("Prune failed");
    } finally {
      setPruning(false);
    }
  };

  if (!images) return (<><PageHeader title="Images" /><div className="p-6 flex items-center gap-2 text-muted"><Spinner /> Loading…</div></>);

  const danglingCount = images.filter((i) => i.dangling).length;

  return (
    <>
      <PageHeader
        title="Images"
        actions={
          <>
            <button className="btn-ghost" onClick={() => setShowLoad(true)} title="Load or import an image from a tar">
              <HardDriveDownload className="h-4 w-4" /> Load
            </button>
            <button className="btn-ghost" onClick={() => setShowBuild(true)} title="Build an image from a tar context">
              <Hammer className="h-4 w-4" /> Build
            </button>
            <button className="btn-ghost" onClick={prune} disabled={pruning || danglingCount === 0} title="Remove dangling (untagged) images">
              {pruning ? <Loader2 className="h-4 w-4 animate-spin" /> : <Eraser className="h-4 w-4" />}
              Prune {danglingCount > 0 ? `(${danglingCount})` : ""}
            </button>
          </>
        }
      />
      <div className="p-6 space-y-4">
        <PullPanel onPulled={load} />
        {notice && <div className="text-xs text-muted">{notice}</div>}
        {images.length === 0 ? (
          <EmptyState title="No images" hint="Pull one above to get started." />
        ) : (
          <>
          <SearchBar controls={controls} placeholder="Search images by tag or id…" />
          <div className="card divide-y divide-border">
            {controls.pageItems.map((img) => {
              const tags = (img.repoTags ?? []).filter((t) => t && t !== "<none>:<none>");
              const e = err[img.id];
              return (
                <div key={img.id} className="p-4 flex items-start gap-4">
                  <Layers className="h-5 w-5 text-accent shrink-0 mt-0.5" />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2 flex-wrap">
                      {tags.length > 0 ? (
                        tags.map((t) => (
                          <span key={t} className="font-medium text-sm break-all">{t}</span>
                        ))
                      ) : (
                        <span className="text-sm text-muted italic">&lt;none&gt;</span>
                      )}
                      {img.dangling && <span className="text-[10px] bg-warn/15 text-warn rounded-sm px-1.5 py-0.5">dangling</span>}
                      {img.inUse && <span className="text-[10px] bg-ok/15 text-ok rounded-sm px-1.5 py-0.5 inline-flex items-center gap-1"><Boxes className="h-3 w-3" />in use</span>}
                    </div>
                    <div className="text-xs text-muted font-mono mt-1">
                      {shortId(img.id)} · {bytes(img.size)} · {relTime(img.created)}
                    </div>
                    {e && (
                      <div className="mt-2 text-xs text-danger flex items-center gap-2 flex-wrap">
                        <span className="break-all">{e}</span>
                        <button className="btn-ghost px-2 py-0.5 text-danger border border-danger/40" onClick={() => remove(img, true)}>
                          Force remove
                        </button>
                      </div>
                    )}
                  </div>
                  <div className="flex items-center gap-1 shrink-0">
                    <button className="btn-ghost px-2 py-1" title="Save (download tar)" onClick={() => triggerDownload(api.saveImageUrl(tags[0] || img.id))}><Download className="h-4 w-4" /></button>
                    <button className="btn-ghost px-2 py-1" title="Push to registry" onClick={() => setPush(img)}><Upload className="h-4 w-4" /></button>
                    <button className="btn-ghost px-2 py-1" title="Scan for vulnerabilities" onClick={() => setScan(img)}><ShieldAlert className="h-4 w-4" /></button>
                    <button className="btn-ghost px-2 py-1" title="Layer history" onClick={() => setHistory(img)}><History className="h-4 w-4" /></button>
                    <button className="btn-ghost px-2 py-1" title="Inspect (raw JSON)" onClick={() => setInspect(img)}><FileSearch className="h-4 w-4" /></button>
                    <button
                      className="btn-ghost px-2 py-1 text-danger"
                      title="Remove image"
                      disabled={busy[img.id]}
                      onClick={() => remove(img)}
                    >
                      {busy[img.id] ? <Loader2 className="h-4 w-4 animate-spin" /> : <Trash2 className="h-4 w-4" />}
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
      {inspect && (
        <InspectModal kind="image" id={inspect.id} title={(inspect.repoTags ?? [])[0] || shortId(inspect.id)} onClose={() => setInspect(null)} />
      )}
      {history && (
        <ImageHistoryModal img={history} onClose={() => setHistory(null)} />
      )}
      {scan && (
        <ScanModal img={scan} onClose={() => setScan(null)} />
      )}
      {push && (
        <PushModal image={push} onClose={() => setPush(null)} onDone={load} />
      )}
      {showBuild && (
        <BuildModal onClose={() => setShowBuild(false)} onDone={load} />
      )}
      {showLoad && (
        <LoadModal onClose={() => setShowLoad(false)} onDone={load} />
      )}
    </>
  );
}

// ImageHistoryModal lists an image's build layers (docker history).
function ImageHistoryModal({ img, onClose }: { img: ImageSummary; onClose: () => void }) {
  const [layers, setLayers] = useState<HistoryEntry[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.imageHistory(img.id).then(setLayers).catch((e) => setError(e instanceof Error ? e.message : "failed"));
  }, [img.id]);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <div className="card w-full max-w-4xl max-h-[85vh] flex flex-col" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <div className="font-medium min-w-0">
            <span className="text-xs uppercase tracking-wide text-muted mr-2">history</span>
            <span className="break-all">{(img.repoTags ?? [])[0] || shortId(img.id)}</span>
          </div>
          <button className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="flex-1 min-h-0 overflow-auto p-4">
          {error ? (
            <div className="text-sm text-danger">{error}</div>
          ) : !layers ? (
            <div className="flex items-center gap-2 text-muted text-sm"><Spinner className="h-4 w-4" /> Loading…</div>
          ) : (
            <div className="space-y-2">
              {layers.map((l, i) => (
                <div key={i} className="text-xs border-b border-border/50 pb-2">
                  <div className="flex justify-between gap-3 text-muted">
                    <span className="font-mono">{l.id && l.id !== "<missing>" ? shortId(l.id) : "—"}</span>
                    <span>{bytes(l.size)} · {relTime(l.created)}</span>
                  </div>
                  <div className="font-mono break-all text-text/80 mt-0.5">{cleanLayerCmd(l.createdBy)}</div>
                  {l.tags && l.tags.length > 0 && <div className="text-accent mt-0.5">{l.tags.join(", ")}</div>}
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

// safeHttpUrl guards an advisory link from a scan result: only http(s) URLs are
// rendered as clickable, so a crafted `javascript:` URL from an untrusted image
// can't execute on click.
function safeHttpUrl(u?: string): boolean {
  return !!u && /^https?:\/\//i.test(u);
}

const SEVERITIES = ["CRITICAL", "HIGH", "MEDIUM", "LOW", "UNKNOWN"] as const;
const sevColor: Record<string, string> = {
  CRITICAL: "bg-red-500/20 text-red-300 border-red-500/40",
  HIGH: "bg-orange-500/20 text-orange-300 border-orange-500/40",
  MEDIUM: "bg-amber-500/20 text-amber-300 border-amber-500/40",
  LOW: "bg-sky-500/20 text-sky-300 border-sky-500/40",
  UNKNOWN: "bg-zinc-500/20 text-zinc-300 border-zinc-500/40",
};

// ScanModal runs and shows a Trivy vulnerability scan for an image.
function ScanModal({ img, onClose }: { img: ImageSummary; onClose: () => void }) {
  const ref = (img.repoTags ?? [])[0] || img.id;
  const [result, setResult] = useState<ScanResult | null>(null);
  const [state, setState] = useState<"scanning" | "done" | "error" | "unavailable">("scanning");
  const [error, setError] = useState("");
  useEffect(() => {
    let alive = true;
    api.scanImage(ref).then((r) => {
      if (!alive) return;
      if (!r.available) { setState("unavailable"); setError(r.error ?? ""); return; }
      if (!r.ok || !r.result) { setState("error"); setError(r.error ?? "scan failed"); return; }
      setResult(r.result); setState("done");
    }).catch((e) => { if (alive) { setState("error"); setError(e instanceof Error ? e.message : "scan failed"); } });
    return () => { alive = false; };
  }, [ref]);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const total = result ? SEVERITIES.reduce((n, s) => n + (result.summary[s] ?? 0), 0) : 0;
  return (
    <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <div className="card w-full max-w-4xl max-h-[85vh] flex flex-col" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <ShieldAlert className="h-4 w-4 text-accent" />
          <div className="font-medium min-w-0">
            <span className="text-xs uppercase tracking-wide text-muted mr-2">scan</span>
            <span className="break-all">{ref}</span>
          </div>
          <button className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="flex-1 min-h-0 overflow-auto p-4">
          {state === "scanning" && <div className="flex items-center gap-2 text-muted text-sm"><Spinner className="h-4 w-4" /> Scanning with Trivy… (the first scan also downloads the vulnerability database)</div>}
          {state === "unavailable" && (
            <div className="text-sm space-y-2">
              <p className="text-danger">Trivy isn't installed on the host running Docker Commander.</p>
              <p className="text-muted">Install it (e.g. <code className="font-mono">apt install trivy</code> or see trivy.dev) to enable image scanning.</p>
            </div>
          )}
          {state === "error" && <div className="text-sm text-danger break-all">{error}</div>}
          {state === "done" && result && (
            <div className="space-y-3">
              <div className="flex flex-wrap gap-2">
                {SEVERITIES.map((s) => (
                  <span key={s} className={clsx("text-xs px-2 py-1 rounded-md border", sevColor[s])}>{s} {result.summary[s] ?? 0}</span>
                ))}
              </div>
              {total === 0 ? (
                <div className="text-sm text-ok">No known vulnerabilities found. 🎉</div>
              ) : (
                <table className="w-full text-xs">
                  <thead className="text-muted uppercase tracking-wide">
                    <tr className="border-b border-border text-left">
                      <th className="py-2 pr-3 font-medium">Severity</th>
                      <th className="py-2 pr-3 font-medium">CVE</th>
                      <th className="py-2 pr-3 font-medium">Package</th>
                      <th className="py-2 pr-3 font-medium">Installed</th>
                      <th className="py-2 pr-3 font-medium">Fixed in</th>
                    </tr>
                  </thead>
                  <tbody>
                    {result.vulns.map((v, i) => (
                      <tr key={i} className="border-b border-border/40 align-top">
                        <td className="py-1.5 pr-3"><span className={clsx("px-1.5 py-0.5 rounded border", sevColor[v.severity] ?? sevColor.UNKNOWN)}>{v.severity}</span></td>
                        <td className="py-1.5 pr-3 font-mono whitespace-nowrap">{safeHttpUrl(v.url) ? <a href={v.url} target="_blank" rel="noreferrer" className="text-accent hover:underline">{v.id}</a> : v.id}</td>
                        <td className="py-1.5 pr-3 font-mono break-all">{v.package}</td>
                        <td className="py-1.5 pr-3 font-mono break-all">{v.version}</td>
                        <td className="py-1.5 pr-3 font-mono break-all text-ok">{v.fixedVersion || "—"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

// cleanLayerCmd strips the noisy buildkit prefix from a history command line.
function cleanLayerCmd(s: string): string {
  return s.replace(/^\/bin\/sh -c #\(nop\)\s*/, "").replace(/^\/bin\/sh -c /, "RUN ").trim() || "—";
}

// PullPanel pulls an image, streaming per-layer progress over a WebSocket.
function PullPanel({ onPulled }: { onPulled: () => void }) {
  const [ref, setRef] = useState("");
  const [pulling, setPulling] = useState(false);
  const [layers, setLayers] = useState<Map<string, PullProgress>>(new Map());
  const [status, setStatus] = useState("");
  const [error, setError] = useState("");
  const wsRef = useRef<WebSocket | null>(null);

  useEffect(() => () => wsRef.current?.close(), []);

  const start = (e: React.FormEvent) => {
    e.preventDefault();
    const image = ref.trim();
    if (!image || pulling) return;
    setPulling(true);
    setLayers(new Map());
    setStatus("Connecting…");
    setError("");

    const proto = location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${proto}://${location.host}/api/images/pull?ref=${encodeURIComponent(image)}${hostParam("&")}`);
    wsRef.current = ws;

    ws.onmessage = (ev) => {
      let p: PullProgress;
      try {
        p = JSON.parse(ev.data as string);
      } catch {
        return;
      }
      if (p.error) {
        setError(p.error);
        setStatus("");
        return;
      }
      if (p.done) {
        setStatus("Pull complete");
        return;
      }
      setStatus(p.status ?? "");
      // Layer-scoped messages carry an id; aggregate the latest per layer.
      if (p.id) {
        setLayers((prev) => {
          const next = new Map(prev);
          next.set(p.id!, p);
          return next;
        });
      }
    };
    ws.onclose = () => {
      setPulling(false);
      wsRef.current = null;
      onPulled();
    };
    ws.onerror = () => setError("connection failed");
  };

  const cancel = () => wsRef.current?.close();

  const layerList = [...layers.values()];

  return (
    <form onSubmit={start} className="card p-4 space-y-3">
      <div className="flex items-center gap-2">
        <div className="relative flex-1">
          <Download className="absolute left-2.5 top-2.5 h-4 w-4 text-muted" />
          <input
            className="input pl-8"
            placeholder="Pull an image, e.g. nginx:latest or ghcr.io/owner/app:tag"
            value={ref}
            onChange={(e) => setRef(e.target.value)}
            disabled={pulling}
          />
        </div>
        {pulling ? (
          <button type="button" className="btn-ghost" onClick={cancel}>
            <X className="h-4 w-4" /> Cancel
          </button>
        ) : (
          <button className="btn-primary" disabled={!ref.trim()}>
            <Download className="h-4 w-4" /> Pull
          </button>
        )}
      </div>

      {(pulling || status || error) && (
        <div className="rounded-md bg-panel2 p-3 text-xs font-mono space-y-1.5">
          {status && <div className="flex items-center gap-2 text-text">{pulling && <Loader2 className="h-3.5 w-3.5 animate-spin" />}{status}</div>}
          {error && <div className="text-danger">{error}</div>}
          {layerList.map((l) => (
            <LayerRow key={l.id} p={l} />
          ))}
        </div>
      )}
    </form>
  );
}

function LayerRow({ p }: { p: PullProgress }) {
  const pct = p.total && p.total > 0 ? Math.min(100, Math.round(((p.current ?? 0) / p.total) * 100)) : null;
  return (
    <div className="flex items-center gap-2 text-muted">
      <span className="w-28 shrink-0 truncate text-text/70">{p.id}</span>
      <span className="w-32 shrink-0 truncate">{p.status}</span>
      {pct !== null && (
        <div className="flex-1 h-1.5 bg-border rounded-sm overflow-hidden">
          <div className={clsx("h-full bg-accent transition-all")} style={{ width: `${pct}%` }} />
        </div>
      )}
      {pct !== null && <span className="w-9 text-right">{pct}%</span>}
    </div>
  );
}
