import { useEffect, useState } from "react";
import { X, Cpu, MemoryStick, HardDrive, Server, Info } from "lucide-react";
import { api } from "../lib/api";
import type { Host, SystemInfo } from "../lib/types";
import { bytes } from "../lib/format";
import { Spinner } from "./ui";

// HostDetailModal shows the engine/host facts for a single host: hardware,
// OS/kernel and the Docker engine configuration.
export function HostDetailModal({ host, onClose }: { host: Host; onClose: () => void }) {
  const [info, setInfo] = useState<SystemInfo | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    api
      .hostSystem(host.id)
      .then(setInfo)
      .catch((e) => setError(e instanceof Error ? e.message : "could not load host info"));
  }, [host.id]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  // Docker Desktop / a containerised engine reports the engine VM's OS, not the
  // real host — flag it so "Docker Desktop" isn't mistaken for the box's OS.
  const isDesktop = !!info && /docker desktop/i.test(info.operatingSystem);

  return (
    <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <div className="card w-full max-w-2xl max-h-[85vh] flex flex-col" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <Server className="h-4 w-4 text-accent shrink-0" />
          <div className="font-medium min-w-0">
            <span className="break-all">{host.name}</span>
            <span className="text-xs bg-panel2 rounded px-1.5 py-0.5 text-muted ml-2">{host.kind}</span>
          </div>
          <button className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose} title="Close">
            <X className="h-4 w-4" />
          </button>
        </div>

        <div className="p-4 overflow-auto">
          {error ? (
            <p className="text-sm text-danger">{error}</p>
          ) : !info ? (
            <div className="flex items-center gap-2 text-muted text-sm">
              <Spinner /> Loading host info…
            </div>
          ) : (
            <div className="space-y-5">
              <Section icon={<Cpu className="h-4 w-4" />} title="Hardware">
                <Row label="Hostname" value={info.hostName} mono />
                <Row label="CPUs" value={info.cpus ? String(info.cpus) : "—"} />
                <Row
                  label="Memory"
                  value={info.memTotal ? bytes(info.memTotal) : "—"}
                  icon={<MemoryStick className="h-3.5 w-3.5 text-muted" />}
                />
                <Row label="Architecture" value={info.architecture || "—"} mono />
              </Section>

              <Section icon={<Info className="h-4 w-4" />} title="Operating system">
                <Row label="OS" value={info.operatingSystem || "—"} />
                <Row label="Type" value={info.osType || "—"} mono />
                {info.osVersion && <Row label="Version" value={info.osVersion} mono />}
                <Row label="Kernel" value={info.kernelVersion || "—"} mono />
                {isDesktop && (
                  <p className="text-xs text-muted bg-panel2 rounded-md p-2.5 mt-1 leading-relaxed">
                    This is <strong>Docker Desktop</strong>: the values above describe the engine's Linux VM, not the
                    desktop OS. The Docker API doesn't expose the underlying host — the <em>kernel</em> is the best hint
                    (e.g. <code>…microsoft-standard-WSL2</code> means a Windows/WSL2 host).
                  </p>
                )}
              </Section>

              <Section icon={<Server className="h-4 w-4" />} title="Docker engine">
                <Row label="Version" value={info.serverVersion || "—"} mono />
                <Row label="Storage driver" value={info.storageDriver || "—"} mono />
                <Row label="Logging driver" value={info.loggingDriver || "—"} mono />
                <Row
                  label="Cgroup"
                  value={[info.cgroupDriver, info.cgroupVersion && `v${info.cgroupVersion}`].filter(Boolean).join(" · ") || "—"}
                  mono
                />
                <Row label="Live restore" value={info.liveRestore ? "enabled" : "disabled"} />
                <Row
                  label="Root dir"
                  value={info.dockerRootDir || "—"}
                  mono
                  icon={<HardDrive className="h-3.5 w-3.5 text-muted" />}
                />
              </Section>

              <Section icon={<HardDrive className="h-4 w-4" />} title="Workload">
                <Row
                  label="Containers"
                  value={`${info.containers} total · ${info.containersRunning} running · ${info.containersPaused} paused · ${info.containersStopped} stopped`}
                />
                <Row label="Images" value={String(info.images)} />
              </Section>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function Section({ icon, title, children }: { icon: React.ReactNode; title: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-muted mb-2">
        <span className="text-accent">{icon}</span>
        {title}
      </div>
      <dl className="divide-y divide-border rounded-lg border border-border overflow-hidden">{children}</dl>
    </div>
  );
}

function Row({ label, value, mono, icon }: { label: string; value: string; mono?: boolean; icon?: React.ReactNode }) {
  return (
    <div className="flex items-start gap-3 px-3 py-2 text-sm bg-panel">
      <dt className="w-32 shrink-0 text-muted">{label}</dt>
      <dd className={`min-w-0 break-all flex items-center gap-1.5 ${mono ? "font-mono text-xs" : ""}`}>
        {icon}
        {value}
      </dd>
    </div>
  );
}
