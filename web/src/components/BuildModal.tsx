import { useEffect, useRef, useState } from "react";
import { Hammer, X, Loader2, CheckCircle2 } from "lucide-react";
import clsx from "clsx";
import { getHostId } from "../lib/host";

// BuildModal uploads a tar build context and streams the daemon's build output
// (newline-delimited JSON) live into a log pane.
export function BuildModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [file, setFile] = useState<File | null>(null);
  const [tags, setTags] = useState("");
  const [dockerfile, setDockerfile] = useState("");
  const [nocache, setNocache] = useState(false);
  const [phase, setPhase] = useState<"idle" | "working" | "done" | "error">("idle");
  const [output, setOutput] = useState("");
  const [error, setError] = useState("");
  const preRef = useRef<HTMLPreElement>(null);

  useEffect(() => {
    if (preRef.current) preRef.current.scrollTop = preRef.current.scrollHeight;
  }, [output]);

  const start = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!file || phase === "working") return;
    setPhase("working");
    setOutput("");
    setError("");

    const params = new URLSearchParams();
    tags.split(/[\s,]+/).filter(Boolean).forEach((t) => params.append("tag", t));
    if (dockerfile.trim()) params.set("dockerfile", dockerfile.trim());
    if (nocache) params.set("nocache", "1");
    const h = getHostId();
    if (h != null) params.set("host", String(h));

    try {
      const res = await fetch(`/api/images/build?${params.toString()}`, {
        method: "POST",
        headers: { "Content-Type": "application/x-tar" },
        credentials: "same-origin",
        body: file,
      });
      if (!res.body) { setError("no response stream"); setPhase("error"); return; }
      const reader = res.body.getReader();
      const dec = new TextDecoder();
      let buf = "";
      let failed = false;
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        const lines = buf.split("\n");
        buf = lines.pop() ?? "";
        for (const line of lines) {
          if (!line.trim()) continue;
          let msg: { stream?: string; error?: string; done?: boolean };
          try { msg = JSON.parse(line); } catch { continue; }
          if (msg.error) { setError(msg.error); failed = true; }
          if (msg.stream) setOutput((o) => o + msg.stream);
        }
      }
      setPhase(failed ? "error" : "done");
      if (!failed) onDone();
    } catch {
      setError("build request failed");
      setPhase("error");
    }
  };

  return (
    <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <form className="card w-full max-w-3xl max-h-[88vh] flex flex-col" onClick={(e) => e.stopPropagation()} onSubmit={start}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <Hammer className="h-4 w-4 text-accent" />
          <div className="font-medium">Build image</div>
          <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 space-y-3 overflow-auto">
          <div>
            <label className="label">Build context (.tar or .tar.gz)</label>
            <input type="file" accept=".tar,.tar.gz,.tgz,application/x-tar,application/gzip" className="input py-1.5" onChange={(e) => setFile(e.target.files?.[0] ?? null)} disabled={phase === "working"} />
            <p className="text-[11px] text-muted mt-1">A tar archive of your build context (the directory containing the Dockerfile).</p>
          </div>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            <div>
              <label className="label">Tags (space/comma separated)</label>
              <input className="input font-mono" value={tags} onChange={(e) => setTags(e.target.value)} placeholder="myapp:latest" disabled={phase === "working"} />
            </div>
            <div>
              <label className="label">Dockerfile path (optional)</label>
              <input className="input font-mono" value={dockerfile} onChange={(e) => setDockerfile(e.target.value)} placeholder="Dockerfile" disabled={phase === "working"} />
            </div>
          </div>
          <label className="flex items-center gap-2 text-sm text-muted">
            <input type="checkbox" checked={nocache} onChange={(e) => setNocache(e.target.checked)} disabled={phase === "working"} /> No cache
          </label>
          {(output || error) && (
            <pre ref={preRef} className="rounded-md bg-bg border border-border p-3 text-xs font-mono whitespace-pre-wrap break-all max-h-72 overflow-auto text-text/85">
              {output}
              {error && <span className="text-danger">{"\n" + error}</span>}
            </pre>
          )}
        </div>
        <div className="flex items-center justify-end gap-2 p-4 border-t border-border">
          {phase === "done" && <span className="text-xs text-ok flex items-center gap-1 mr-auto"><CheckCircle2 className="h-4 w-4" /> Build complete</span>}
          <button type="button" className="btn-ghost" onClick={onClose}>{phase === "done" ? "Close" : "Cancel"}</button>
          <button className={clsx("btn-primary", phase === "done" && "hidden")} disabled={!file || phase === "working"}>
            {phase === "working" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Hammer className="h-4 w-4" />} Build
          </button>
        </div>
      </form>
    </div>
  );
}
