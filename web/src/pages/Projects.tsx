import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import {
  FolderGit2, Plus, Rocket, Square, Trash2, X, FilePlus, FolderPlus, Upload, Loader2,
  ExternalLink, Save, FileText, FileBox, Folder, Terminal, Pencil, ChevronRight, Download, Search, CheckCircle2, AlertCircle, AlertTriangle, Eye, Boxes,
} from "lucide-react";
import { bytes as fmtBytes } from "../lib/format";
import { api, ApiError } from "../lib/api";
import type { Project, ProjectFile, Stack, ComposeModel, ComposeService } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner, StateBadge } from "../components/ui";
import { useDialogs } from "../components/Dialog";
// CodeMirror is ~440 KB — load it only when a project editor is actually opened.
const CodeEditor = lazy(() => import("../components/CodeEditor").then((m) => ({ default: m.CodeEditor })));
import { getPref, setPref } from "../lib/prefs";
import { useDockerEventTick } from "../lib/dockerEvents";

type Output = { title: string; text: string; ok: boolean };
type Kind = "deploy" | "down" | "restart";

function projectState(stack: Stack | undefined): { cls: string; label: string; deployed: boolean } {
  if (!stack) return { cls: "bg-muted/40", label: "Not deployed", deployed: false };
  const total = stack.containers.length;
  if (stack.running === 0) return { cls: "bg-danger text-danger", label: "Stopped", deployed: true };
  if (stack.running === total) return { cls: "bg-ok text-ok", label: "Running", deployed: true };
  return { cls: "bg-warn text-warn", label: "Partial", deployed: true };
}

type TreeNode = { name: string; path: string; isDir: boolean; binary?: boolean; children: TreeNode[] };

// buildTree turns the flat file list (paths like "config/app.conf") into a
// nested tree, materialising intermediate folders.
function buildTree(files: ProjectFile[]): TreeNode[] {
  const root: TreeNode = { name: "", path: "", isDir: true, children: [] };
  const dirs = new Map<string, TreeNode>([["", root]]);
  const ensureDir = (path: string): TreeNode => {
    const hit = dirs.get(path);
    if (hit) return hit;
    const slash = path.lastIndexOf("/");
    const parent = ensureDir(slash >= 0 ? path.slice(0, slash) : "");
    const node: TreeNode = { name: path.slice(slash + 1), path, isDir: true, children: [] };
    parent.children.push(node);
    dirs.set(path, node);
    return node;
  };
  for (const f of files) {
    const slash = f.name.lastIndexOf("/");
    if (f.isDir) { ensureDir(f.name); continue; }
    const parent = ensureDir(slash >= 0 ? f.name.slice(0, slash) : "");
    parent.children.push({ name: f.name.slice(slash + 1), path: f.name, isDir: false, binary: f.binary, children: [] });
  }
  const sort = (n: TreeNode) => {
    n.children.sort((a, b) => (a.isDir === b.isDir ? a.name.localeCompare(b.name) : a.isDir ? -1 : 1));
    n.children.forEach(sort);
  };
  sort(root);
  return root.children;
}

// TreeItem renders one tree node (folder or file) recursively.
function TreeItem({ node, depth, active, dirty, collapsed, currentDir, onToggle, onSelect, onEnterDir, onDelete }: {
  node: TreeNode; depth: number; active: string; dirty: boolean; currentDir: string;
  collapsed: Set<string>; onToggle: (path: string) => void;
  onSelect: (path: string) => void; onEnterDir: (path: string) => void; onDelete: (n: { name: string; isDir?: boolean }) => void;
}) {
  const pad = { paddingLeft: `${depth * 12 + 6}px` };
  if (node.isDir) {
    const open = !collapsed.has(node.path);
    const isCurrent = node.path === currentDir;
    return (
      <>
        {/* Clicking the row "enters" the folder (selects + expands); the chevron toggles collapse. */}
        <div className={`group flex items-center gap-1 rounded px-2 py-1 text-sm cursor-pointer ${isCurrent ? "bg-accent/10 text-accent" : "hover:bg-panel2"}`} style={pad} onClick={() => onEnterDir(node.path)}>
          <button className="shrink-0" title={open ? "Collapse" : "Expand"} onClick={(e) => { e.stopPropagation(); onToggle(node.path); }}>
            <ChevronRight className={`h-3.5 w-3.5 transition-transform ${open ? "rotate-90" : ""} ${isCurrent ? "text-accent" : "text-muted"}`} />
          </button>
          <Folder className={`h-3.5 w-3.5 shrink-0 ${isCurrent ? "text-accent" : "text-muted"}`} />
          <span className={`truncate font-mono text-xs ${isCurrent ? "" : "text-muted"}`}>{node.name}</span>
          <button className="ml-auto opacity-0 group-hover:opacity-100 text-danger" title="Delete folder" onClick={(e) => { e.stopPropagation(); onDelete({ name: node.path, isDir: true }); }}><Trash2 className="h-3.5 w-3.5" /></button>
        </div>
        {open && node.children.map((c) => (
          <TreeItem key={c.path} node={c} depth={depth + 1} active={active} dirty={dirty} collapsed={collapsed} currentDir={currentDir} onToggle={onToggle} onSelect={onSelect} onEnterDir={onEnterDir} onDelete={onDelete} />
        ))}
      </>
    );
  }
  const isActive = node.path === active;
  return (
    <div className={`group flex items-center gap-1 rounded px-2 py-1 text-sm cursor-pointer ${isActive ? "bg-accent/15 text-accent" : "hover:bg-panel2"}`} style={pad} onClick={() => onSelect(node.path)}>
      {node.binary
        ? <FileBox className="h-3.5 w-3.5 shrink-0 opacity-70" />
        : <FileText className="h-3.5 w-3.5 shrink-0 opacity-70" />}
      <span className="truncate font-mono text-xs">{node.name}</span>
      {dirty && isActive && <span className="text-warn text-xs">●</span>}
      <button className="ml-auto opacity-0 group-hover:opacity-100 text-danger" title="Delete file" onClick={(e) => { e.stopPropagation(); onDelete({ name: node.path, isDir: false }); }}><Trash2 className="h-3.5 w-3.5" /></button>
    </div>
  );
}

// isComposeFile reports whether a project file is a compose entry file that
// `docker compose` discovers by default (root-level compose.yaml/.yml,
// docker-compose.yaml/.yml and their .override variants), plus the project's
// configured compose file. Validation is scoped to these.
function isComposeFile(name: string, configured: string): boolean {
  if (!name) return false;
  if (name === configured) return true;
  if (name.includes("/")) return false; // compose files live at the project root
  return /^(docker-)?compose(\.override)?\.ya?ml$/.test(name.toLowerCase());
}

// portConflicts finds host ports published by more than one service in the
// resolved compose model.
function portConflicts(model: ComposeModel): string[] {
  const byKey = new Map<string, string[]>();
  for (const [name, svc] of Object.entries(model.services ?? {})) {
    for (const p of svc.ports ?? []) {
      if (!p.published) continue;
      const key = `${p.published}/${p.protocol ?? "tcp"}`;
      byKey.set(key, [...(byKey.get(key) ?? []), name]);
    }
  }
  const out: string[] = [];
  for (const [key, svcs] of byKey) {
    if (svcs.length > 1) out.push(`Host port ${key} is published by ${svcs.join(", ")}`);
  }
  return out;
}

function buildLabel(b: ComposeService["build"]): string {
  if (!b) return "";
  if (typeof b === "string") return `build: ${b}`;
  return b.context ? `build: ${b.context}` : "build";
}

// ComposeSummaryModal renders an overview of the resolved compose model:
// services with their image/ports/volumes, top-level resources, and any
// duplicate-host-port conflicts.
function ComposeSummaryModal({ model, onClose }: { model: ComposeModel; onClose: () => void }) {
  const conflicts = portConflicts(model);
  const services = Object.entries(model.services ?? {});
  const tops: [string, string[]][] = [
    ["Networks", Object.keys(model.networks ?? {})],
    ["Volumes", Object.keys(model.volumes ?? {})],
    ["Configs", Object.keys(model.configs ?? {})],
    ["Secrets", Object.keys(model.secrets ?? {})],
  ];
  return (
    <div className="fixed inset-0 z-[60] bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <div className="card w-[70vw] max-w-3xl max-h-[85vh] flex flex-col" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center gap-2 p-4 border-b border-border">
          <Boxes className="h-4 w-4 text-accent" />
          <span className="font-medium">Compose summary</span>
          {model.name && <span className="text-xs text-muted font-mono">{model.name}</span>}
          <button className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 overflow-auto space-y-3">
          {conflicts.length > 0 && (
            <div className="text-xs text-warn bg-warn/10 border border-warn/30 rounded-md p-2.5 space-y-1">
              {conflicts.map((c, i) => <div key={i}>⚠ {c}</div>)}
            </div>
          )}
          {services.length === 0 ? (
            <div className="text-sm text-muted">No services defined.</div>
          ) : services.map(([name, svc]) => (
            <div key={name} className="border border-border rounded-md p-3">
              <div className="flex items-center gap-2 flex-wrap">
                <span className="font-medium text-sm">{name}</span>
                <span className="text-xs text-muted font-mono break-all">{svc.image ?? buildLabel(svc.build)}</span>
                {svc.restart && <span className="text-[10px] bg-panel2 rounded px-1.5 py-0.5 text-muted">{svc.restart}</span>}
              </div>
              {!!svc.ports?.length && (
                <div className="mt-1.5 flex flex-wrap gap-1">
                  {svc.ports.map((p, i) => <span key={i} className="text-[10px] font-mono bg-accent/10 text-accent rounded px-1.5 py-0.5">{p.published ?? "?"}→{p.target}/{p.protocol ?? "tcp"}</span>)}
                </div>
              )}
              {!!svc.volumes?.length && (
                <div className="mt-1.5 space-y-0.5 text-[11px] text-muted font-mono">
                  {svc.volumes.map((v, i) => <div key={i} className="break-all">{typeof v === "string" ? v : `${v.source ?? v.type ?? "?"} → ${v.target}`}</div>)}
                </div>
              )}
            </div>
          ))}
          <div className="flex flex-wrap gap-x-6 gap-y-1.5 text-xs pt-1">
            {tops.filter(([, names]) => names.length > 0).map(([label, names]) => (
              <div key={label}><span className="text-muted">{label}:</span> <span className="font-mono">{names.join(", ")}</span></div>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}

// isDockerfile reports whether a file is a Dockerfile (Dockerfile, Dockerfile.*,
// *.dockerfile) — these can live in subdirectories, unlike compose files.
function isDockerfile(name: string): boolean {
  if (!name) return false;
  const base = (name.split("/").pop() ?? "").toLowerCase();
  return base === "dockerfile" || base.startsWith("dockerfile.") || base.endsWith(".dockerfile");
}

// downloadText triggers a client-side download of in-memory text.
function downloadText(name: string, content: string) {
  const url = URL.createObjectURL(new Blob([content], { type: "text/plain" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name.split("/").pop() || name;
  a.click();
  URL.revokeObjectURL(url);
}

export function Projects() {
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [composeAvailable, setComposeAvailable] = useState(true);
  const [stacks, setStacks] = useState<Stack[]>([]);
  const [busy, setBusy] = useState(""); // slug acting
  const [editing, setEditing] = useState<Project | null>(null);
  const [output, setOutput] = useState<Output | null>(null);
  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  const [query, setQuery] = useState(() => getPref<string>("projects.query", ""));
  const [showNew, setShowNew] = useState(false);
  const [searchParams, setSearchParams] = useSearchParams();
  const dialogs = useDialogs();
  const tick = useDockerEventTick();

  const load = useCallback(() => {
    api.projects().then((r) => { setProjects(r.projects); setComposeAvailable(r.composeAvailable); }).catch(() => setProjects([]));
    api.stacks().then(setStacks).catch(() => {});
  }, []);
  useEffect(() => load(), [load, tick]);

  const stackBySlug = useMemo(() => {
    const m = new Map<string, Stack>();
    for (const s of stacks) m.set(s.project, s);
    return m;
  }, [stacks]);

  // ?open=<slug> (from "Open in Projects") opens that project's editor.
  useEffect(() => {
    const open = searchParams.get("open");
    if (open && projects) {
      const p = projects.find((x) => x.slug === open);
      if (p) setEditing(p);
      setSearchParams({}, { replace: true });
    }
  }, [projects, searchParams, setSearchParams]);

  const toggleExpand = (id: number) => setExpanded((s) => { const n = new Set(s); n.has(id) ? n.delete(id) : n.add(id); return n; });

  const setSearch = (q: string) => { setQuery(q); setPref("projects.query", q); };
  const onCreated = (p: Project) => { setShowNew(false); load(); setEditing(p); };

  const rename = async (p: Project) => {
    const name = await dialogs.prompt({ title: "Rename project", label: "Display name (the identifier won't change)", defaultValue: p.name });
    if (!name || name === p.name) return;
    try { await api.renameProject(p.id, name); load(); }
    catch (e) { dialogs.alert({ title: "Rename failed", message: e instanceof Error ? e.message : "unknown error" }); }
  };

  const runCompose = async (p: Project, kind: Kind) => {
    setBusy(p.slug);
    try {
      const r = kind === "deploy" ? await api.deployProject(p.id, getPref<string[]>(`projects.profiles.${p.slug}`, []))
        : kind === "down" ? await api.downProject(p.id) : await api.restartProject(p.id);
      setOutput({ title: `${p.name} — ${kind}`, text: r.output || r.error || "(no output)", ok: r.ok });
      load();
    } catch (e) {
      setOutput({ title: `${p.name} — ${kind}`, text: e instanceof Error ? e.message : "failed", ok: false });
    } finally {
      setBusy("");
    }
  };

  const remove = async (p: Project) => {
    if (!(await dialogs.confirm({ title: `Delete project "${p.name}"?`, message: "This removes its folder and all files.", danger: true, confirmLabel: "Delete" }))) return;
    setBusy(p.slug);
    try {
      await api.deleteProject(p.id);
      load();
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        if (await dialogs.confirm({ title: `"${p.name}" is deployed`, message: "Run docker compose down and delete it anyway?", danger: true, confirmLabel: "Down & delete" })) {
          try { await api.deleteProject(p.id, true); load(); }
          catch (e2) { dialogs.alert({ title: "Delete failed", message: e2 instanceof Error ? e2.message : "unknown error" }); }
        }
      } else {
        dialogs.alert({ title: "Delete failed", message: e instanceof Error ? e.message : "unknown error" });
      }
    } finally {
      setBusy("");
    }
  };

  if (!projects)
    return (<><PageHeader title="Projects" /><div className="p-6 flex items-center gap-2 text-muted"><Spinner /> Loading…</div></>);

  const editStack = editing ? stackBySlug.get(editing.slug) : undefined;
  const q = query.trim().toLowerCase();
  const shown = q ? projects.filter((p) => p.name.toLowerCase().includes(q) || p.slug.toLowerCase().includes(q)) : projects;

  return (
    <>
      <PageHeader title="Projects" actions={<button className="btn-primary px-3 py-1.5 text-sm" onClick={() => setShowNew(true)}><Plus className="h-4 w-4" /> New project</button>} />
      <div className="p-6 space-y-3">
        {!composeAvailable && (
          <div className="card p-3 text-sm text-warn flex items-center gap-2">
            <Terminal className="h-4 w-4 shrink-0" />
            The <code>docker compose</code> CLI isn't available on the host running Docker Commander — you can edit files, but Deploy/Down are disabled.
          </div>
        )}
        <p className="text-xs text-muted">Projects are managed compose folders deployed with <strong>docker&nbsp;compose</strong> on the <strong>local</strong> Docker host. A deployed project also appears on the Stacks page.</p>

        {projects.length === 0 ? (
          <EmptyState title="No projects yet" hint="Create a project to edit a compose file plus its sidecar configs and scripts, then deploy it." />
        ) : (
          <>
            <div className="flex items-center gap-3">
              <div className="relative flex-1">
                <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted" />
                <input className="input pl-8 py-1.5" placeholder="Filter projects…" value={query} onChange={(e) => setSearch(e.target.value)} />
              </div>
              <span className="text-xs text-muted shrink-0">{shown.length} of {projects.length}</span>
            </div>
            {shown.length === 0 ? (
              <p className="text-sm text-muted">No projects match “{query}”.</p>
            ) : (
              shown.map((p) => {
            const stack = stackBySlug.get(p.slug);
            const st = projectState(stack);
            const acting = busy === p.slug;
            const isOpen = expanded.has(p.id);
            return (
              <div key={p.id} className="card p-4">
                <div className="flex items-center gap-3">
                  <button className="shrink-0 text-muted" onClick={() => toggleExpand(p.id)} title={isOpen ? "Collapse" : "Expand"}>
                    <ChevronRight className={`h-4 w-4 transition-transform ${isOpen ? "rotate-90" : ""}`} />
                  </button>
                  <span className={`h-2.5 w-2.5 rounded-full shrink-0 ${st.cls}`} style={st.deployed ? { boxShadow: "0 0 6px currentColor" } : undefined} title={st.label} />
                  <FolderGit2 className="h-4 w-4 text-accent shrink-0" />
                  <button className="min-w-0 text-left" onClick={() => setEditing(p)}>
                    <div className="font-medium truncate hover:text-accent">{p.name}</div>
                    <div className="text-xs text-muted font-mono truncate">{p.slug}{stack ? ` · ${stack.running}/${stack.containers.length} running` : ""}</div>
                  </button>
                  <div className="flex items-center gap-1 shrink-0 ml-auto">
                    {acting ? <Loader2 className="h-4 w-4 animate-spin text-muted" /> : (
                      <>
                        <button className="btn-ghost px-2 py-1" title="Edit files" onClick={() => setEditing(p)}><FileText className="h-4 w-4" /></button>
                        <button className="btn-ghost px-2 py-1" title="Rename" onClick={() => rename(p)}><Pencil className="h-4 w-4" /></button>
                        {st.deployed ? (
                          <>
                            <button className="btn-ghost px-2 py-1 text-accent disabled:opacity-40" title="Redeploy (docker compose up -d)" disabled={!composeAvailable} onClick={() => runCompose(p, "deploy")}><Rocket className="h-4 w-4" /></button>
                            <button className="btn-ghost px-2 py-1 disabled:opacity-40" title="Down" disabled={!composeAvailable} onClick={() => runCompose(p, "down")}><Square className="h-4 w-4" /></button>
                            <Link className="btn-ghost px-2 py-1" title="Open in Stacks" to={`/stacks?focus=${encodeURIComponent(p.slug)}`}><ExternalLink className="h-4 w-4" /></Link>
                          </>
                        ) : (
                          <button className="btn-ghost px-2 py-1 text-accent disabled:opacity-40" title={composeAvailable ? "Deploy" : "docker compose CLI not available"} disabled={!composeAvailable} onClick={() => runCompose(p, "deploy")}><Rocket className="h-4 w-4" /></button>
                        )}
                        <button className="btn-ghost px-2 py-1 text-danger" title="Delete project" onClick={() => remove(p)}><Trash2 className="h-4 w-4" /></button>
                      </>
                    )}
                  </div>
                </div>

                {isOpen && (
                  <div className="mt-3 rounded-lg border border-border">
                    {!stack ? (
                      <div className="px-3 py-2 text-sm text-muted">Not deployed.</div>
                    ) : (
                      stack.containers.map((c, i) => (
                        <div key={c.id} className={`flex items-center gap-3 px-3 py-2 text-sm ${i > 0 ? "border-t border-border" : ""}`}>
                          <span className="w-28 shrink-0 font-medium truncate">{c.service || "—"}</span>
                          <StateBadge state={c.state} />
                          <Link to={`/containers/${c.id}`} className="text-muted hover:text-accent truncate">{c.name}</Link>
                          <span className="ml-auto flex flex-wrap gap-1 justify-end">
                            {(c.ports ?? []).filter((pt) => pt.publicPort).map((pt, j) => (
                              <span key={j} className="font-mono text-xs bg-panel2 rounded px-1.5 py-0.5">{pt.publicPort}→{pt.privatePort}/{pt.type}</span>
                            ))}
                          </span>
                        </div>
                      ))
                    )}
                  </div>
                )}
              </div>
            );
          })
            )}
          </>
        )}
      </div>

      {showNew && <NewProjectModal onClose={() => setShowNew(false)} onCreated={onCreated} />}

      {editing && (
        <ProjectEditor
          project={editing}
          composeAvailable={composeAvailable}
          deployed={!!editStack}
          onClose={() => { setEditing(null); load(); }}
          onOutput={(o) => { setOutput(o); load(); }}
        />
      )}

      {output && (
        <div className="fixed inset-0 z-[55] bg-black/60 grid place-items-center p-6" onClick={() => setOutput(null)}>
          <div className="card w-[70vw] max-h-[80vh] flex flex-col" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center gap-3 p-4 border-b border-border">
              <Terminal className={`h-4 w-4 shrink-0 ${output.ok ? "text-ok" : "text-danger"}`} />
              <div className="font-medium">{output.title}</div>
              <span className={`text-xs ${output.ok ? "text-ok" : "text-danger"}`}>{output.ok ? "ok" : "failed"}</span>
              <button className="btn-ghost px-2 py-1.5 ml-auto" onClick={() => setOutput(null)}><X className="h-4 w-4" /></button>
            </div>
            <div className="p-4 overflow-auto"><pre className="text-xs font-mono whitespace-pre-wrap bg-panel2 rounded-lg p-3">{output.text}</pre></div>
          </div>
        </div>
      )}
    </>
  );
}

// NewProjectModal creates a project from a name, optionally importing a .zip.
function NewProjectModal({ onClose, onCreated }: { onClose: () => void; onCreated: (p: Project) => void }) {
  const [name, setName] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const n = name.trim();
    if (!n) return;
    setBusy(true); setErr("");
    try {
      const r = file ? await api.importProject(n, file) : await api.createProject(n);
      onCreated({ id: r.id, name: n, slug: r.slug, composeFile: "compose.yml", createdBy: "", createdAt: "", updatedAt: "" });
    } catch (e2) {
      setErr(e2 instanceof ApiError ? e2.message : "could not create project");
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-[55] bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <form className="card w-full max-w-md flex flex-col" onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <FolderGit2 className="h-4 w-4 text-accent" />
          <div className="font-medium">New project</div>
          <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 space-y-3">
          <label className="block">
            <span className="label">Project name</span>
            <input autoFocus className="input" value={name} placeholder="My app" onChange={(e) => setName(e.target.value)} />
          </label>
          <label className="block">
            <span className="label">Import from .zip (optional)</span>
            <input type="file" accept=".zip,application/zip" className="input py-1.5" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
            {file && <span className="text-xs text-muted">Will import {file.name}</span>}
          </label>
          {err && <p className="text-sm text-danger">{err}</p>}
        </div>
        <div className="flex justify-end gap-2 p-4 border-t border-border">
          <button type="button" className="btn-ghost px-3 py-1.5 text-sm" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn-primary px-3 py-1.5 text-sm disabled:opacity-40" disabled={!name.trim() || busy}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />} {file ? "Import" : "Create"}
          </button>
        </div>
      </form>
    </div>
  );
}

// ProjectEditor is a multi-file editor over the project folder.
function ProjectEditor({ project, composeAvailable, deployed, onClose, onOutput }: {
  project: Project; composeAvailable: boolean; deployed: boolean; onClose: () => void; onOutput: (o: Output) => void;
}) {
  const [files, setFiles] = useState<ProjectFile[] | null>(null);
  const [active, setActive] = useState<string>("");
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState("");
  const [profiles, setProfiles] = useState<string[]>([]);
  const [selectedProfiles, setSelectedProfiles] = useState<string[]>(() => getPref<string[]>(`projects.profiles.${project.slug}`, []));
  const [collapsedDirs, setCollapsedDirs] = useState<Set<string>>(new Set());
  const [currentDir, setCurrentDir] = useState(""); // where New file/folder land
  const [liveVal, setLiveVal] = useState<{ status: "idle" | "checking" | "ok" | "error"; message?: string; warnings?: string[] }>({ status: "idle" });
  const [summary, setSummary] = useState<ComposeModel | null>(null);
  const valSeq = useRef(0);
  const dialogs = useDialogs();
  const uploadRef = useRef<HTMLInputElement>(null);

  const dirOf = (path: string) => (path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : "");

  useEffect(() => { api.projectProfiles(project.id).then((r) => setProfiles(r.profiles)).catch(() => {}); }, [project.id]);

  // Live compose validation: validation is a property of the compose file, so it
  // only runs while that file is open. Debounce edits and validate the *unsaved*
  // buffer server-side (the same `docker compose config` used to deploy —
  // anchors/merge keys/interpolation resolved), no save required.
  const composeFileName = project.composeFile || "compose.yml";
  useEffect(() => {
    if (!composeAvailable || !isComposeFile(active, composeFileName)) {
      setLiveVal({ status: "idle" });
      return;
    }
    const seq = ++valSeq.current;
    const t = setTimeout(() => {
      setLiveVal((v) => ({ ...v, status: "checking" }));
      api.validateProject(project.id, { name: active, content: draft })
        .then((r) => { if (seq === valSeq.current) setLiveVal(r.unavailable ? { status: "idle" } : r.valid ? { status: "ok", warnings: r.warnings } : { status: "error", message: r.error }); })
        .catch(() => { if (seq === valSeq.current) setLiveVal({ status: "idle" }); });
    }, 800);
    return () => clearTimeout(t);
  }, [active, draft, composeAvailable, project.id, composeFileName]);
  const toggleProfile = (p: string) => setSelectedProfiles((s) => {
    const next = s.includes(p) ? s.filter((x) => x !== p) : [...s, p];
    setPref(`projects.profiles.${project.slug}`, next);
    return next;
  });
  const toggleDir = (path: string) => setCollapsedDirs((s) => { const n = new Set(s); n.has(path) ? n.delete(path) : n.add(path); return n; });
  // Entering a folder selects it as the target and ensures it's expanded, so a
  // new file created in it is actually visible.
  const enterDir = (path: string) => {
    setCurrentDir(path);
    setCollapsedDirs((s) => { if (!s.has(path)) return s; const n = new Set(s); n.delete(path); return n; });
  };

  const original = files?.find((f) => f.name === active)?.content ?? "";
  const dirty = files != null && active !== "" && draft !== original;

  const loadFiles = useCallback((select?: string) => {
    return api.projectFiles(project.id).then((fs) => {
      setFiles(fs);
      setActive((cur) => {
        const want = select ?? cur;
        const pick = fs.find((f) => !f.isDir && f.name === want) ?? fs.find((f) => f.name === "compose.yml") ?? fs.find((f) => !f.isDir);
        if (pick) setDraft(pick.content);
        return pick?.name ?? "";
      });
      return fs;
    }).catch(() => { setFiles([]); return [] as ProjectFile[]; });
  }, [project.id]);
  useEffect(() => { loadFiles(); }, [loadFiles]);

  const select = async (name: string) => {
    if (name === active) return;
    if (dirty && !(await dialogs.confirm({ title: "Discard unsaved changes?", message: "This file has unsaved edits.", danger: true, confirmLabel: "Discard" }))) return;
    setActive(name);
    setCurrentDir(dirOf(name)); // new files land next to the one you opened
    setDraft(files?.find((x) => x.name === name)?.content ?? "");
  };

  const save = async () => {
    setBusy("save");
    try { await api.writeProjectFile(project.id, active, draft); loadFiles(active); }
    catch (e) { dialogs.alert({ title: "Save failed", message: e instanceof Error ? e.message : "unknown error" }); }
    finally { setBusy(""); }
  };

  const addFile = async () => {
    const name = await dialogs.prompt({ title: "New file", label: currentDir ? `File name (in ${currentDir}/)` : "File name", placeholder: "nginx.conf or scripts/init.sh" });
    if (!name) return;
    const full = currentDir ? `${currentDir}/${name}` : name;
    setBusy("add");
    try { await api.writeProjectFile(project.id, full, ""); loadFiles(full); }
    catch (e) { dialogs.alert({ title: "Could not add file", message: e instanceof Error ? e.message : "unknown error" }); }
    finally { setBusy(""); }
  };

  const addDir = async () => {
    const name = await dialogs.prompt({ title: "New folder", label: currentDir ? `Folder name (in ${currentDir}/)` : "Folder name", placeholder: "config" });
    if (!name) return;
    const full = currentDir ? `${currentDir}/${name}` : name;
    setBusy("dir");
    try { await api.makeProjectDir(project.id, full); setCurrentDir(full); loadFiles(); }
    catch (e) { dialogs.alert({ title: "Could not create folder", message: e instanceof Error ? e.message : "unknown error" }); }
    finally { setBusy(""); }
  };

  const removeEntry = async (f: { name: string; isDir?: boolean }) => {
    if (!(await dialogs.confirm({ title: `Delete ${f.isDir ? "folder" : "file"}`, message: <>Really delete <code className="font-mono text-text">{f.name}</code>?</>, danger: true, confirmLabel: "Delete" }))) return;
    setBusy("del");
    try {
      await api.deleteProjectFile(project.id, f.name);
      const fs = await loadFiles(f.name === active ? undefined : active);
      // Offer to delete the whole project once it has no editable files left.
      if (!fs.some((x) => !x.isDir)) {
        if (await dialogs.confirm({ title: "Delete project?", message: "That was the last file — this project is now empty. Delete the whole project?", danger: true, confirmLabel: "Delete project" })) {
          setBusy("delproj");
          try { await api.deleteProject(project.id); onClose(); }
          catch (e) { dialogs.alert({ title: "Could not delete project", message: e instanceof ApiError ? e.message : e instanceof Error ? e.message : "unknown error" }); }
        }
      }
    } catch (e) {
      dialogs.alert({ title: "Delete failed", message: e instanceof Error ? e.message : "unknown error (folders must be empty)" });
    } finally { setBusy(""); }
  };

  const upload = async (file: File) => {
    const full = currentDir ? `${currentDir}/${file.name}` : file.name;
    setBusy("upload");
    // Raw octet-stream upload preserves bytes for binary/data files (and works
    // for text too). loadFiles re-reads, so a text file becomes editable.
    try { await api.uploadProjectFile(project.id, full, file); loadFiles(full); }
    catch (e) { dialogs.alert({ title: "Upload failed", message: e instanceof Error ? e.message : "unknown error" }); }
    finally { setBusy(""); if (uploadRef.current) uploadRef.current.value = ""; }
  };

  // downloadActive saves the currently-selected file: binary/too-large files
  // stream from the server (raw bytes), text files download the in-memory draft.
  const downloadActive = () => {
    if (activeFile?.binary || activeFile?.tooLarge) {
      const a = document.createElement("a");
      a.href = api.projectFileDownloadUrl(project.id, active);
      a.click();
    } else {
      downloadText(active, draft);
    }
  };

  const runCompose = async (kind: Kind) => {
    if (dirty && !(await dialogs.confirm({ title: "Unsaved changes", message: "Continue with the last saved files?", confirmLabel: "Continue" }))) return;
    setBusy(kind);
    try {
      const r = kind === "deploy" ? await api.deployProject(project.id, selectedProfiles) : kind === "down" ? await api.downProject(project.id) : await api.restartProject(project.id);
      onOutput({ title: `${project.name} — ${kind}`, text: r.output || r.error || "(no output)", ok: r.ok });
    } catch (e) {
      onOutput({ title: `${project.name} — ${kind}`, text: e instanceof Error ? e.message : "failed", ok: false });
    } finally { setBusy(""); }
  };

  // validate runs `docker compose config` server-side (the real deploy parser,
  // so YAML anchors/merge keys/interpolation resolve as they will at up time)
  // and shows the result in the shared output panel.
  const validate = async () => {
    setBusy("validate");
    try {
      // Validate the current (possibly unsaved) editor state — overlay the
      // active file if one is open, else the on-disk project.
      const r = await api.validateProject(project.id, active ? { name: active, content: draft } : undefined);
      onOutput({ title: `${project.name} — validate`, text: r.valid ? "✓ compose file is valid" : (r.error || "invalid compose file"), ok: r.valid });
    } catch (e) {
      onOutput({ title: `${project.name} — validate`, text: e instanceof Error ? e.message : "failed", ok: false });
    } finally { setBusy(""); }
  };

  // openSummary fetches the resolved compose model and shows the overview modal.
  const openSummary = async () => {
    setBusy("summary");
    try {
      const r = await api.projectSummary(project.id, active ? { name: active, content: draft } : undefined);
      if (r.ok && r.model) setSummary(r.model);
      else onOutput({ title: `${project.name} — summary`, text: r.error || "could not parse compose", ok: false });
    } catch (e) {
      onOutput({ title: `${project.name} — summary`, text: e instanceof Error ? e.message : "failed", ok: false });
    } finally { setBusy(""); }
  };

  // showResolved displays the fully-resolved compose (anchors/interpolation/
  // extends flattened — what `up` actually deploys) in the output panel.
  const showResolved = async () => {
    setBusy("resolve");
    try {
      const r = await api.resolveProject(project.id, active ? { name: active, content: draft } : undefined);
      onOutput({ title: `${project.name} — resolved compose`, text: r.ok ? (r.config || "(empty)") : (r.error || "failed"), ok: r.ok });
    } catch (e) {
      onOutput({ title: `${project.name} — resolved compose`, text: e instanceof Error ? e.message : "failed", ok: false });
    } finally { setBusy(""); }
  };

  // checkDockerfile lints the active Dockerfile (unsaved buffer) via
  // `docker build --check`; results go to the shared output panel. It's a button
  // (not live) because the check resolves base-image metadata from the registry.
  const checkDockerfile = async () => {
    setBusy("dfcheck");
    try {
      const r = await api.checkDockerfile(project.id, draft);
      onOutput({ title: `${active} — check`, text: r.valid ? (r.output || "✓ no issues found") : (r.error || "issues found"), ok: r.valid });
    } catch (e) {
      onOutput({ title: `${active} — check`, text: e instanceof Error ? e.message : "failed", ok: false });
    } finally { setBusy(""); }
  };

  const activeFile = files?.find((f) => f.name === active);
  const onComposeFile = isComposeFile(active, composeFileName); // validation belongs to the compose file(s)
  const onDockerfile = isDockerfile(active);

  return (
    <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <div className="card relative w-[85vw] h-[85vh] flex flex-col" onClick={(e) => e.stopPropagation()}>
        {busy === "delproj" && (
          <div className="absolute inset-0 z-10 bg-bg/70 grid place-items-center rounded-xl">
            <div className="flex items-center gap-2 text-sm text-muted"><Loader2 className="h-4 w-4 animate-spin" /> Deleting project…</div>
          </div>
        )}
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <FolderGit2 className="h-4 w-4 text-accent shrink-0" />
          <div className="min-w-0">
            <div className="font-medium truncate">{project.name}</div>
            <div className="text-xs text-muted font-mono">{project.slug}</div>
          </div>
          <div className="flex items-center gap-1 ml-auto">
            <a className="btn-ghost px-2 h-8" title="Download project as .zip" href={api.projectDownloadUrl(project.id)}><Download className="h-4 w-4" /></a>
            <button className="btn-primary px-3 h-8 text-sm disabled:opacity-40" disabled={!composeAvailable || busy === "deploy"} onClick={() => runCompose("deploy")} title={composeAvailable ? "docker compose up -d" : "docker compose CLI not available"}>
              {busy === "deploy" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Rocket className="h-4 w-4" />} {deployed ? "Redeploy" : "Deploy"}
            </button>
            <button className="btn-ghost px-3 h-8 text-sm disabled:opacity-40" disabled={!composeAvailable || !deployed || busy === "down"} onClick={() => runCompose("down")} title={deployed ? "docker compose down" : "not deployed"}>Down</button>
            <button className="btn-ghost px-2 h-8" onClick={onClose}><X className="h-4 w-4" /></button>
          </div>
        </div>

        {profiles.length > 0 && (
          <div className="flex items-center gap-2 px-4 py-2 border-b border-border text-xs flex-wrap">
            <span className="text-muted">Profiles:</span>
            {profiles.map((pr) => {
              const on = selectedProfiles.includes(pr);
              return (
                <button key={pr} onClick={() => toggleProfile(pr)} className={`px-2 py-0.5 rounded-md border font-mono ${on ? "border-accent text-accent bg-accent/10" : "border-border text-muted hover:text-text"}`}>{pr}</button>
              );
            })}
            <span className="text-muted/60">— applied on Deploy</span>
          </div>
        )}

        <div className="flex-1 flex min-h-0">
          {/* File tree */}
          <div className="w-56 shrink-0 border-r border-border flex flex-col">
            <div className="flex items-center gap-1 p-2 border-b border-border">
              <span className="text-xs uppercase tracking-wide text-muted px-1">Files</span>
              <button className="btn-ghost px-1.5 py-1 ml-auto" title={`New file${currentDir ? ` in ${currentDir}/` : ""}`} onClick={addFile}><FilePlus className="h-4 w-4" /></button>
              <button className="btn-ghost px-1.5 py-1" title={`New folder${currentDir ? ` in ${currentDir}/` : ""}`} onClick={addDir}><FolderPlus className="h-4 w-4" /></button>
              <button className="btn-ghost px-1.5 py-1" title="Upload file" onClick={() => uploadRef.current?.click()}><Upload className="h-4 w-4" /></button>
              <input ref={uploadRef} type="file" className="hidden" onChange={(e) => e.target.files?.[0] && upload(e.target.files[0])} />
            </div>
            {currentDir && (
              <div className="flex items-center gap-1 px-2 py-1 border-b border-border text-[11px] text-accent font-mono" title={`New items are created in ${currentDir}/`}>
                <Folder className="h-3 w-3 shrink-0" />
                <span className="truncate">{currentDir}/</span>
                <button className="ml-auto text-muted hover:text-text" title="Create in the project root" onClick={() => setCurrentDir("")}><X className="h-3 w-3" /></button>
              </div>
            )}
            <div className="flex-1 overflow-auto p-1">
              {files === null ? <div className="p-3 text-muted text-sm flex items-center gap-2"><Spinner /> …</div> :
                files.length === 0 ? <div className="p-3 text-muted text-xs">No files</div> :
                buildTree(files).map((n) => (
                  <TreeItem key={n.path} node={n} depth={0} active={active} dirty={dirty} collapsed={collapsedDirs} currentDir={currentDir} onToggle={toggleDir} onSelect={select} onEnterDir={enterDir} onDelete={removeEntry} />
                ))}
            </div>
          </div>

          {/* Editor */}
          <div className="flex-1 flex flex-col min-w-0">
            <div className="flex items-center gap-2 p-2 border-b border-border">
              <span className="text-xs font-mono text-muted truncate">{active || "—"}</span>
              {liveVal.status !== "idle" && (
                <span className="text-[11px] flex items-center gap-1 shrink-0" title={liveVal.message ?? liveVal.warnings?.join("\n")}>
                  {liveVal.status === "checking" ? (
                    <><Loader2 className="h-3 w-3 animate-spin text-muted" /><span className="text-muted">checking…</span></>
                  ) : liveVal.status === "ok" && liveVal.warnings?.length ? (
                    <><AlertTriangle className="h-3 w-3 text-warn" /><span className="text-warn">{liveVal.warnings.length} warning{liveVal.warnings.length === 1 ? "" : "s"}</span></>
                  ) : liveVal.status === "ok" ? (
                    <><CheckCircle2 className="h-3 w-3 text-ok" /><span className="text-ok">valid</span></>
                  ) : (
                    <><AlertCircle className="h-3 w-3 text-danger" /><span className="text-danger">invalid</span></>
                  )}
                </span>
              )}
              <div className="ml-auto flex items-center gap-1">
                {onComposeFile && (
                  <button className="btn-ghost px-2 py-1 text-xs disabled:opacity-40" disabled={!composeAvailable || busy === "validate"} onClick={validate} title={composeAvailable ? "Re-validate (docker compose config)" : "docker compose CLI not available"}>
                    {busy === "validate" ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <CheckCircle2 className="h-3.5 w-3.5" />} Validate
                  </button>
                )}
                {onComposeFile && (
                  <button className="btn-ghost px-2 py-1 text-xs disabled:opacity-40" disabled={!composeAvailable || busy === "resolve"} onClick={showResolved} title="Show the fully-resolved compose (anchors/interpolation/extends flattened)">
                    {busy === "resolve" ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Eye className="h-3.5 w-3.5" />} Resolved
                  </button>
                )}
                {onComposeFile && (
                  <button className="btn-ghost px-2 py-1 text-xs disabled:opacity-40" disabled={!composeAvailable || busy === "summary"} onClick={openSummary} title="Services / ports / volumes overview + port-conflict check">
                    {busy === "summary" ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Boxes className="h-3.5 w-3.5" />} Summary
                  </button>
                )}
                {onDockerfile && (
                  <button className="btn-ghost px-2 py-1 text-xs disabled:opacity-40" disabled={busy === "dfcheck"} onClick={checkDockerfile} title="Lint this Dockerfile (docker build --check)">
                    {busy === "dfcheck" ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <CheckCircle2 className="h-3.5 w-3.5" />} Check
                  </button>
                )}
                <button className="btn-ghost px-2 py-1 text-xs disabled:opacity-40" disabled={!active} title="Download this file" onClick={downloadActive}><Download className="h-3.5 w-3.5" /></button>
                <button className="btn-primary px-3 py-1 text-xs disabled:opacity-40" disabled={!dirty || busy === "save" || !active || activeFile?.binary} onClick={save}>
                  {busy === "save" ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />} Save
                </button>
              </div>
            </div>
            {liveVal.status === "error" && liveVal.message && (
              <div className="px-3 py-1.5 text-xs text-danger bg-danger/10 border-b border-danger/30 font-mono whitespace-pre-wrap break-words max-h-24 overflow-auto">{liveVal.message}</div>
            )}
            {liveVal.status === "ok" && !!liveVal.warnings?.length && (
              <div className="px-3 py-1.5 text-xs text-warn bg-warn/10 border-b border-warn/30 max-h-24 overflow-auto">
                {liveVal.warnings.map((wmsg, i) => <div key={i} className="break-words">⚠ {wmsg}</div>)}
              </div>
            )}
            {activeFile?.tooLarge ? (
              <div className="p-4 text-sm text-muted">This file is too large to edit here.</div>
            ) : activeFile?.binary ? (
              <div className="p-4 flex flex-col items-start gap-3 text-sm text-muted">
                <div className="flex items-center gap-2"><FileBox className="h-4 w-4" /> Binary file ({fmtBytes(activeFile.size)}) — can't be edited as text.</div>
                <button className="btn-ghost px-3 py-1.5 text-xs" onClick={downloadActive}><Download className="h-3.5 w-3.5" /> Download</button>
              </div>
            ) : active ? (
              <div className="flex-1 min-h-0 overflow-hidden">
                <Suspense fallback={<div className="h-full grid place-items-center text-muted"><Spinner /></div>}>
                  <CodeEditor filename={active} value={draft} onChange={setDraft} />
                </Suspense>
              </div>
            ) : (
              <div className="flex-1 grid place-items-center text-sm text-muted">Select or add a file</div>
            )}
          </div>
        </div>
      </div>
      {summary && <ComposeSummaryModal model={summary} onClose={() => setSummary(null)} />}
    </div>
  );
}
