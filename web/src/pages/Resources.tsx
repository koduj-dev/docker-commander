import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { Boxes, HardDrive, Layers, TrendingUp } from "lucide-react";
import { api } from "../lib/api";
import type { ResourceOverview } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { Spinner } from "../components/ui";
import { Tabs } from "../components/Tabs";
import { ContainersTab } from "../components/resources/ContainersTab";
import { NetworkTab } from "../components/resources/NetworkTab";
import { StacksTab } from "../components/resources/StacksTab";
import { DiskTab } from "../components/resources/DiskTab";

const REFRESH_MS = 5000;
type Tab = "containers" | "network" | "stacks" | "disk";
const TABS: Tab[] = ["containers", "network", "stacks", "disk"];

// Resources is where "how much does it take" lives: live CPU/memory/network
// per container and per stack, network throughput ranked over a window, and
// what sits on disk. The tab is in the URL (?tab=) so a link lands on it.
//
// The live figures come from the monitor's background sampler, which updates
// on its own (slower) cadence — this page re-reads that snapshot every
// REFRESH_MS, so two consecutive refreshes can legitimately show the same
// numbers.
export function Resources() {
  const [params, setParams] = useSearchParams();
  const asked = params.get("tab") as Tab | null;
  const tab: Tab = asked && TABS.includes(asked) ? asked : "containers";

  const [data, setData] = useState<ResourceOverview | null>(null);
  const [error, setError] = useState("");
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null);

  // Only the tabs that show the sampler snapshot poll it.
  const live = tab === "containers" || tab === "stacks";
  useEffect(() => {
    if (!live) return;
    let cancelled = false;
    const load = () =>
      api
        .statsOverview()
        .then((d) => { if (!cancelled) { setData(d); setError(""); setUpdatedAt(new Date()); } })
        // A transient failure keeps the last good table instead of blanking it.
        .catch((e) => { if (!cancelled) setError(e instanceof Error ? e.message : "could not read resource usage"); });
    load();
    const t = setInterval(load, REFRESH_MS);
    return () => { cancelled = true; clearInterval(t); };
  }, [live]);

  return (
    <>
      <PageHeader
        title="Resources"
        actions={live && updatedAt && <span className="text-xs text-muted">Updated {updatedAt.toLocaleTimeString()} · refreshes every 5 s</span>}
      />
      <div className="p-6 space-y-4">
        <Tabs
          active={tab}
          onChange={(t) => setParams(t === "containers" ? {} : { tab: t }, { replace: true })}
          tabs={[
            { key: "containers", label: "Containers", icon: <Boxes className="h-4 w-4" /> },
            { key: "network", label: "Network", icon: <TrendingUp className="h-4 w-4" /> },
            { key: "stacks", label: "Stacks", icon: <Layers className="h-4 w-4" /> },
            { key: "disk", label: "Disk", icon: <HardDrive className="h-4 w-4" /> },
          ]}
        />
        {tab === "network" && <NetworkTab />}
        {tab === "disk" && <DiskTab />}
        {live && (
          <>
            {error && <div className="text-sm text-danger">Couldn't read resource usage: {error}</div>}
            {!data ? (
              !error && <div className="flex items-center gap-2 text-muted"><Spinner /> Loading…</div>
            ) : tab === "containers" ? (
              <ContainersTab data={data} />
            ) : (
              <StacksTab data={data} />
            )}
          </>
        )}
      </div>
    </>
  );
}
