import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router-dom";
import clsx from "clsx";
import {
  FolderGit2, Plus, Rocket, Square, Trash2, X, FilePlus, FolderPlus, Upload, Loader2,
  ExternalLink, Save, FileText, FileBox, Folder, Terminal, Pencil, ChevronRight, Download, Search, CheckCircle2, AlertCircle, AlertTriangle, Eye, Boxes,
  LayoutTemplate, Puzzle, KeyRound, Anchor,
} from "lucide-react";
import { bytes as fmtBytes } from "../lib/format";
import { api, ApiError } from "../lib/api";
import type { Project, ProjectFile, Stack, ComposeModel, ComposeService, ProjectTemplateMeta, ServiceBlockMeta, ComposeFragmentMeta, TemplateRef, TemplateVariable } from "../lib/types";
import type { ServerCheck } from "../components/CodeEditor";
import { buildTree, TreeItem } from "../components/FileTree";
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
// SaveAsTemplateModal snapshots an existing project's files into a reusable
// user preset (shows up under New project → Template and on the Templates page).
function SaveAsTemplateModal({ projectId, onClose, onSaved }: { projectId: number; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setBusy(true); setErr("");
    try {
      await api.saveProjectAsTemplate(projectId, name.trim(), description.trim());
      onSaved();
    } catch (e2) {
      setErr(e2 instanceof ApiError ? e2.message : "could not save template");
      setBusy(false);
    }
  };
  return (
    <div className="fixed inset-0 z-[60] bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <form className="card w-full max-w-lg flex flex-col" onClick={(e) => e.stopPropagation()} onSubmit={save}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <LayoutTemplate className="h-4 w-4 text-accent" />
          <div className="font-medium">Save as preset</div>
          <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 space-y-3">
          <p className="text-xs text-muted">Snapshots this project’s files as a reusable preset — listed under <b>New project → Template</b> and on the <b>Templates</b> page.</p>
          <label className="block"><span className="label">Preset name</span><input autoFocus className="input" value={name} placeholder="My stack" onChange={(e) => setName(e.target.value)} /></label>
          <label className="block"><span className="label">Description</span><input className="input" value={description} placeholder="What it sets up" onChange={(e) => setDescription(e.target.value)} /></label>
          {err && <p className="text-sm text-danger">{err}</p>}
        </div>
        <div className="flex justify-end gap-2 p-4 border-t border-border">
          <button type="button" className="btn-ghost px-3 py-1.5 text-sm" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn-primary px-3 py-1.5 text-sm disabled:opacity-40" disabled={!name.trim() || busy}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />} Save
          </button>
        </div>
      </form>
    </div>
  );
}

const refKey = (m: { id: string; source: string }) => `${m.source}:${m.id}`;

function dedupeVars(vars: TemplateVariable[]): TemplateVariable[] {
  const seen = new Set<string>();
  const out: TemplateVariable[] = [];
  for (const v of vars) if (!seen.has(v.key)) { seen.add(v.key); out.push(v); }
  return out;
}

// VarFields renders the fill-in form for a preset's / block selection's variables.
function VarFields({ vars, values, onChange }: { vars: TemplateVariable[]; values: Record<string, string>; onChange: (k: string, v: string) => void }) {
  if (!vars.length) return null;
  return (
    <div className="space-y-2 rounded-lg border border-border bg-panel2/40 p-3">
      <div className="flex items-center gap-2 text-xs text-muted"><KeyRound className="h-3.5 w-3.5" /> Variables</div>
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
        {vars.map((v) => (
          <label key={v.key} className="block">
            <span className="label">{v.label}{v.secret ? <span className="text-muted"> (secret)</span> : null}</span>
            <input
              className="input"
              type={v.secret ? "password" : "text"}
              autoComplete={v.secret ? "new-password" : "off"}
              value={values[v.key] ?? ""}
              placeholder={v.default || (v.generate === "password" ? "auto-generated" : "")}
              onChange={(e) => onChange(v.key, e.target.value)}
            />
          </label>
        ))}
      </div>
    </div>
  );
}

// CustomBlockForm lets the user add their own service block to the builder.
function CustomBlockForm({ onClose, onSaved }: { onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState("");
  const [service, setService] = useState("");
  const [yaml, setYaml] = useState("");
  const [vols, setVols] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const save = async () => {
    if (!name.trim() || !service.trim() || !yaml.trim()) { setErr("name, service key and YAML are required"); return; }
    setBusy(true); setErr("");
    try {
      await api.createServiceBlock({
        name: name.trim(), description: "", service: service.trim(), serviceYaml: yaml,
        volumes: vols.split(",").map((s) => s.trim()).filter(Boolean),
      });
      onSaved();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : "could not save block");
      setBusy(false);
    }
  };
  return (
    <div className="space-y-2 rounded-lg border border-border bg-panel2/40 p-3">
      <div className="flex items-center gap-2 text-xs font-medium"><Plus className="h-3.5 w-3.5 text-accent" /> Custom service block</div>
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
        <label className="block"><span className="label">Name</span><input className="input" value={name} placeholder="My worker" onChange={(e) => setName(e.target.value)} /></label>
        <label className="block"><span className="label">Service key</span><input className="input font-mono" value={service} placeholder="worker" onChange={(e) => setService(e.target.value)} /></label>
      </div>
      <label className="block">
        <span className="label">Service YAML (indented under <code>services:</code>)</span>
        <textarea className="input font-mono text-xs" rows={5} value={yaml} placeholder={"  worker:\n    image: alpine\n    command: [\"sleep\", \"infinity\"]"} onChange={(e) => setYaml(e.target.value)} />
      </label>
      <label className="block"><span className="label">Named volumes (comma-separated, optional)</span><input className="input font-mono" value={vols} placeholder="workerdata" onChange={(e) => setVols(e.target.value)} /></label>
      {err && <p className="text-sm text-danger">{err}</p>}
      <div className="flex justify-end gap-2">
        <button type="button" className="btn-ghost px-3 py-1.5 text-sm" onClick={onClose}>Cancel</button>
        <button type="button" className="btn-primary px-3 py-1.5 text-sm disabled:opacity-40" disabled={busy} onClick={save}>
          {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />} Save block
        </button>
      </div>
    </div>
  );
}

// CustomFragmentForm saves a shared definition (a top-level YAML anchor) for the
// builder. The content is copied literally, so anchors/merge keys are preserved.
function CustomFragmentForm({ onClose, onSaved }: { onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState("");
  const [content, setContent] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const save = async () => {
    if (!name.trim() || !content.trim()) { setErr("name and YAML are required"); return; }
    setBusy(true); setErr("");
    try {
      await api.createComposeFragment({ name: name.trim(), description: "", content });
      onSaved();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : "could not save definition");
      setBusy(false);
    }
  };
  return (
    <div className="space-y-2 rounded-lg border border-border bg-panel2/40 p-3">
      <div className="flex items-center gap-2 text-xs font-medium"><Anchor className="h-3.5 w-3.5 text-accent" /> Shared definition</div>
      <label className="block"><span className="label">Name</span><input className="input" value={name} placeholder="Postgres security" onChange={(e) => setName(e.target.value)} /></label>
      <label className="block">
        <span className="label">Top-level YAML (define an anchor with <code>&amp;name</code>)</span>
        <textarea className="input font-mono text-xs" rows={6} value={content} placeholder={"x-pg-common: &pg-common\n  restart: unless-stopped\n  volumes:\n    - ./certs:/certs:ro"} onChange={(e) => setContent(e.target.value)} />
      </label>
      {err && <p className="text-sm text-danger">{err}</p>}
      <div className="flex justify-end gap-2">
        <button type="button" className="btn-ghost px-3 py-1.5 text-sm" onClick={onClose}>Cancel</button>
        <button type="button" className="btn-primary px-3 py-1.5 text-sm disabled:opacity-40" disabled={busy} onClick={save}>
          {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />} Save definition
        </button>
      </div>
    </div>
  );
}

type NewMode = "template" | "builder" | "import";
type BuilderTab = "services" | "shared" | "variables";
// One service placed in the builder: a block under a chosen key, plus which
// shared definitions (by refKey) to merge into it via `<<: *anchor`.
interface BuilderInstance { uid: number; block: ServiceBlockMeta; key: string; merge: Record<string, boolean>; }

// dedupeKey makes a service key unique among the ones already used (db, db-2, …).
function dedupeKey(base: string, taken: string[]): string {
  const b = (base || "service").trim() || "service";
  if (!taken.includes(b)) return b;
  for (let i = 2; ; i++) if (!taken.includes(`${b}-${i}`)) return `${b}-${i}`;
}

function NewProjectModal({ onClose, onCreated }: { onClose: () => void; onCreated: (p: Project) => void }) {
  const dialogs = useDialogs();
  const [name, setName] = useState("");
  const [mode, setMode] = useState<NewMode>("template");
  const [templates, setTemplates] = useState<ProjectTemplateMeta[]>([]);
  const [blocks, setBlocks] = useState<ServiceBlockMeta[]>([]);
  const [fragments, setFragments] = useState<ComposeFragmentMeta[]>([]);
  const [tplKey, setTplKey] = useState("");                 // "" = empty starter
  const [instances, setInstances] = useState<BuilderInstance[]>([]);
  const [pickedFrags, setPickedFrags] = useState<Record<string, boolean>>({});
  const uidRef = useRef(0);
  const [vars, setVars] = useState<Record<string, string>>({});
  const [file, setFile] = useState<File | null>(null);
  const [customOpen, setCustomOpen] = useState(false);
  const [customFragOpen, setCustomFragOpen] = useState(false);
  const [builderTab, setBuilderTab] = useState<BuilderTab>("services");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [preview, setPreview] = useState("");
  const [previewName, setPreviewName] = useState("compose.yml");
  const [previewBusy, setPreviewBusy] = useState(false);
  const [previewErr, setPreviewErr] = useState("");
  const [previewValid, setPreviewValid] = useState<boolean | null>(null);
  const [previewIssue, setPreviewIssue] = useState("");

  const loadCatalog = useCallback(() => {
    api.projectTemplates().then(setTemplates).catch(() => {});
    api.serviceBlocks().then(setBlocks).catch(() => {});
    api.composeFragments().then(setFragments).catch(() => {});
  }, []);
  useEffect(() => loadCatalog(), [loadCatalog]);

  const selectedTpl = templates.find((t) => refKey(t) === tplKey) ?? null;
  const selectedFragments = fragments.filter((f) => pickedFrags[refKey(f)]);
  const activeVars = mode === "template"
    ? (selectedTpl?.variables ?? [])
    : mode === "builder"
      ? dedupeVars(instances.flatMap((i) => i.block.variables ?? []))
      : [];

  const setVar = (k: string, v: string) => setVars((prev) => ({ ...prev, [k]: v }));

  const addInstance = (b: ServiceBlockMeta) =>
    setInstances((cur) => [...cur, { uid: ++uidRef.current, block: b, key: dedupeKey(b.service, cur.map((i) => i.key)), merge: {} }]);
  const removeInstance = (uid: number) => setInstances((cur) => cur.filter((i) => i.uid !== uid));
  const setInstanceKey = (uid: number, key: string) => setInstances((cur) => cur.map((i) => (i.uid === uid ? { ...i, key } : i)));
  const toggleMerge = (uid: number, rk: string) => setInstances((cur) => cur.map((i) => (i.uid === uid ? { ...i, merge: { ...i.merge, [rk]: !i.merge[rk] } } : i)));
  // Map each instance's merged fragment refKeys back to refs for the API payload.
  const instancePayload = () => instances.map((i) => ({
    block: { id: i.block.id, source: i.block.source } as TemplateRef,
    key: i.key.trim(),
    merge: selectedFragments.filter((f) => i.merge[refKey(f)]).map((f): TemplateRef => ({ id: f.id, source: f.source })),
  }));
  // Service keys must be non-empty and unique, or two instances collide into one
  // compose service (a blank key falls back to the block's default key server-side).
  const trimmedKeys = instances.map((i) => i.key.trim());
  const dupKeys = new Set(trimmedKeys.filter((k, idx) => k !== "" && trimmedKeys.indexOf(k) !== idx));
  const keyInvalid = (key: string) => { const k = key.trim(); return !k || dupKeys.has(k); };
  const keysOk = instances.every((i) => !keyInvalid(i.key));

  // Live read-only preview of the compose.yml the current selection would seed.
  // Shown for a chosen preset or a non-empty builder selection; debounced so it
  // doesn't fire on every keystroke. Generated secrets vary per call — that's
  // fine for an illustrative preview.
  const showPreview = (mode === "template" && !!selectedTpl) || (mode === "builder" && (instances.length > 0 || selectedFragments.length > 0));
  useEffect(() => {
    if (!showPreview) { setPreview(""); setPreviewErr(""); setPreviewBusy(false); setPreviewValid(null); setPreviewIssue(""); return; }
    const variables = Object.fromEntries(activeVars.map((v) => [v.key, vars[v.key] ?? ""]));
    const opts = mode === "builder"
      ? {
          name: name || "preview",
          instances: instancePayload(),
          fragments: selectedFragments.map((f): TemplateRef => ({ id: f.id, source: f.source })),
          variables,
        }
      : { name: name || "preview", template: { id: selectedTpl!.id, source: selectedTpl!.source }, variables };
    let cancelled = false;
    setPreviewBusy(true);
    setPreviewValid(null); setPreviewIssue(""); // clear the stale chip/banner while the new preview loads
    const t = setTimeout(() => {
      api.previewTemplate(opts).then((r) => {
        if (cancelled) return;
        // Prefer the compose entry file; user-saved snapshots may name it
        // compose.yaml / docker-compose.yml, so match those too before falling back.
        const compose = r.files.find((f) => /^(docker-)?compose\.ya?ml$/i.test(f.path)) ?? r.files[0];
        setPreview(compose?.content ?? ""); setPreviewName(compose?.path ?? "compose.yml"); setPreviewErr("");
        setPreviewValid(r.valid ?? null);
        setPreviewIssue(r.valid === false ? (r.error ?? "invalid compose") : (r.warnings?.length ? r.warnings.join(" · ") : ""));
      }).catch((e) => { if (!cancelled) { setPreview(""); setPreviewErr(e instanceof ApiError ? e.message : "preview failed"); setPreviewValid(null); setPreviewIssue(""); } })
        .finally(() => { if (!cancelled) setPreviewBusy(false); });
    }, 350);
    return () => { cancelled = true; clearTimeout(t); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [showPreview, mode, tplKey, instances, pickedFrags, vars, name, fragments, templates]);

  const deleteItem = async (kind: "template" | "block" | "fragment", id: string, label: string) => {
    if (!(await dialogs.confirm({ title: `Delete ${kind}`, message: <>Delete <code className="font-mono text-text">{label}</code>?</>, danger: true, confirmLabel: "Delete" }))) return;
    try {
      if (kind === "template") await api.deleteProjectTemplate(id);
      else if (kind === "fragment") await api.deleteComposeFragment(id);
      else await api.deleteServiceBlock(id);
      loadCatalog();
    } catch (e) {
      await dialogs.alert({ title: "Could not delete", message: e instanceof ApiError ? e.message : "failed" });
    }
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const n = name.trim();
    if (!n) return;
    setBusy(true); setErr("");
    try {
      // Only send the variables that belong to the active selection, so values
      // typed under another template/block don't leak into this create call.
      const variables = Object.fromEntries(activeVars.map((v) => [v.key, vars[v.key] ?? ""]));
      let r: { id: number; slug: string };
      if (mode === "import" && file) r = await api.importProject(n, file);
      else if (mode === "builder" && (instances.length || selectedFragments.length)) r = await api.createProject(n, { instances: instancePayload(), fragments: selectedFragments.map((f): TemplateRef => ({ id: f.id, source: f.source })), variables });
      else if (mode === "template" && selectedTpl) r = await api.createProject(n, { template: { id: selectedTpl.id, source: selectedTpl.source }, variables });
      else r = await api.createProject(n);
      onCreated({ id: r.id, name: n, slug: r.slug, composeFile: "compose.yml", createdBy: "", createdAt: "", updatedAt: "" });
    } catch (e2) {
      setErr(e2 instanceof ApiError ? e2.message : "could not create project");
      setBusy(false);
    }
  };

  const tab = (m: NewMode, icon: ReactNode, label: string) => (
    <button type="button" onClick={() => { setMode(m); setVars({}); setErr(""); }}
      className={clsx("flex items-center gap-2 px-3 py-1.5 text-sm rounded-lg border", mode === m ? "border-accent bg-accent/10 text-text" : "border-border text-muted hover:text-text")}>
      {icon} {label}
    </button>
  );

  const canSubmit = !!name.trim() && !busy && (mode === "import" ? !!file : mode === "builder" ? ((instances.length > 0 || selectedFragments.length > 0) && keysOk) : true);

  // Inner segmented control for the builder (Services / Shared / Variables).
  const subTab = (key: BuilderTab, icon: ReactNode, label: string, count: number) => (
    <button type="button" onClick={() => setBuilderTab(key)}
      className={clsx("flex-1 flex items-center justify-center gap-1.5 px-2 py-1.5 rounded-md text-sm", builderTab === key ? "bg-panel text-text shadow-sm" : "text-muted hover:text-text")}>
      {icon} {label}{count > 0 ? <span className="text-[10px] bg-accent/20 text-accent rounded-full px-1.5 leading-4">{count}</span> : null}
    </button>
  );

  return (
    <div className="fixed inset-0 z-[55] bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <form className={clsx("card flex flex-col max-h-[90vh]", showPreview ? "w-[92vw] max-w-[1500px]" : "w-full max-w-2xl")} onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <FolderGit2 className="h-4 w-4 text-accent" />
          <div className="font-medium">New project</div>
          <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="flex-1 flex min-h-0">
        <div className="flex-1 min-w-0 p-4 space-y-3 overflow-y-auto">
          <label className="block">
            <span className="label">Project name</span>
            <input autoFocus className="input" value={name} placeholder="My app" onChange={(e) => setName(e.target.value)} />
          </label>

          <div className="flex flex-wrap gap-2">
            {tab("template", <LayoutTemplate className="h-4 w-4" />, "Template")}
            {tab("builder", <Puzzle className="h-4 w-4" />, "Builder")}
            {tab("import", <Upload className="h-4 w-4" />, "Import .zip")}
          </div>

          {mode === "template" && (
            <div className="space-y-1.5">
              <button type="button" onClick={() => setTplKey("")}
                className={clsx("w-full text-left px-3 py-2 rounded-lg border text-sm", tplKey === "" ? "border-accent bg-accent/10" : "border-border hover:bg-panel2/50")}>
                <span className="font-medium">Empty</span> <span className="text-muted">— starter compose.yml</span>
              </button>
              {templates.map((t) => (
                <div key={refKey(t)} className={clsx("flex items-start gap-2 px-3 py-2 rounded-lg border", refKey(t) === tplKey ? "border-accent bg-accent/10" : "border-border")}>
                  <button type="button" className="flex-1 text-left" onClick={() => { setTplKey(refKey(t)); setVars({}); }}>
                    <div className="text-sm font-medium flex items-center gap-2">
                      {t.name}
                      {t.source === "user" && <span className="text-[10px] uppercase tracking-wide text-muted border border-border rounded px-1">yours</span>}
                    </div>
                    <div className="text-xs text-muted">{t.description}</div>
                  </button>
                  {t.deletable && <button type="button" className="btn-ghost px-2 py-1 text-danger" title="Delete template" onClick={() => deleteItem("template", t.id, t.name)}><Trash2 className="h-3.5 w-3.5" /></button>}
                </div>
              ))}
            </div>
          )}

          {mode === "builder" && (
            <div className="space-y-3">
              <div className="flex gap-1 rounded-lg bg-panel2/50 p-0.5">
                {subTab("services", <Puzzle className="h-3.5 w-3.5" />, "Services", instances.length)}
                {subTab("shared", <Anchor className="h-3.5 w-3.5" />, "Shared defs", selectedFragments.length)}
                {subTab("variables", <KeyRound className="h-3.5 w-3.5" />, "Variables", activeVars.length)}
              </div>

              {builderTab === "services" && (
                <div className="space-y-3">
                  {/* Added service instances — add a block twice for a cluster. */}
                  {instances.length > 0 && (
                    <div className="space-y-1.5">
                      {instances.map((i) => (
                        <div key={i.uid} className="rounded-lg border border-accent/40 bg-accent/5 px-3 py-2 space-y-1.5">
                          <div className="flex items-center gap-2">
                            <span className="text-xs text-muted shrink-0 truncate max-w-[8rem]" title={i.block.name}>{i.block.name}</span>
                            <input className={clsx("input font-mono py-1 text-xs", keyInvalid(i.key) && "border-danger")} value={i.key} placeholder="service key" onChange={(e) => setInstanceKey(i.uid, e.target.value)} />
                            <button type="button" className="btn-ghost px-1.5 py-1 text-danger ml-auto" title="Remove" onClick={() => removeInstance(i.uid)}><Trash2 className="h-3.5 w-3.5" /></button>
                          </div>
                          {selectedFragments.length > 0 && (
                            <div className="flex items-center gap-1.5 flex-wrap text-xs">
                              <span className="text-muted">Merge:</span>
                              {selectedFragments.map((f) => {
                                const on = !!i.merge[refKey(f)];
                                return (
                                  <button type="button" key={refKey(f)} onClick={() => toggleMerge(i.uid, refKey(f))}
                                    className={clsx("rounded px-1.5 py-0.5 border font-mono", on ? "border-accent bg-accent/15 text-accent" : "border-border text-muted hover:text-text")}>
                                    {on ? "<<: *" : ""}{f.name}
                                  </button>
                                );
                              })}
                            </div>
                          )}
                        </div>
                      ))}
                      {!keysOk && <p className="text-[11px] text-danger">Service keys must be unique and non-empty.</p>}
                    </div>
                  )}

                  {/* Palette: click to add an instance (again for a cluster). */}
                  <div className="text-xs text-muted">Add a service{instances.length > 0 ? " (again for a cluster)" : ""} — they merge into one <code>compose.yml</code> you can edit afterwards.</div>
                  <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
                    {blocks.map((b) => (
                      <button type="button" key={refKey(b)} className="rounded-lg border border-border px-3 py-2 text-left hover:bg-panel2/50" onClick={() => addInstance(b)}>
                        <div className="text-sm font-medium flex items-center gap-2">
                          <Plus className="h-3.5 w-3.5 text-accent shrink-0" />{b.name}
                          {b.source === "user" && <span className="text-[10px] uppercase tracking-wide text-muted border border-border rounded px-1">yours</span>}
                        </div>
                        <div className="text-xs text-muted">{b.description}</div>
                      </button>
                    ))}
                  </div>
                  {customOpen
                    ? <CustomBlockForm onClose={() => setCustomOpen(false)} onSaved={() => { setCustomOpen(false); loadCatalog(); }} />
                    : <button type="button" className="btn-ghost px-3 py-1.5 text-sm" onClick={() => setCustomOpen(true)}><Plus className="h-4 w-4" /> Custom service…</button>}
                </div>
              )}

              {builderTab === "shared" && (
                <div className="space-y-2">
                  <div className="flex items-start gap-2 text-xs text-muted">
                    <Anchor className="h-3.5 w-3.5 mt-0.5 shrink-0" /> <span>Include top-level YAML anchors here, then tick <strong>Merge</strong> on each service (in the <strong>Services</strong> tab) to inject <code>{"<<: *name"}</code>.</span>
                  </div>
                  {fragments.length > 0 && (
                    <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
                      {fragments.map((f) => {
                        const on = !!pickedFrags[refKey(f)];
                        return (
                          <div key={refKey(f)} className={clsx("rounded-lg border px-3 py-2", on ? "border-accent bg-accent/10" : "border-border")}>
                            <div className="flex items-start gap-2">
                              <button type="button" className="flex-1 text-left" onClick={() => setPickedFrags((p) => ({ ...p, [refKey(f)]: !on }))}>
                                <div className="text-sm font-medium flex items-center gap-2">
                                  {f.name}
                                  {on && <CheckCircle2 className="h-3.5 w-3.5 text-accent" />}
                                  {f.source === "user" && <span className="text-[10px] uppercase tracking-wide text-muted border border-border rounded px-1">yours</span>}
                                </div>
                                <div className="text-xs text-muted">{f.description}</div>
                              </button>
                              {f.deletable && <button type="button" className="btn-ghost px-1.5 py-1 text-danger" title="Delete definition" onClick={() => deleteItem("fragment", f.id, f.name)}><Trash2 className="h-3.5 w-3.5" /></button>}
                            </div>
                          </div>
                        );
                      })}
                    </div>
                  )}
                  {customFragOpen
                    ? <CustomFragmentForm onClose={() => setCustomFragOpen(false)} onSaved={() => { setCustomFragOpen(false); loadCatalog(); }} />
                    : <button type="button" className="btn-ghost px-3 py-1.5 text-sm" onClick={() => setCustomFragOpen(true)}><Plus className="h-4 w-4" /> Custom definition…</button>}
                </div>
              )}

              {builderTab === "variables" && (
                activeVars.length > 0
                  ? <VarFields vars={activeVars} values={vars} onChange={setVar} />
                  : <p className="text-xs text-muted py-2">No variables — the selected services don’t declare any.</p>
              )}
            </div>
          )}

          {mode === "import" && (
            <label className="block">
              <span className="label">Project archive (.zip)</span>
              <input type="file" accept=".zip,application/zip" className="input py-1.5" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
              {file && <span className="text-xs text-muted">Will import {file.name}</span>}
            </label>
          )}

          {mode === "template" && <VarFields vars={activeVars} values={vars} onChange={setVar} />}
          {err && <p className="text-sm text-danger">{err}</p>}
        </div>
        {showPreview && (
          <div className="hidden md:flex w-[42%] shrink-0 border-l border-border flex-col min-h-0">
            <div className="flex items-center gap-2 px-3 py-2 border-b border-border text-xs text-muted">
              <Eye className="h-3.5 w-3.5" /> Preview — <span className="font-mono">{previewName}</span>
              <span className="ml-auto flex items-center gap-1">
                {previewBusy ? <Loader2 className="h-3 w-3 animate-spin" />
                  : previewValid === true && !previewIssue ? <><CheckCircle2 className="h-3 w-3 text-ok" /><span className="text-ok">valid</span></>
                  : previewValid === true ? <><AlertTriangle className="h-3 w-3 text-warn" /><span className="text-warn">warnings</span></>
                  : previewValid === false ? <><AlertCircle className="h-3 w-3 text-danger" /><span className="text-danger">invalid</span></>
                  : null}
              </span>
            </div>
            {previewIssue && (
              <div className={clsx("px-3 py-1.5 text-[11px] border-b border-border", previewValid === false ? "text-danger bg-danger/10" : "text-warn bg-warn/10")}>{previewIssue}</div>
            )}
            <div className="flex-1 overflow-auto p-3 bg-panel2/40">
              {previewErr
                ? <p className="text-xs text-danger">{previewErr}</p>
                : <pre className="text-xs font-mono whitespace-pre text-text/90">{preview || "…"}</pre>}
            </div>
          </div>
        )}
        </div>
        <div className="flex justify-end gap-2 p-4 border-t border-border">
          <button type="button" className="btn-ghost px-3 py-1.5 text-sm" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn-primary px-3 py-1.5 text-sm disabled:opacity-40" disabled={!canSubmit}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />} {mode === "import" ? "Import" : "Create"}
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
  const [liveVal, setLiveVal] = useState<"idle" | "checking" | "ok" | "warning" | "error">("idle");
  const [serverCheck, setServerCheck] = useState<ServerCheck>(null);
  const [summary, setSummary] = useState<ComposeModel | null>(null);
  const [saveTpl, setSaveTpl] = useState(false);
  const valSeq = useRef(0);
  const dialogs = useDialogs();
  const uploadRef = useRef<HTMLInputElement>(null);

  const dirOf = (path: string) => (path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : "");

  useEffect(() => { api.projectProfiles(project.id).then((r) => setProfiles(r.profiles)).catch(() => {}); }, [project.id]);

  // Live validation: compose files are checked with `docker compose config` and
  // Dockerfiles with `docker build --check`, both over the *unsaved* buffer (no
  // save required). Results render as inline diagnostics in the editor; the chip
  // shows the overall status. Validation is a property of those files, so it
  // only runs while one of them is open.
  const composeFileName = project.composeFile || "compose.yml";
  const onComposeFile = isComposeFile(active, composeFileName);
  const onDockerfileActive = !onComposeFile && isDockerfile(active);
  // Switching files clears the previous file's diagnostics immediately (the
  // debounced re-check below would otherwise leave them hanging for ~1s).
  useEffect(() => { setServerCheck(null); setLiveVal("idle"); }, [active]);
  useEffect(() => {
    if (!composeAvailable || (!onComposeFile && !onDockerfileActive)) {
      setLiveVal("idle"); setServerCheck(null);
      return;
    }
    const seq = ++valSeq.current;
    const stale = () => seq !== valSeq.current;
    const t = setTimeout(() => {
      setLiveVal("checking");
      if (onDockerfileActive) {
        api.checkDockerfile(project.id, draft)
          .then((r) => { if (stale()) return;
            if (r.unavailable) { setLiveVal("idle"); setServerCheck(null); return; }
            setServerCheck({ kind: "dockerfile", output: r.output });
            setLiveVal(r.level);
          })
          .catch(() => { if (!stale()) { setLiveVal("idle"); setServerCheck(null); } });
      } else {
        api.validateProject(project.id, { name: active, content: draft })
          .then((r) => { if (stale()) return;
            if (r.unavailable) { setLiveVal("idle"); setServerCheck(null); return; }
            if (!r.valid) { setServerCheck({ kind: "compose", error: r.error }); setLiveVal("error"); }
            else { setServerCheck({ kind: "compose", warnings: r.warnings }); setLiveVal(r.warnings?.length ? "warning" : "ok"); }
          })
          .catch(() => { if (!stale()) { setLiveVal("idle"); setServerCheck(null); } });
      }
    }, onDockerfileActive ? 1200 : 800);
    return () => clearTimeout(t);
  }, [active, draft, composeAvailable, project.id, onComposeFile, onDockerfileActive]);
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

  const activeFile = files?.find((f) => f.name === active);

  return (
    <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <div className="card relative w-[92vw] h-[90vh] flex flex-col" onClick={(e) => e.stopPropagation()}>
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
            <button className="btn-ghost px-2 h-8" title="Save as preset" onClick={() => setSaveTpl(true)}><LayoutTemplate className="h-4 w-4" /></button>
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
          <div className="w-64 shrink-0 border-r border-border flex flex-col">
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
              {liveVal !== "idle" && (
                <span className="text-[11px] flex items-center gap-1 shrink-0" title="Issues are underlined inline in the editor">
                  {liveVal === "checking" ? (
                    <><Loader2 className="h-3 w-3 animate-spin text-muted" /><span className="text-muted">checking…</span></>
                  ) : liveVal === "warning" ? (
                    <><AlertTriangle className="h-3 w-3 text-warn" /><span className="text-warn">warnings</span></>
                  ) : liveVal === "ok" ? (
                    <><CheckCircle2 className="h-3 w-3 text-ok" /><span className="text-ok">valid</span></>
                  ) : (
                    <><AlertCircle className="h-3 w-3 text-danger" /><span className="text-danger">invalid</span></>
                  )}
                </span>
              )}
              <div className="ml-auto flex items-center gap-1">
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
                <button className="btn-ghost px-2 py-1 text-xs disabled:opacity-40" disabled={!active} title="Download this file" onClick={downloadActive}><Download className="h-3.5 w-3.5" /></button>
                <button className="btn-primary px-3 py-1 text-xs disabled:opacity-40" disabled={!dirty || busy === "save" || !active || activeFile?.binary} onClick={save}>
                  {busy === "save" ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />} Save
                </button>
              </div>
            </div>
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
                  <CodeEditor filename={active} value={draft} onChange={setDraft} serverCheck={serverCheck} />
                </Suspense>
              </div>
            ) : (
              <div className="flex-1 grid place-items-center text-sm text-muted">Select or add a file</div>
            )}
          </div>
        </div>
      </div>
      {summary && <ComposeSummaryModal model={summary} onClose={() => setSummary(null)} />}
      {saveTpl && <SaveAsTemplateModal projectId={project.id} onClose={() => setSaveTpl(false)} onSaved={() => setSaveTpl(false)} />}
    </div>
  );
}
