import { useState } from "react";
import { Boxes, X, Loader2 } from "lucide-react";
import { api } from "../lib/api";
import type { CreateSpec, PortSpec } from "../lib/types";

const RESTART = ["", "no", "on-failure", "always", "unless-stopped"];

// CreateContainerModal is the create/run form. Free-text areas (one entry per
// line) keep the UI simple while covering the common docker run options.
export function CreateContainerModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [image, setImage] = useState("");
  const [name, setName] = useState("");
  const [cmd, setCmd] = useState("");
  const [env, setEnv] = useState("");
  const [ports, setPorts] = useState("");
  const [binds, setBinds] = useState("");
  const [restartPolicy, setRestartPolicy] = useState("");
  const [memoryMb, setMemoryMb] = useState("");
  const [cpus, setCpus] = useState("");
  const [start, setStart] = useState(true);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const lines = (s: string) => s.split("\n").map((l) => l.trim()).filter(Boolean);

  const parsePorts = (): PortSpec[] =>
    lines(ports).map((l) => {
      // "8080:80", "8080:80/udp", or just "80" (expose only)
      const [mapping, proto] = l.split("/");
      const parts = mapping.split(":");
      const containerPort = parts.length > 1 ? parts[1] : parts[0];
      const hostPort = parts.length > 1 ? parts[0] : "";
      return { hostPort, containerPort, proto: proto || "tcp" };
    });

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!image.trim() || busy) return;
    setBusy(true); setErr("");
    const spec: CreateSpec = {
      image: image.trim(),
      name: name.trim(),
      cmd: cmd.trim() ? cmd.trim().split(/\s+/) : [],
      env: lines(env),
      binds: lines(binds),
      ports: parsePorts(),
      restartPolicy,
      memory: memoryMb ? Math.round(Number(memoryMb) * 1024 * 1024) : 0,
      nanoCpus: cpus ? Math.round(Number(cpus) * 1e9) : 0,
      start,
    };
    try {
      const r = await api.createContainer(spec);
      if (r.ok) onDone();
      else setErr(r.error ?? "create failed");
    } catch (e) {
      setErr(e instanceof Error ? e.message : "request failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <form className="card w-full max-w-2xl max-h-[88vh] flex flex-col" onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <Boxes className="h-4 w-4 text-accent" />
          <div className="font-medium">Create container</div>
          <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 space-y-3 overflow-auto">
          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            <div>
              <label className="label">Image *</label>
              <input className="input font-mono" value={image} onChange={(e) => setImage(e.target.value)} placeholder="nginx:latest" required />
            </div>
            <div>
              <label className="label">Name</label>
              <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="(optional)" />
            </div>
          </div>
          <div>
            <label className="label">Command (optional, overrides CMD)</label>
            <input className="input font-mono" value={cmd} onChange={(e) => setCmd(e.target.value)} placeholder="e.g. nginx -g 'daemon off;'" />
          </div>
          <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
            <Area label="Ports (host:container[/proto])" value={ports} set={setPorts} ph={"8080:80\n5353:53/udp"} />
            <Area label="Env (KEY=VALUE)" value={env} set={setEnv} ph={"TZ=Europe/Prague"} />
            <Area label="Volumes (src:dst[:ro])" value={binds} set={setBinds} ph={"/data:/var/lib/data"} />
          </div>
          <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
            <div>
              <label className="label">Restart policy</label>
              <select className="input" value={restartPolicy} onChange={(e) => setRestartPolicy(e.target.value)}>
                {RESTART.map((p) => <option key={p} value={p}>{p || "(none)"}</option>)}
              </select>
            </div>
            <div>
              <label className="label">Memory limit (MB)</label>
              <input className="input" type="number" min="0" value={memoryMb} onChange={(e) => setMemoryMb(e.target.value)} placeholder="0 = unlimited" />
            </div>
            <div>
              <label className="label">CPUs</label>
              <input className="input" type="number" min="0" step="0.1" value={cpus} onChange={(e) => setCpus(e.target.value)} placeholder="0 = unlimited" />
            </div>
          </div>
          <label className="flex items-center gap-2 text-sm text-muted">
            <input type="checkbox" checked={start} onChange={(e) => setStart(e.target.checked)} /> Start immediately after create
          </label>
          {err && <p className="text-sm text-danger break-all">{err}</p>}
        </div>
        <div className="flex justify-end gap-2 p-4 border-t border-border">
          <button type="button" className="btn-ghost" onClick={onClose}>Cancel</button>
          <button className="btn-primary" disabled={!image.trim() || busy}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Boxes className="h-4 w-4" />} Create{start ? " & start" : ""}
          </button>
        </div>
      </form>
    </div>
  );
}

function Area({ label, value, set, ph }: { label: string; value: string; set: (s: string) => void; ph: string }) {
  return (
    <div>
      <label className="label">{label}</label>
      <textarea className="input font-mono text-xs h-20" value={value} onChange={(e) => set(e.target.value)} placeholder={ph} />
    </div>
  );
}
