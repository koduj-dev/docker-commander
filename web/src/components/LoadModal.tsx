import { useState } from "react";
import { Download, X, Loader2, CheckCircle2 } from "lucide-react";
import clsx from "clsx";
import { api } from "../lib/api";

// LoadModal uploads a tar to either load a docker-save archive or import a
// filesystem tarball as a new image.
export function LoadModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [mode, setMode] = useState<"load" | "import">("load");
  const [file, setFile] = useState<File | null>(null);
  const [ref, setRef] = useState("");
  const [phase, setPhase] = useState<"idle" | "working" | "done" | "error">("idle");
  const [output, setOutput] = useState("");
  const [error, setError] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!file || phase === "working") return;
    if (mode === "import" && !ref.trim()) { setError("a target reference is required for import"); return; }
    setPhase("working"); setError(""); setOutput("");
    try {
      const r = mode === "load" ? await api.loadImage(file) : await api.importImage(ref.trim(), file);
      if (r.ok) { setOutput(r.output || "Done."); setPhase("done"); onDone(); }
      else { setError(r.error ?? "failed"); setPhase("error"); }
    } catch (e) {
      setError(e instanceof Error ? e.message : "request failed"); setPhase("error");
    }
  };

  return (
    <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <form className="card w-full max-w-2xl" onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <Download className="h-4 w-4 text-accent" />
          <div className="font-medium">Load / import image</div>
          <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 space-y-3">
          <div className="flex gap-1 bg-panel2 rounded-lg p-1 w-max">
            {(["load", "import"] as const).map((m) => (
              <button key={m} type="button" onClick={() => setMode(m)}
                className={clsx("px-3 py-1 rounded-md text-xs font-medium capitalize", mode === m ? "bg-accent text-white" : "text-muted")}>
                {m}
              </button>
            ))}
          </div>
          <p className="text-[11px] text-muted">
            {mode === "load"
              ? "Load images from a docker save archive (.tar) — tags are restored from the archive."
              : "Import a filesystem tarball (.tar/.tar.gz) as a new image; you choose the tag."}
          </p>
          <div>
            <label className="label">Archive (.tar / .tar.gz)</label>
            <input type="file" accept=".tar,.tar.gz,.tgz,application/x-tar,application/gzip" className="input py-1.5" onChange={(e) => setFile(e.target.files?.[0] ?? null)} disabled={phase === "working"} />
          </div>
          {mode === "import" && (
            <div>
              <label className="label">Target reference</label>
              <input className="input font-mono" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="myimage:latest" disabled={phase === "working"} />
            </div>
          )}
          {(output || error) && (
            <pre className="rounded-md bg-bg border border-border p-3 text-xs font-mono whitespace-pre-wrap break-all max-h-48 overflow-auto text-text/85">
              {output}{error && <span className="text-danger">{error}</span>}
            </pre>
          )}
        </div>
        <div className="flex items-center justify-end gap-2 p-4 border-t border-border">
          {phase === "done" && <span className="text-xs text-ok flex items-center gap-1 mr-auto"><CheckCircle2 className="h-4 w-4" /> Done</span>}
          <button type="button" className="btn-ghost" onClick={onClose}>{phase === "done" ? "Close" : "Cancel"}</button>
          <button className={clsx("btn-primary", phase === "done" && "hidden")} disabled={!file || phase === "working"}>
            {phase === "working" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Download className="h-4 w-4" />} {mode === "load" ? "Load" : "Import"}
          </button>
        </div>
      </form>
    </div>
  );
}

// triggerDownload starts a same-origin download via a transient anchor (cookie
// auth + the server's Content-Disposition handle the rest).
export function triggerDownload(url: string) {
  const a = document.createElement("a");
  a.href = url;
  a.rel = "noopener";
  document.body.appendChild(a);
  a.click();
  a.remove();
}
