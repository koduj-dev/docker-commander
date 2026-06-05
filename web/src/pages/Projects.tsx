import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import {
  FolderGit2, Plus, Rocket, Square, RotateCw, Trash2, X, FilePlus, FolderPlus, Upload, Loader2,
  ExternalLink, Save, FileText, Folder, Terminal, Pencil, ChevronRight, Download,
} from "lucide-react";
import { api, ApiError } from "../lib/api";
import type { Project, ProjectFile, Stack } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner, StateBadge } from "../components/ui";
import { useDialogs } from "../components/Dialog";
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

  const newProject = async () => {
    const name = await dialogs.prompt({ title: "New project", label: "Project name", placeholder: "My app" });
    if (!name) return;
    try {
      const r = await api.createProject(name);
      load();
      setEditing({ id: r.id, name, slug: r.slug, composeFile: "compose.yml", createdBy: "", createdAt: "", updatedAt: "" });
    } catch (e) {
      dialogs.alert({ title: "Could not create project", message: e instanceof ApiError ? e.message : "unknown error" });
    }
  };

  const rename = async (p: Project) => {
    const name = await dialogs.prompt({ title: "Rename project", label: "Display name (the identifier won't change)", defaultValue: p.name });
    if (!name || name === p.name) return;
    try { await api.renameProject(p.id, name); load(); }
    catch (e) { dialogs.alert({ title: "Rename failed", message: e instanceof Error ? e.message : "unknown error" }); }
  };

  const runCompose = async (p: Project, kind: Kind) => {
    setBusy(p.slug);
    try {
      const r = kind === "deploy" ? await api.deployProject(p.id) : kind === "down" ? await api.downProject(p.id) : await api.restartProject(p.id);
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

  return (
    <>
      <PageHeader title="Projects" actions={<button className="btn-primary px-3 py-1.5 text-sm" onClick={newProject}><Plus className="h-4 w-4" /> New project</button>} />
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
          projects.map((p) => {
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
                            <button className="btn-ghost px-2 py-1 disabled:opacity-40" title="Restart" disabled={!composeAvailable} onClick={() => runCompose(p, "restart")}><RotateCw className="h-4 w-4" /></button>
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
      </div>

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

// ProjectEditor is a multi-file editor over the project folder.
function ProjectEditor({ project, composeAvailable, deployed, onClose, onOutput }: {
  project: Project; composeAvailable: boolean; deployed: boolean; onClose: () => void; onOutput: (o: Output) => void;
}) {
  const [files, setFiles] = useState<ProjectFile[] | null>(null);
  const [active, setActive] = useState<string>("");
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState("");
  const dialogs = useDialogs();
  const uploadRef = useRef<HTMLInputElement>(null);

  const original = files?.find((f) => f.name === active)?.content ?? "";
  const dirty = files != null && active !== "" && draft !== original;

  const loadFiles = useCallback((select?: string) => {
    api.projectFiles(project.id).then((fs) => {
      setFiles(fs);
      setActive((cur) => {
        const want = select ?? cur;
        const pick = fs.find((f) => !f.isDir && f.name === want) ?? fs.find((f) => f.name === "compose.yml") ?? fs.find((f) => !f.isDir);
        if (pick) setDraft(pick.content);
        return pick?.name ?? "";
      });
    }).catch(() => setFiles([]));
  }, [project.id]);
  useEffect(() => loadFiles(), [loadFiles]);

  const select = async (name: string) => {
    if (name === active) return;
    if (dirty && !(await dialogs.confirm({ title: "Discard unsaved changes?", message: "This file has unsaved edits.", danger: true, confirmLabel: "Discard" }))) return;
    setActive(name);
    setDraft(files?.find((x) => x.name === name)?.content ?? "");
  };

  const save = async () => {
    setBusy("save");
    try { await api.writeProjectFile(project.id, active, draft); loadFiles(active); }
    catch (e) { dialogs.alert({ title: "Save failed", message: e instanceof Error ? e.message : "unknown error" }); }
    finally { setBusy(""); }
  };

  const addFile = async () => {
    const name = await dialogs.prompt({ title: "New file", label: "File name", placeholder: "nginx.conf or scripts/init.sh" });
    if (!name) return;
    setBusy("add");
    try { await api.writeProjectFile(project.id, name, ""); loadFiles(name); }
    catch (e) { dialogs.alert({ title: "Could not add file", message: e instanceof Error ? e.message : "unknown error" }); }
    finally { setBusy(""); }
  };

  const addDir = async () => {
    const name = await dialogs.prompt({ title: "New folder", label: "Folder name", placeholder: "config" });
    if (!name) return;
    setBusy("dir");
    try { await api.makeProjectDir(project.id, name); loadFiles(); }
    catch (e) { dialogs.alert({ title: "Could not create folder", message: e instanceof Error ? e.message : "unknown error" }); }
    finally { setBusy(""); }
  };

  const removeEntry = async (f: ProjectFile) => {
    if (!(await dialogs.confirm({ title: `Delete ${f.isDir ? "folder" : "file"} "${f.name}"?`, danger: true, confirmLabel: "Delete" }))) return;
    setBusy("del");
    try { await api.deleteProjectFile(project.id, f.name); loadFiles(f.name === active ? undefined : active); }
    catch (e) { dialogs.alert({ title: "Delete failed", message: e instanceof Error ? e.message : "unknown error (folders must be empty)" }); }
    finally { setBusy(""); }
  };

  const upload = async (file: File) => {
    setBusy("upload");
    try { await api.writeProjectFile(project.id, file.name, await file.text()); loadFiles(file.name); }
    catch (e) { dialogs.alert({ title: "Upload failed", message: e instanceof Error ? e.message : "unknown error" }); }
    finally { setBusy(""); if (uploadRef.current) uploadRef.current.value = ""; }
  };

  const runCompose = async (kind: Kind) => {
    if (dirty && !(await dialogs.confirm({ title: "Unsaved changes", message: "Continue with the last saved files?", confirmLabel: "Continue" }))) return;
    setBusy(kind);
    try {
      const r = kind === "deploy" ? await api.deployProject(project.id) : kind === "down" ? await api.downProject(project.id) : await api.restartProject(project.id);
      onOutput({ title: `${project.name} — ${kind}`, text: r.output || r.error || "(no output)", ok: r.ok });
    } catch (e) {
      onOutput({ title: `${project.name} — ${kind}`, text: e instanceof Error ? e.message : "failed", ok: false });
    } finally { setBusy(""); }
  };

  const activeFile = files?.find((f) => f.name === active);

  return (
    <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <div className="card w-[85vw] h-[85vh] flex flex-col" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <FolderGit2 className="h-4 w-4 text-accent shrink-0" />
          <div className="min-w-0">
            <div className="font-medium truncate">{project.name}</div>
            <div className="text-xs text-muted font-mono">{project.slug}</div>
          </div>
          <div className="flex items-center gap-1 ml-auto">
            <a className="btn-ghost px-2 py-1.5" title="Download project as .zip" href={api.projectDownloadUrl(project.id)}><Download className="h-4 w-4" /></a>
            <button className="btn-primary px-3 py-1.5 text-sm disabled:opacity-40" disabled={!composeAvailable || busy === "deploy"} onClick={() => runCompose("deploy")} title={composeAvailable ? "docker compose up -d" : "docker compose CLI not available"}>
              {busy === "deploy" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Rocket className="h-4 w-4" />} {deployed ? "Redeploy" : "Deploy"}
            </button>
            <button className="btn-ghost px-3 py-1.5 text-sm disabled:opacity-40" disabled={!composeAvailable || !deployed || busy === "down"} onClick={() => runCompose("down")} title={deployed ? "docker compose down" : "not deployed"}>Down</button>
            <button className="btn-ghost px-2 py-1.5" onClick={onClose}><X className="h-4 w-4" /></button>
          </div>
        </div>

        <div className="flex-1 flex min-h-0">
          {/* File list */}
          <div className="w-56 shrink-0 border-r border-border flex flex-col">
            <div className="flex items-center gap-1 p-2 border-b border-border">
              <span className="text-xs uppercase tracking-wide text-muted px-1">Files</span>
              <button className="btn-ghost px-1.5 py-1 ml-auto" title="New file" onClick={addFile}><FilePlus className="h-4 w-4" /></button>
              <button className="btn-ghost px-1.5 py-1" title="New folder" onClick={addDir}><FolderPlus className="h-4 w-4" /></button>
              <button className="btn-ghost px-1.5 py-1" title="Upload file" onClick={() => uploadRef.current?.click()}><Upload className="h-4 w-4" /></button>
              <input ref={uploadRef} type="file" className="hidden" onChange={(e) => e.target.files?.[0] && upload(e.target.files[0])} />
            </div>
            <div className="flex-1 overflow-auto p-1">
              {files === null ? <div className="p-3 text-muted text-sm flex items-center gap-2"><Spinner /> …</div> :
                files.length === 0 ? <div className="p-3 text-muted text-xs">No files</div> :
                files.map((f) => (
                  <div
                    key={f.name}
                    className={`group flex items-center gap-1 rounded px-2 py-1 text-sm ${f.isDir ? "" : "cursor-pointer"} ${f.name === active ? "bg-accent/15 text-accent" : "hover:bg-panel2"}`}
                    onClick={() => !f.isDir && select(f.name)}
                  >
                    {f.isDir ? <Folder className="h-3.5 w-3.5 shrink-0 text-muted" /> : <FileText className="h-3.5 w-3.5 shrink-0 opacity-70" />}
                    <span className={`truncate font-mono text-xs ${f.isDir ? "text-muted" : ""}`}>{f.name}</span>
                    {dirty && f.name === active && <span className="text-warn text-xs">●</span>}
                    <button className="ml-auto opacity-0 group-hover:opacity-100 text-danger" title="Delete" onClick={(e) => { e.stopPropagation(); removeEntry(f); }}><Trash2 className="h-3.5 w-3.5" /></button>
                  </div>
                ))}
            </div>
          </div>

          {/* Editor */}
          <div className="flex-1 flex flex-col min-w-0">
            <div className="flex items-center gap-2 p-2 border-b border-border">
              <span className="text-xs font-mono text-muted truncate">{active || "—"}</span>
              <div className="ml-auto flex items-center gap-1">
                <button className="btn-ghost px-2 py-1 text-xs disabled:opacity-40" disabled={!active} title="Download this file" onClick={() => downloadText(active, draft)}><Download className="h-3.5 w-3.5" /></button>
                <button className="btn-primary px-3 py-1 text-xs disabled:opacity-40" disabled={!dirty || busy === "save" || !active} onClick={save}>
                  {busy === "save" ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />} Save
                </button>
              </div>
            </div>
            {activeFile?.tooLarge ? (
              <div className="p-4 text-sm text-muted">This file is too large to edit here.</div>
            ) : (
              <textarea
                className="flex-1 w-full resize-none bg-bg text-text font-mono text-sm p-3 focus:outline-none"
                spellCheck={false}
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                placeholder={active ? "" : "Select or add a file"}
                disabled={!active}
              />
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
