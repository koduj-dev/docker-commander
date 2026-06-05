import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Blocks, Play, Square, RotateCw, Trash2, Loader2 } from "lucide-react";
import { api } from "../lib/api";
import type { Stack } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { StateBadge, EmptyState, Spinner } from "../components/ui";
import { useDockerEventTick } from "../lib/dockerEvents";

export function Stacks() {
  const [stacks, setStacks] = useState<Stack[] | null>(null);
  const [busy, setBusy] = useState(""); // project currently acting
  const tick = useDockerEventTick();

  const load = useCallback(() => {
    api.stacks().then(setStacks).catch(() => setStacks([]));
  }, []);
  useEffect(() => load(), [load, tick]);

  const act = async (project: string, action: string) => {
    if (
      action === "remove" &&
      !window.confirm(`Remove stack "${project}"?\n\nThis force-removes its containers and the stack's Compose networks (named volumes are kept).`)
    )
      return;
    setBusy(project);
    try {
      await api.stackAction(project, action);
      load();
    } finally {
      setBusy("");
    }
  };

  if (!stacks)
    return (
      <>
        <PageHeader title="Stacks" />
        <div className="p-6 flex items-center gap-2 text-muted"><Spinner /> Loading…</div>
      </>
    );

  return (
    <>
      <PageHeader title="Stacks" />
      <div className="p-6 space-y-4">
        {stacks.length === 0 ? (
          <EmptyState
            title="No Compose stacks"
            hint="Containers labelled with com.docker.compose.project (e.g. started via docker compose) show up here grouped as stacks."
          />
        ) : (
          stacks.map((s) => (
            <div key={s.project} className="card p-4">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="flex items-center gap-2 font-medium">
                    <Blocks className="h-4 w-4 text-accent" /> {s.project}
                    <span className="text-xs text-muted">{s.running}/{s.containers.length} running</span>
                  </div>
                  {s.configFile && <div className="text-xs text-muted font-mono mt-1 break-all">{s.configFile}</div>}
                </div>
                <div className="flex items-center gap-1 shrink-0">
                  {busy === s.project ? (
                    <Loader2 className="h-4 w-4 animate-spin text-muted" />
                  ) : (
                    <>
                      <button className="btn-ghost px-2 py-1" title="Start" onClick={() => act(s.project, "start")}><Play className="h-4 w-4" /></button>
                      <button className="btn-ghost px-2 py-1" title="Stop" onClick={() => act(s.project, "stop")}><Square className="h-4 w-4" /></button>
                      <button className="btn-ghost px-2 py-1" title="Restart" onClick={() => act(s.project, "restart")}><RotateCw className="h-4 w-4" /></button>
                      <button className="btn-ghost px-2 py-1 text-danger" title="Remove stack" onClick={() => act(s.project, "remove")}><Trash2 className="h-4 w-4" /></button>
                    </>
                  )}
                </div>
              </div>

              <div className="mt-3 divide-y divide-border rounded-lg border border-border overflow-hidden">
                {s.containers.map((c) => (
                  <div key={c.id} className="flex items-center gap-3 px-3 py-2 text-sm bg-panel">
                    <span className="w-28 shrink-0 font-medium truncate">{c.service || "—"}</span>
                    <StateBadge state={c.state} />
                    <Link to={`/containers/${c.id}`} className="text-muted hover:text-accent truncate">{c.name}</Link>
                    <span className="ml-auto text-xs text-muted font-mono truncate hidden md:block">{c.image}</span>
                  </div>
                ))}
              </div>
            </div>
          ))
        )}

        <p className="text-xs text-muted">
          Stacks are discovered from the <code>com.docker.compose.project</code> label, so groups started with the{" "}
          <strong>docker&nbsp;compose</strong> CLI appear here too. Deploying a stack from a compose file is coming next.
        </p>
      </div>
    </>
  );
}
