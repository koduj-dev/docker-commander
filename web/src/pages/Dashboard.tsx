import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Boxes, Cpu, Database, HardDrive, Layers, Play, Server, Square, Wrench } from "lucide-react";
import { api } from "../lib/api";
import type { DiskUsage, SystemInfo } from "../lib/types";
import { bytes } from "../lib/format";
import { PageHeader } from "../layout/Shell";
import { StatCard, Spinner } from "../components/ui";
import { ResourceBreakdown } from "../components/ResourceBreakdown";
import { OpenPorts } from "../components/OpenPorts";
import { ContainerTable } from "./Containers";

export function Dashboard() {
  const [info, setInfo] = useState<SystemInfo | null>(null);
  const [df, setDf] = useState<DiskUsage | null>(null);

  // Poll so the counts/disk usage reflect containers starting or stopping.
  useEffect(() => {
    const load = () => {
      api.system().then(setInfo).catch(() => {});
      api.diskUsage().then(setDf).catch(() => {});
    };
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, []);

  return (
    <>
      <PageHeader title="Dashboard" />
      <div className="p-6 space-y-6">
        {!info ? (
          <div className="flex items-center gap-2 text-muted">
            <Spinner /> Loading host info…
          </div>
        ) : (
          <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-3">
            <StatCard icon={<Server className="h-5 w-5" />} label="Host" value={info.hostName} sub={`Docker ${info.serverVersion}`} />
            <StatCard icon={<Cpu className="h-5 w-5" />} label="CPUs" value={info.cpus} sub={info.architecture} />
            <StatCard icon={<HardDrive className="h-5 w-5" />} label="Memory" value={bytes(info.memTotal)} sub={info.operatingSystem} />
            <StatCard icon={<Play className="h-5 w-5" />} label="Running" value={info.containersRunning} />
            <StatCard icon={<Square className="h-5 w-5" />} label="Stopped" value={info.containersStopped} />
            <StatCard icon={<Layers className="h-5 w-5" />} label="Images" value={info.images} />
          </div>
        )}

        {df && (
          <div>
            <h2 className="text-sm font-semibold text-muted mb-3">Disk usage</h2>
            <div className="grid grid-cols-2 md:grid-cols-4 lg:grid-cols-5 gap-3">
              <StatCard icon={<HardDrive className="h-5 w-5" />} label="Layers total" value={bytes(df.layersSize)} />
              <StatCard icon={<Layers className="h-5 w-5" />} label="Images" value={bytes(df.images.size)} sub={`${df.images.count} images`} />
              <StatCard icon={<Boxes className="h-5 w-5" />} label="Containers (rw)" value={bytes(df.containers.size)} sub={`${df.containers.count} containers`} />
              <StatCard icon={<Database className="h-5 w-5" />} label="Volumes" value={bytes(df.volumes.size)} sub={`${df.volumes.count} volumes`} />
              <StatCard icon={<Wrench className="h-5 w-5" />} label="Build cache" value={bytes(df.buildCache.size)} sub={`${df.buildCache.count} records`} />
            </div>
          </div>
        )}

        <ResourceBreakdown />

        <OpenPorts />

        <div>
          <div className="flex items-baseline justify-between mb-3">
            <h2 className="text-sm font-semibold text-muted">Running containers</h2>
            <Link to="/containers" className="text-xs text-accent hover:underline">View all →</Link>
          </div>
          <ContainerTable runningOnly />
        </div>
      </div>
    </>
  );
}
