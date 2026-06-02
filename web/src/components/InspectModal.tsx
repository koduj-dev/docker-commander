import { useEffect, useRef, useState } from "react";
import { Copy, Check, X, Search } from "lucide-react";
import { api } from "../lib/api";
import { Spinner } from "./ui";

type Kind = "container" | "image" | "network" | "volume";

// InspectModal fetches and pretty-prints the daemon's raw JSON for any object.
// It is reused across containers, images, networks and volumes.
export function InspectModal({ kind, id, title, onClose }: { kind: Kind; id: string; title: string; onClose: () => void }) {
  const [json, setJson] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [filter, setFilter] = useState("");
  const [copied, setCopied] = useState(false);
  const copyTimer = useRef<number | undefined>(undefined);

  useEffect(() => {
    api
      .inspect(kind, id)
      .then((data) => setJson(JSON.stringify(data, null, 2)))
      .catch((e) => setError(e instanceof Error ? e.message : "inspect failed"));
  }, [kind, id]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  useEffect(() => () => window.clearTimeout(copyTimer.current), []);

  const copy = async () => {
    if (!json) return;
    try {
      await navigator.clipboard.writeText(json);
      setCopied(true);
      copyTimer.current = window.setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard may be unavailable over plain http */
    }
  };

  // When filtering, keep only lines that match (cheap, line-oriented).
  const shown = json && filter ? json.split("\n").filter((l) => l.toLowerCase().includes(filter.toLowerCase())).join("\n") : json;

  return (
    <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <div className="card w-full max-w-4xl max-h-[85vh] flex flex-col" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <div className="font-medium min-w-0">
            <span className="text-xs uppercase tracking-wide text-muted mr-2">{kind}</span>
            <span className="break-all">{title}</span>
          </div>
          <div className="relative ml-auto w-48">
            <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted" />
            <input className="input pl-8 py-1.5" placeholder="Filter lines…" value={filter} onChange={(e) => setFilter(e.target.value)} />
          </div>
          <button className="btn-ghost px-2 py-1.5" onClick={copy} title="Copy JSON" disabled={!json}>
            {copied ? <Check className="h-4 w-4 text-ok" /> : <Copy className="h-4 w-4" />}
          </button>
          <button className="btn-ghost px-2 py-1.5" onClick={onClose} title="Close (Esc)"><X className="h-4 w-4" /></button>
        </div>
        <div className="flex-1 min-h-0 overflow-auto p-4">
          {error ? (
            <div className="text-sm text-danger">{error}</div>
          ) : json === null ? (
            <div className="flex items-center gap-2 text-muted text-sm"><Spinner className="h-4 w-4" /> Loading…</div>
          ) : (
            <pre className="font-mono text-xs leading-relaxed whitespace-pre text-text/90">{shown}</pre>
          )}
        </div>
      </div>
    </div>
  );
}
