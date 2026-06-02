import { useEffect, useRef, useState } from "react";
import { Upload, X, Loader2, CheckCircle2 } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";
import type { ImageSummary, PullProgress } from "../lib/types";
import { hostParam } from "../lib/host";
import { shortId } from "../lib/format";

// PushModal tags a local image for a registry (if needed) and pushes it,
// streaming per-layer progress over the push WebSocket.
export function PushModal({ image, onClose, onDone }: { image: ImageSummary; onClose: () => void; onDone: () => void }) {
  const firstTag = (image.repoTags ?? []).find((t) => t && t !== "<none>:<none>") ?? "";
  const [target, setTarget] = useState(firstTag);
  const [phase, setPhase] = useState<"idle" | "working" | "done" | "error">("idle");
  const [status, setStatus] = useState("");
  const [error, setError] = useState("");
  const [layers, setLayers] = useState<Map<string, PullProgress>>(new Map());
  const wsRef = useRef<WebSocket | null>(null);

  useEffect(() => () => wsRef.current?.close(), []);

  const start = async (e: React.FormEvent) => {
    e.preventDefault();
    const ref = target.trim();
    if (!ref || phase === "working") return;
    setPhase("working");
    setError("");
    setLayers(new Map());

    // Tag the image under the target ref first (no-op if it already has it).
    const source = firstTag || image.id;
    if (ref !== source) {
      setStatus(`Tagging ${ref}…`);
      try {
        const r = await api.tagImage(source, ref);
        if (!r.ok) { setError(r.error ?? "tag failed"); setPhase("error"); return; }
      } catch { setError("tag request failed"); setPhase("error"); return; }
    }

    setStatus("Connecting…");
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${proto}://${location.host}/api/images/push?ref=${encodeURIComponent(ref)}${hostParam("&")}`);
    wsRef.current = ws;
    ws.onmessage = (ev) => {
      let p: PullProgress;
      try { p = JSON.parse(ev.data as string); } catch { return; }
      if (p.error) { setError(p.error); setPhase("error"); return; }
      if (p.done) { setStatus("Push complete"); setPhase("done"); return; }
      setStatus(p.status ?? "");
      if (p.id) setLayers((prev) => new Map(prev).set(p.id!, p));
    };
    ws.onclose = () => { wsRef.current = null; if (phase !== "error") onDone(); };
    ws.onerror = () => { setError("connection failed"); setPhase("error"); };
  };

  const layerList = [...layers.values()];

  return (
    <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <form className="card w-full max-w-2xl" onClick={(e) => e.stopPropagation()} onSubmit={start}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <Upload className="h-4 w-4 text-accent" />
          <div className="font-medium">Push image</div>
          <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 space-y-3">
          <div className="text-xs text-muted">
            Source: <span className="font-mono text-text/80">{firstTag || shortId(image.id)}</span>
          </div>
          <div>
            <label className="label">Target reference (registry-qualified)</label>
            <input
              className="input font-mono"
              value={target}
              onChange={(e) => setTarget(e.target.value)}
              placeholder="registry.example.com/team/app:tag"
              disabled={phase === "working"}
            />
            <p className="text-[11px] text-muted mt-1">Credentials are resolved from <strong>Registries</strong> by the target's host.</p>
          </div>
          {(status || error) && (
            <div className="rounded-md bg-panel2 p-3 text-xs font-mono space-y-1.5 max-h-60 overflow-auto">
              {status && <div className={clsx("flex items-center gap-2", phase === "done" ? "text-ok" : "text-text")}>{phase === "working" && <Loader2 className="h-3.5 w-3.5 animate-spin" />}{phase === "done" && <CheckCircle2 className="h-3.5 w-3.5" />}{status}</div>}
              {error && <div className="text-danger">{error}</div>}
              {layerList.map((l) => {
                const pct = l.total && l.total > 0 ? Math.min(100, Math.round(((l.current ?? 0) / l.total) * 100)) : null;
                return (
                  <div key={l.id} className="flex items-center gap-2 text-muted">
                    <span className="w-28 shrink-0 truncate text-text/70">{l.id}</span>
                    <span className="w-32 shrink-0 truncate">{l.status}</span>
                    {pct !== null && <div className="flex-1 h-1.5 bg-border rounded overflow-hidden"><div className="h-full bg-accent" style={{ width: `${pct}%` }} /></div>}
                    {pct !== null && <span className="w-9 text-right">{pct}%</span>}
                  </div>
                );
              })}
            </div>
          )}
        </div>
        <div className="flex justify-end gap-2 p-4 border-t border-border">
          <button type="button" className="btn-ghost" onClick={onClose}>{phase === "done" ? "Close" : "Cancel"}</button>
          <button className="btn-primary" disabled={!target.trim() || phase === "working"}>
            {phase === "working" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Upload className="h-4 w-4" />} Push
          </button>
        </div>
      </form>
    </div>
  );
}
