import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import {
  FolderGit2, Plus, Rocket, Square, Trash2, X, FilePlus, Upload, Loader2,
  ExternalLink, Save, FileText, Terminal,
} from "lucide-react";
import { api, ApiError } from "../lib/api";
import type { Project, ProjectFile, Stack } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";
import { useDockerEventTick } from "../lib/dockerEvents";

type Output = { title: string; text: string; ok: boolean };

function projectState(stack: Stack | undefined): { cls: string; label: string; deployed: boolean } {
  if (!stack) return { cls: "bg-muted/40", label: "Not deployed", deployed: false };
  const total = stack.containers.length;
  if (stack.running === 0) return { cls: "bg-danger text-danger", label: "Stopped", deployed: true };
  if (stack.running === total) return { cls: "bg-ok text-ok", label: "Running", deployed: true };
  return { cls: "bg-warn text-warn", label: "Partial", deployed: true };
}

export function Projects() {
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [composeAvailable, setComposeAvailable] = useState(true);
  const [stacks, setStacks] = useState<Stack[]>([]);
  const [busy, setBusy] = useState(""); // slug acting
  const [editing, setEditing] = useState<Project | null>(null);
  const [output, setOutput] = useState<Output | null>(null);
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

  const newProject = async () => {
    const name = window.prompt("Project name");
    if (!name?.trim()) return;
    try {
      const r = await api.createProject(name.trim());
      load();
      // open the editor for the fresh project
      setEditing({ id: r.id, name: name.trim(), slug: r.slug, composeFile: "compose.yml", createdBy: "", createdAt: "", updatedAt: "" });
    } catch (e) {
      alert(e instanceof ApiError ? e.message : "could not create project");
    }
  };

  const runCompose = async (p: Project, kind: "deploy" | "down") => {
    setBusy(p.slug);
    try {
      const r = kind === "deploy" ? await api.deployProject(p.id) : await api.downProject(p.id);
      setOutput({ title: `${p.name} — ${kind}`, text: r.output || r.error || "(no output)", ok: r.ok });
      load();
    } catch (e) {
      setOutput({ title: `${p.name} — ${kind}`, text: e instanceof Error ? e.message : "failed", ok: false });
    } finally {
      setBusy("");
    }
  };

  const remove = async (p: Project) => {
    if (!window.confirm(`Delete project "${p.name}"? This removes its folder and files.`)) return;
    setBusy(p.slug);
    try {
      await api.deleteProject(p.id);
      load();
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        if (window.confirm(`"${p.name}" is currently deployed. Run docker compose down and delete it anyway?`)) {
          try { await api.deleteProject(p.id, true); load(); }
          catch (e2) { alert(e2 instanceof Error ? e2.message : "delete failed"); }
        }
      } else {
        alert(e instanceof Error ? e.message : "delete failed");
      }
    } finally {
      setBusy("");
    }
  };

  if (!projects)
    return (<><PageHeader title="Projects" /><div className="p-6 flex items-center gap-2 text-muted"><Spinner /> Loading…</div></>);

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
            return (
              <div key={p.id} className="card p-4 flex items-center gap-3">
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
                      <button className="btn-ghost px-2 py-1 disabled:opacity-40" title={composeAvailable ? "Deploy" : "docker compose CLI not available"} disabled={!composeAvailable} onClick={() => runCompose(p, "deploy")}><Rocket className="h-4 w-4" /></button>
                      {st.deployed && <button className="btn-ghost px-2 py-1 disabled:opacity-40" title="Down" disabled={!composeAvailable} onClick={() => runCompose(p, "down")}><Square className="h-4 w-4" /></button>}
                      {st.deployed && <Link className="btn-ghost px-2 py-1" title="Open in Stacks" to="/stacks"><ExternalLink className="h-4 w-4" /></Link>}
                      <button className="btn-ghost px-2 py-1 text-danger" title="Delete project" onClick={() => remove(p)}><Trash2 className="h-4 w-4" /></button>
                    </>
                  )}
                </div>
              </div>
            );
          })
        )}
      </div>

      {editing && (
        <ProjectEditor
          project={editing}
          composeAvailable={composeAvailable}
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

// ProjectEditor is a multi-file editor over the project folder: a left file
// list (add / delete / upload) and a right textarea, plus Deploy / Down.
function ProjectEditor({ project, composeAvailable, onClose, onOutput }: {
  project: Project; composeAvailable: boolean; onClose: () => void; onOutput: (o: Output) => void;
}) {
  const [files, setFiles] = useState<ProjectFile[] | null>(null);
  const [active, setActive] = useState<string>("");
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState("");
  const uploadRef = useRef<HTMLInputElement>(null);

  const original = files?.find((f) => f.name === active)?.content ?? "";
  const dirty = files != null && draft !== original;

  const loadFiles = useCallback((select?: string) => {
    api.projectFiles(project.id).then((fs) => {
      setFiles(fs);
      const next = select ?? active ?? "";
      const pick = fs.find((f) => f.name === next) ?? fs.find((f) => f.name === "compose.yml") ?? fs[0];
      if (pick) { setActive(pick.name); setDraft(pick.content); }
    }).catch(() => setFiles([]));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [project.id]);
  useEffect(() => loadFiles(), [loadFiles]);

  const select = (name: string) => {
    if (name === active) return;
    if (dirty && !window.confirm("Discard unsaved changes to this file?")) return;
    const f = files?.find((x) => x.name === name);
    setActive(name);
    setDraft(f?.content ?? "");
  };

  const save = async () => {
    setBusy("save");
    try { await api.writeProjectFile(project.id, active, draft); loadFiles(active); }
    catch (e) { alert(e instanceof Error ? e.message : "save failed"); }
    finally { setBusy(""); }
  };

  const addFile = async () => {
    const name = window.prompt("New file name (e.g. nginx.conf or scripts/init.sh)");
    if (!name?.trim()) return;
    setBusy("add");
    try { await api.writeProjectFile(project.id, name.trim(), ""); loadFiles(name.trim()); }
    catch (e) { alert(e instanceof Error ? e.message : "could not add file"); }
    finally { setBusy(""); }
  };

  const removeFile = async (name: string) => {
    if (!window.confirm(`Delete file "${name}"?`)) return;
    setBusy("del");
    try { await api.deleteProjectFile(project.id, name); loadFiles(name === active ? undefined : active); }
    catch (e) { alert(e instanceof Error ? e.message : "delete failed"); }
    finally { setBusy(""); }
  };

  const upload = async (file: File) => {
    setBusy("upload");
    try { await api.writeProjectFile(project.id, file.name, await file.text()); loadFiles(file.name); }
    catch (e) { alert(e instanceof Error ? e.message : "upload failed"); }
    finally { setBusy(""); if (uploadRef.current) uploadRef.current.value = ""; }
  };

  const deploy = async (kind: "deploy" | "down") => {
    if (dirty && !window.confirm("You have unsaved changes. Deploy with the last saved files?")) return;
    setBusy(kind);
    try {
      const r = kind === "deploy" ? await api.deployProject(project.id) : await api.downProject(project.id);
      onOutput({ title: `${project.name} — ${kind}`, text: r.output || r.error || "(no output)", ok: r.ok });
    } catch (e) {
      onOutput({ title: `${project.name} — ${kind}`, text: e instanceof Error ? e.message : "failed", ok: false });
    } finally { setBusy(""); }
  };

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
            <button className="btn-primary px-3 py-1.5 text-sm disabled:opacity-40" disabled={!composeAvailable || busy === "deploy"} onClick={() => deploy("deploy")} title={composeAvailable ? "docker compose up -d" : "docker compose CLI not available"}>
              {busy === "deploy" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Rocket className="h-4 w-4" />} Deploy
            </button>
            <button className="btn-ghost px-3 py-1.5 text-sm disabled:opacity-40" disabled={!composeAvailable || busy === "down"} onClick={() => deploy("down")}>Down</button>
            <button className="btn-ghost px-2 py-1.5" onClick={onClose}><X className="h-4 w-4" /></button>
          </div>
        </div>

        <div className="flex-1 flex min-h-0">
          {/* File list */}
          <div className="w-56 shrink-0 border-r border-border flex flex-col">
            <div className="flex items-center gap-1 p-2 border-b border-border">
              <span className="text-xs uppercase tracking-wide text-muted px-1">Files</span>
              <button className="btn-ghost px-1.5 py-1 ml-auto" title="New file" onClick={addFile}><FilePlus className="h-4 w-4" /></button>
              <button className="btn-ghost px-1.5 py-1" title="Upload file" onClick={() => uploadRef.current?.click()}><Upload className="h-4 w-4" /></button>
              <input ref={uploadRef} type="file" className="hidden" onChange={(e) => e.target.files?.[0] && upload(e.target.files[0])} />
            </div>
            <div className="flex-1 overflow-auto p-1">
              {files === null ? <div className="p-3 text-muted text-sm flex items-center gap-2"><Spinner /> …</div> :
                files.length === 0 ? <div className="p-3 text-muted text-xs">No files</div> :
                files.map((f) => (
                  <div key={f.name} className={`group flex items-center gap-1 rounded px-2 py-1 text-sm cursor-pointer ${f.name === active ? "bg-accent/15 text-accent" : "hover:bg-panel2"}`} onClick={() => select(f.name)}>
                    <FileText className="h-3.5 w-3.5 shrink-0 opacity-70" />
                    <span className="truncate font-mono text-xs">{f.name}</span>
                    {dirty && f.name === active && <span className="text-warn text-xs">●</span>}
                    <button className="ml-auto opacity-0 group-hover:opacity-100 text-danger" title="Delete file" onClick={(e) => { e.stopPropagation(); removeFile(f.name); }}><Trash2 className="h-3.5 w-3.5" /></button>
                  </div>
                ))}
            </div>
          </div>

          {/* Editor */}
          <div className="flex-1 flex flex-col min-w-0">
            <div className="flex items-center gap-2 p-2 border-b border-border">
              <span className="text-xs font-mono text-muted truncate">{active || "—"}</span>
              <button className="btn-primary px-3 py-1 text-xs ml-auto disabled:opacity-40" disabled={!dirty || busy === "save" || !active} onClick={save}>
                {busy === "save" ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />} Save
              </button>
            </div>
            {files && files.find((f) => f.name === active)?.tooLarge ? (
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
