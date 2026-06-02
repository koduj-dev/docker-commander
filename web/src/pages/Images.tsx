import { useCallback, useEffect, useRef, useState } from "react";
import { Download, Trash2, Layers, Loader2, X, Boxes, Eraser } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { ImageSummary, PullProgress } from "../lib/types";
import { hostParam } from "../lib/host";
import { bytes, relTime, shortId } from "../lib/format";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";

export function Images() {
  const [images, setImages] = useState<ImageSummary[] | null>(null);
  const [busy, setBusy] = useState<Record<string, boolean>>({});
  const [err, setErr] = useState<Record<string, string>>({});
  const [notice, setNotice] = useState("");
  const [pruning, setPruning] = useState(false);

  const load = useCallback(() => {
    api.images().then(setImages).catch(() => setImages([]));
  }, []);
  useEffect(() => load(), [load]);

  const remove = async (img: ImageSummary, force = false) => {
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
          <button className="btn-ghost" onClick={prune} disabled={pruning || danglingCount === 0} title="Remove dangling (untagged) images">
            {pruning ? <Loader2 className="h-4 w-4 animate-spin" /> : <Eraser className="h-4 w-4" />}
            Prune {danglingCount > 0 ? `(${danglingCount})` : ""}
          </button>
        }
      />
      <div className="p-6 space-y-4">
        <PullPanel onPulled={load} />
        {notice && <div className="text-xs text-muted">{notice}</div>}
        {images.length === 0 ? (
          <EmptyState title="No images" hint="Pull one above to get started." />
        ) : (
          <div className="card divide-y divide-border">
            {images.map((img) => {
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
                      {img.dangling && <span className="text-[10px] bg-warn/15 text-warn rounded px-1.5 py-0.5">dangling</span>}
                      {img.inUse && <span className="text-[10px] bg-ok/15 text-ok rounded px-1.5 py-0.5 inline-flex items-center gap-1"><Boxes className="h-3 w-3" />in use</span>}
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
                  <button
                    className="btn-ghost px-2 py-1 text-danger shrink-0"
                    title="Remove image"
                    disabled={busy[img.id]}
                    onClick={() => remove(img)}
                  >
                    {busy[img.id] ? <Loader2 className="h-4 w-4 animate-spin" /> : <Trash2 className="h-4 w-4" />}
                  </button>
                </div>
              );
            })}
          </div>
        )}
      </div>
    </>
  );
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
        <div className="flex-1 h-1.5 bg-border rounded overflow-hidden">
          <div className={clsx("h-full bg-accent transition-all")} style={{ width: `${pct}%` }} />
        </div>
      )}
      {pct !== null && <span className="w-9 text-right">{pct}%</span>}
    </div>
  );
}
