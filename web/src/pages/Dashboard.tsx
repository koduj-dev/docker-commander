import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Cpu, HardDrive, Layers, Play, Server, Square } from "lucide-react";
import { api } from "../lib/api";
import type { SystemInfo } from "../lib/types";
import { bytes } from "../lib/format";
import { PageHeader } from "../layout/Shell";
import { StatCard, Spinner } from "../components/ui";
import { ContainerTable } from "./Containers";

export function Dashboard() {
  const [info, setInfo] = useState<SystemInfo | null>(null);

  useEffect(() => {
    api.system().then(setInfo).catch(() => {});
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
