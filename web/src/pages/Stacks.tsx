import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Blocks, Play, Square, RotateCw, Trash2, Loader2, FileText, X, ChevronRight, Search, Copy, Check, Download, FolderGit2 } from "lucide-react";
import { api } from "../lib/api";
import type { Stack, StackContainer } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { StateBadge, EmptyState, Spinner } from "../components/ui";
import { useDockerEventTick } from "../lib/dockerEvents";
import { getPref, setPref } from "../lib/prefs";
import { shortId } from "../lib/format";

type ComposeView = { project: string; loading: boolean; path?: string; content?: string; error?: string };
type Hover = { c: StackContainer; x: number; y: number };
const TOOLTIP_W = 288; // matches w-72

// stackHealth: green = all running & healthy, red = nothing running, yellow =
// partial or an unhealthy container.
function stackHealth(s: Stack): "green" | "yellow" | "red" {
  if (s.running === 0) return "red";
  const unhealthy = s.containers.some((c) => /unhealthy/i.test(c.status));
  if (s.running === s.containers.length && !unhealthy) return "green";
  return "yellow";
}

function StackLed({ s }: { s: Stack }) {
  const h = stackHealth(s);
  const cls = h === "green" ? "bg-ok text-ok" : h === "yellow" ? "bg-warn text-warn" : "bg-danger text-danger";
  const title = h === "green" ? "All running" : h === "yellow" ? "Partially running / unhealthy" : "Stopped";
  return <span className={`h-2.5 w-2.5 rounded-full shrink-0 ${cls}`} style={{ boxShadow: "0 0 6px currentColor" }} title={title} />;
}

function matchStack(s: Stack, q: string): boolean {
  if (s.project.toLowerCase().includes(q)) return true;
  return s.containers.some(
    (c) => c.service.toLowerCase().includes(q) || c.name.toLowerCase().includes(q) || c.image.toLowerCase().includes(q),
  );
}

export function Stacks() {
  const [stacks, setStacks] = useState<Stack[] | null>(null);
  const [busy, setBusy] = useState(""); // project currently acting
  const [compose, setCompose] = useState<ComposeView | null>(null);
  const [query, setQuery] = useState(() => getPref<string>("stacks.query", ""));
  const [health, setHealth] = useState(() => getPref<string>("stacks.health", "all"));
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>(() => getPref("stacks.collapsed", {}));
  const [hover, setHover] = useState<Hover | null>(null);
  const [copied, setCopied] = useState(false);
  const [managed, setManaged] = useState<Set<string>>(new Set());
  const tick = useDockerEventTick();

  const load = useCallback(() => {
    api.stacks().then(setStacks).catch(() => setStacks([]));
  }, []);
  useEffect(() => load(), [load, tick]);

  // Which stacks are DC-managed projects (so we can link back). This changes
  // rarely, so fetch it once — not on every Docker event tick.
  useEffect(() => {
    api.projects().then((r) => setManaged(new Set(r.projects.map((p) => p.slug)))).catch(() => {});
  }, []);

  // ?focus=<slug> (e.g. from "Open in Stacks") filters to and expands that stack.
  const [searchParams, setSearchParams] = useSearchParams();
  useEffect(() => {
    const focus = searchParams.get("focus");
    if (!focus) return;
    setQuery(focus);
    setPref("stacks.query", focus);
    setCollapsed((c) => { const next = { ...c, [focus]: false }; setPref("stacks.collapsed", next); return next; });
    setSearchParams({}, { replace: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const setSearch = (q: string) => { setQuery(q); setPref("stacks.query", q); };
  const setHealthFilter = (h: string) => { setHealth(h); setPref("stacks.health", h); };
  const toggle = (project: string) => {
    setCollapsed((c) => {
      const next = { ...c, [project]: !c[project] };
      setPref("stacks.collapsed", next);
      return next;
    });
  };

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

  const viewCompose = async (project: string) => {
    setCompose({ project, loading: true });
    try {
      const r = await api.stackCompose(project);
      setCompose(r.ok ? { project, loading: false, path: r.path, content: r.content } : { project, loading: false, error: r.error });
    } catch (e) {
      setCompose({ project, loading: false, error: e instanceof Error ? e.message : "failed to read compose file" });
    }
  };

  const shown = useMemo(() => {
    let arr = stacks ?? [];
    if (health !== "all") arr = arr.filter((s) => stackHealth(s) === health);
    const q = query.trim().toLowerCase();
    if (q) arr = arr.filter((s) => matchStack(s, q));
    return arr;
  }, [stacks, query, health]);

  const allCollapsed = shown.length > 0 && shown.every((s) => collapsed[s.project]);
  const toggleAll = () => {
    const collapse = !allCollapsed;
    setCollapsed((c) => {
      const next = { ...c };
      for (const s of shown) next[s.project] = collapse;
      setPref("stacks.collapsed", next);
      return next;
    });
  };

  const copyCompose = async () => {
    if (!compose?.content) return;
    try {
      await navigator.clipboard.writeText(compose.content);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard may be unavailable over plain http */
    }
  };
  const downloadCompose = () => {
    if (!compose?.content) return;
    const name = compose.path ? compose.path.split("/").pop() || "compose.yml" : `${compose.project}.compose.yml`;
    const url = URL.createObjectURL(new Blob([compose.content], { type: "text/yaml" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = name;
    a.click();
    URL.revokeObjectURL(url);
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
      <div className="p-6 space-y-3">
        {stacks.length === 0 ? (
          <EmptyState
            title="No Compose stacks"
            hint="Containers labelled with com.docker.compose.project (e.g. started via docker compose) show up here grouped as stacks."
          />
        ) : (
          <>
            {/* Filter bar — same placement/style as the other agendas. */}
            <div className="flex items-center gap-3">
              <div className="relative flex-1">
                <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted" />
                <input className="input pl-8 py-1.5" placeholder="Filter stacks, services, images…" value={query} onChange={(e) => setSearch(e.target.value)} />
              </div>
              <select className="input py-1.5 w-auto shrink-0 text-sm" value={health} onChange={(e) => setHealthFilter(e.target.value)}>
                <option value="all">All states</option>
                <option value="green">🟢 Running</option>
                <option value="yellow">🟡 Issues</option>
                <option value="red">🔴 Stopped</option>
              </select>
              <span className="text-xs text-muted shrink-0">{shown.length} of {stacks.length}</span>
              <button className="btn-ghost px-2 py-1.5 text-xs shrink-0" onClick={toggleAll}>
                {allCollapsed ? "Expand all" : "Collapse all"}
              </button>
            </div>

            {shown.length === 0 ? (
              <p className="text-sm text-muted">No stacks match “{query}”.</p>
            ) : (
              shown.map((s) => {
                const isOpen = !collapsed[s.project];
                return (
                  <div key={s.project} className="card p-4">
                    <div className="flex items-start justify-between gap-3">
                      <button className="flex items-center gap-2 min-w-0 text-left" onClick={() => toggle(s.project)} title={isOpen ? "Collapse" : "Expand"}>
                        <ChevronRight className={`h-4 w-4 shrink-0 text-muted transition-transform ${isOpen ? "rotate-90" : ""}`} />
                        <StackLed s={s} />
                        <Blocks className="h-4 w-4 text-accent shrink-0" />
                        <span className="font-medium truncate">{s.project}</span>
                        <span className="text-xs text-muted shrink-0">{s.running}/{s.containers.length} running</span>
                      </button>
                      <div className="flex items-center gap-1 shrink-0">
                        {busy === s.project ? (
                          <Loader2 className="h-4 w-4 animate-spin text-muted" />
                        ) : (
                          <>
                            {managed.has(s.project) && (
                              <Link className="btn-ghost px-2 py-1 text-accent" title="Managed project — open in Projects" to={`/projects?open=${encodeURIComponent(s.project)}`}><FolderGit2 className="h-4 w-4" /></Link>
                            )}
                            {s.configFile && (
                              <button className="btn-ghost px-2 py-1" title="View compose file" onClick={() => viewCompose(s.project)}><FileText className="h-4 w-4" /></button>
                            )}
                            <button className="btn-ghost px-2 py-1" title="Start" onClick={() => act(s.project, "start")}><Play className="h-4 w-4" /></button>
                            <button className="btn-ghost px-2 py-1" title="Stop" onClick={() => act(s.project, "stop")}><Square className="h-4 w-4" /></button>
                            <button className="btn-ghost px-2 py-1" title="Restart" onClick={() => act(s.project, "restart")}><RotateCw className="h-4 w-4" /></button>
                            <button className="btn-ghost px-2 py-1 text-danger" title="Remove stack" onClick={() => act(s.project, "remove")}><Trash2 className="h-4 w-4" /></button>
                          </>
                        )}
                      </div>
                    </div>

                    {s.configFile && (
                      <button className="text-xs text-muted font-mono mt-1 break-all hover:text-accent text-left" onClick={() => viewCompose(s.project)} title="View compose file">
                        {s.configFile}
                      </button>
                    )}

                    {isOpen && (
                      <div className="mt-3 rounded-lg border border-border overflow-hidden">
                        {s.containers.map((c, i) => (
                          <div
                            key={c.id}
                            className={`flex items-center gap-3 px-3 py-2 text-sm hover:bg-panel2/40 ${i > 0 ? "border-t border-border" : ""}`}
                            onMouseMove={(e) => setHover({ c, x: e.clientX, y: e.clientY })}
                            onMouseLeave={() => setHover(null)}
                          >
                            <span className="w-28 shrink-0 font-medium truncate">{c.service || "—"}</span>
                            <StateBadge state={c.state} />
                            <Link to={`/containers/${c.id}`} className="text-muted hover:text-accent truncate">{c.name}</Link>
                            <span className="ml-auto text-xs text-muted font-mono truncate hidden md:block">{c.image}</span>
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                );
              })
            )}
          </>
        )}

        <p className="text-xs text-muted">
          Stacks are discovered from the <code>com.docker.compose.project</code> label, so groups started with the{" "}
          <strong>docker&nbsp;compose</strong> CLI appear here too. Deploying a stack from a compose file is coming next.
        </p>
      </div>

      {hover && <HoverCard hover={hover} />}

      {compose && (
        <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-6" onClick={() => setCompose(null)}>
          <div className="card w-[75vw] max-h-[85vh] flex flex-col" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center gap-3 p-4 border-b border-border">
              <FileText className="h-4 w-4 text-accent shrink-0" />
              <div className="min-w-0">
                <div className="font-medium">{compose.project}</div>
                {compose.path && <div className="text-xs text-muted font-mono break-all">{compose.path}</div>}
              </div>
              <div className="flex items-center gap-1 ml-auto">
                {!compose.loading && !compose.error && (
                  <>
                    <button className="btn-ghost px-2 py-1.5" onClick={copyCompose} title="Copy to clipboard">
                      {copied ? <Check className="h-4 w-4 text-ok" /> : <Copy className="h-4 w-4" />}
                    </button>
                    <button className="btn-ghost px-2 py-1.5" onClick={downloadCompose} title="Download"><Download className="h-4 w-4" /></button>
                  </>
                )}
                <button className="btn-ghost px-2 py-1.5" onClick={() => setCompose(null)} title="Close"><X className="h-4 w-4" /></button>
              </div>
            </div>
            <div className="p-4 overflow-auto">
              {compose.loading ? (
                <div className="flex items-center gap-2 text-muted text-sm"><Spinner /> Reading compose file…</div>
              ) : compose.error ? (
                <p className="text-sm text-danger">{compose.error}</p>
              ) : (
                <pre className="text-xs font-mono whitespace-pre overflow-x-auto bg-panel2 rounded-lg p-3">{compose.content}</pre>
              )}
            </div>
          </div>
        </div>
      )}
    </>
  );
}

// HoverCard is a floating details card that follows the cursor.
function HoverCard({ hover }: { hover: Hover }) {
  const { c } = hover;
  const ports = (c.ports ?? []).filter((p) => p.publicPort);
  // Keep the card on-screen: flip left/up near the right/bottom edges.
  const left = hover.x + TOOLTIP_W + 24 > window.innerWidth ? hover.x - TOOLTIP_W - 16 : hover.x + 16;
  const top = Math.min(hover.y + 16, window.innerHeight - 180);
  return (
    <div
      className="pointer-events-none fixed z-60 w-72 rounded-lg border border-border bg-panel2 shadow-xl p-3 text-xs space-y-1.5"
      style={{ left, top }}
    >
      <div className="flex items-center gap-2">
        <StateBadge state={c.state} />
        <span className="font-medium truncate">{c.service || c.name}</span>
        <span className="font-mono text-muted ml-auto">{shortId(c.id)}</span>
      </div>
      <Row label="Image" value={<span className="font-mono break-all">{c.image}</span>} />
      <Row label="Status" value={c.status} />
      <Row
        label="Ports"
        value={
          ports.length ? (
            <span className="flex flex-wrap gap-1">
              {ports.map((p, i) => (
                <span key={i} className="font-mono bg-panel rounded-sm px-1.5 py-0.5">{p.publicPort}→{p.privatePort}/{p.type}</span>
              ))}
            </span>
          ) : (
            <span className="text-muted">none published</span>
          )
        }
      />
    </div>
  );
}

function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex gap-2">
      <span className="text-muted w-14 shrink-0">{label}</span>
      <span className="min-w-0">{value}</span>
    </div>
  );
}
