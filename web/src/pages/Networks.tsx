import { useEffect, useState } from "react";
import { api } from "../lib/api";
import type { NetworkSummary } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";
import { shortId } from "../lib/format";

export function Networks() {
  const [nets, setNets] = useState<NetworkSummary[] | null>(null);

  useEffect(() => {
    api.networks().then(setNets).catch(() => setNets([]));
  }, []);

  return (
    <>
      <PageHeader title="Networks" />
      <div className="p-6">
        {!nets ? (
          <div className="flex items-center gap-2 text-muted"><Spinner /> Loading…</div>
        ) : nets.length === 0 ? (
          <EmptyState title="No networks found" />
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
            {nets.map((n) => (
              <div key={n.id} className="card p-4">
                <div className="flex items-center justify-between">
                  <div className="font-medium">{n.name}</div>
                  <span className="text-xs bg-panel2 rounded px-2 py-0.5 text-muted">{n.driver}</span>
                </div>
                <div className="text-xs text-muted font-mono mt-1">{shortId(n.id)}</div>
                <div className="mt-3 space-y-1 text-sm">
                  <div className="flex justify-between">
                    <span className="text-muted">Scope</span>
                    <span>{n.scope}{n.internal ? " · internal" : ""}</span>
                  </div>
                  <div className="flex justify-between">
                    <span className="text-muted">Subnets</span>
                    <span className="font-mono text-xs">{(n.subnets ?? []).join(", ") || "—"}</span>
                  </div>
                  <div className="flex justify-between">
                    <span className="text-muted">Containers</span>
                    <span>{(n.containers ?? []).length}</span>
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </>
  );
}
