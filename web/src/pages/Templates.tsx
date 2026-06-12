import { lazy, Suspense, useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import clsx from "clsx";
import {
  LayoutTemplate, Puzzle, Plus, Trash2, Eye, Pencil, FileText, Download, X, Save, Loader2,
  FilePlus, FolderPlus, Upload, FileBox, Folder, Copy, Anchor,
} from "lucide-react";
import { api, ApiError } from "../lib/api";
import type { ProjectTemplateMeta, ServiceBlockMeta, ComposeFragmentMeta, ProjectFile } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";
import { useDialogs } from "../components/Dialog";
import { buildTree, TreeItem } from "../components/FileTree";
import { bytes as fmtBytes } from "../lib/format";
// CodeMirror is heavy — load it only when an editor/viewer actually opens.
const CodeEditor = lazy(() => import("../components/CodeEditor").then((m) => ({ default: m.CodeEditor })));

type TplTab = "presets" | "blocks" | "definitions";

const sourceBadge = (source: string) =>
  source === "user"
    ? <span className="text-[10px] uppercase tracking-wide text-accent border border-accent/40 rounded px-1">yours</span>
    : <span className="text-[10px] uppercase tracking-wide text-muted border border-border rounded px-1">built-in</span>;

// Templates is the management surface for project presets and builder service
// blocks — both built-in (read-only) and user-saved (editable). Presets are
// created by "Save as template" from a project; blocks are created here.
export function Templates() {
  const [templates, setTemplates] = useState<ProjectTemplateMeta[] | null>(null);
  const [blocks, setBlocks] = useState<ServiceBlockMeta[] | null>(null);
  const [fragments, setFragments] = useState<ComposeFragmentMeta[] | null>(null);
  const [openTpl, setOpenTpl] = useState<ProjectTemplateMeta | null>(null); // file editor / viewer
  const [renameTpl, setRenameTpl] = useState<ProjectTemplateMeta | null>(null);
  const [openBlock, setOpenBlock] = useState<ServiceBlockMeta | "new" | null>(null);
  const [openFrag, setOpenFrag] = useState<ComposeFragmentMeta | "new" | null>(null);
  const [tab, setTab] = useState<TplTab>("presets");
  const dialogs = useDialogs();

  const load = useCallback(() => {
    api.projectTemplates().then(setTemplates).catch(() => setTemplates([]));
    api.serviceBlocks().then(setBlocks).catch(() => setBlocks([]));
    api.composeFragments().then(setFragments).catch(() => setFragments([]));
  }, []);
  useEffect(() => load(), [load]);

  const deleteTpl = async (t: ProjectTemplateMeta) => {
    if (!(await dialogs.confirm({ title: "Delete template", message: <>Delete <code className="font-mono text-text">{t.name}</code> and its files?</>, danger: true, confirmLabel: "Delete" }))) return;
    try { await api.deleteProjectTemplate(t.id); load(); }
    catch (e) { dialogs.alert({ title: "Could not delete", message: e instanceof ApiError ? e.message : "failed" }); }
  };
  const duplicateTpl = async (t: ProjectTemplateMeta) => {
    const name = await dialogs.prompt({ title: "Duplicate template", label: "Name for the copy", defaultValue: `${t.name} copy` });
    if (!name || !name.trim()) return;
    try { await api.duplicateProjectTemplate(t.id, name.trim()); load(); }
    catch (e) { dialogs.alert({ title: "Could not duplicate", message: e instanceof ApiError ? e.message : "failed" }); }
  };
  const deleteBlock = async (b: ServiceBlockMeta) => {
    if (!(await dialogs.confirm({ title: "Delete service block", message: <>Delete <code className="font-mono text-text">{b.name}</code>?</>, danger: true, confirmLabel: "Delete" }))) return;
    try { await api.deleteServiceBlock(b.id); load(); }
    catch (e) { dialogs.alert({ title: "Could not delete", message: e instanceof ApiError ? e.message : "failed" }); }
  };
  const deleteFrag = async (f: ComposeFragmentMeta) => {
    if (!(await dialogs.confirm({ title: "Delete shared definition", message: <>Delete <code className="font-mono text-text">{f.name}</code>?</>, danger: true, confirmLabel: "Delete" }))) return;
    try { await api.deleteComposeFragment(f.id); load(); }
    catch (e) { dialogs.alert({ title: "Could not delete", message: e instanceof ApiError ? e.message : "failed" }); }
  };

  if (!templates || !blocks || !fragments)
    return (<><PageHeader title="Templates" /><div className="p-6 flex items-center gap-2 text-muted"><Spinner /> Loading…</div></>);

  const tabBtn = (key: TplTab, icon: ReactNode, label: string, count: number) => (
    <button onClick={() => setTab(key)}
      className={clsx("flex items-center gap-2 px-3 py-1.5 text-sm rounded-lg border", tab === key ? "border-accent bg-accent/10 text-text" : "border-border text-muted hover:text-text")}>
      {icon} {label} <span className="text-[10px] text-muted">{count}</span>
    </button>
  );

  const headerAction =
    tab === "blocks" ? <button className="btn-primary px-3 py-1.5 text-sm" onClick={() => setOpenBlock("new")}><Plus className="h-4 w-4" /> New service block</button>
    : tab === "definitions" ? <button className="btn-primary px-3 py-1.5 text-sm" onClick={() => setOpenFrag("new")}><Plus className="h-4 w-4" /> New definition</button>
    : undefined;

  return (
    <>
      <PageHeader title="Templates" actions={headerAction} />
      <div className="p-6 space-y-4">
        <div className="flex flex-wrap gap-2">
          {tabBtn("presets", <LayoutTemplate className="h-4 w-4" />, "Presets", templates.length)}
          {tabBtn("blocks", <Puzzle className="h-4 w-4" />, "Service blocks", blocks.length)}
          {tabBtn("definitions", <Anchor className="h-4 w-4" />, "Shared definitions", fragments.length)}
        </div>

        {/* Presets ------------------------------------------------------------ */}
        {tab === "presets" && (
          templates.length === 0 ? (
            <EmptyState title="No presets" hint="Save a project as a template (the 🗎 button in the project editor) to add one." />
          ) : (
            <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
              {templates.map((t) => (
                <div key={`${t.source}:${t.id}`} className="card p-3 flex items-start gap-3">
                  <div className="min-w-0 flex-1">
                    <div className="text-sm font-medium flex items-center gap-2">{t.name} {sourceBadge(t.source)}</div>
                    <div className="text-xs text-muted truncate">{t.description || "—"}</div>
                    {!!t.variables?.length && <div className="text-[11px] text-muted mt-0.5">{t.variables.length} variable{t.variables.length > 1 ? "s" : ""}</div>}
                  </div>
                  <div className="flex items-center gap-1 shrink-0">
                    <button className="btn-ghost px-2 py-1" title={t.deletable ? "Edit files" : "View files"} onClick={() => setOpenTpl(t)}>
                      {t.deletable ? <FileText className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                    </button>
                    <button className="btn-ghost px-2 py-1" title="Duplicate into an editable copy" onClick={() => duplicateTpl(t)}><Copy className="h-4 w-4" /></button>
                    {t.deletable && <button className="btn-ghost px-2 py-1" title="Rename" onClick={() => setRenameTpl(t)}><Pencil className="h-4 w-4" /></button>}
                    {t.deletable && <a className="btn-ghost px-2 py-1" title="Download as .zip" href={api.templateDownloadUrl(t.id)}><Download className="h-4 w-4" /></a>}
                    {t.deletable && <button className="btn-ghost px-2 py-1 text-danger" title="Delete" onClick={() => deleteTpl(t)}><Trash2 className="h-4 w-4" /></button>}
                  </div>
                </div>
              ))}
            </div>
          )
        )}

        {/* Service blocks ----------------------------------------------------- */}
        {tab === "blocks" && (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
            {blocks.map((b) => (
              <div key={`${b.source}:${b.id}`} className="card p-3 flex items-start gap-3">
                <div className="min-w-0 flex-1">
                  <div className="text-sm font-medium flex items-center gap-2">{b.name} {sourceBadge(b.source)}</div>
                  <div className="text-xs text-muted truncate">{b.description || "—"}</div>
                  <div className="text-[11px] text-muted mt-0.5 font-mono">service: {b.service}</div>
                </div>
                <div className="flex items-center gap-1 shrink-0">
                  <button className="btn-ghost px-2 py-1" title={b.deletable ? "Edit" : "View"} onClick={() => setOpenBlock(b)}>
                    {b.deletable ? <Pencil className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                  </button>
                  {b.deletable && <button className="btn-ghost px-2 py-1 text-danger" title="Delete" onClick={() => deleteBlock(b)}><Trash2 className="h-4 w-4" /></button>}
                </div>
              </div>
            ))}
          </div>
        )}

        {/* Shared definitions (anchors) -------------------------------------- */}
        {tab === "definitions" && (
          <div className="space-y-2">
          <p className="text-xs text-muted">Top-level YAML anchors for the builder — define <code>x-name: &amp;name …</code> and merge it into services with <code>{"<<: *name"}</code>.</p>
          {fragments.length === 0 ? (
            <EmptyState title="No shared definitions" hint="Create one to reuse a common block (security, cert mounts, …) across services." />
          ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
            {fragments.map((f) => (
              <div key={`${f.source}:${f.id}`} className="card p-3 flex items-start gap-3">
                <div className="min-w-0 flex-1">
                  <div className="text-sm font-medium flex items-center gap-2">{f.name} {sourceBadge(f.source)}</div>
                  <div className="text-xs text-muted truncate">{f.description || "—"}</div>
                </div>
                <div className="flex items-center gap-1 shrink-0">
                  <button className="btn-ghost px-2 py-1" title={f.deletable ? "Edit" : "View"} onClick={() => setOpenFrag(f)}>
                    {f.deletable ? <Pencil className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                  </button>
                  {f.deletable && <button className="btn-ghost px-2 py-1 text-danger" title="Delete" onClick={() => deleteFrag(f)}><Trash2 className="h-4 w-4" /></button>}
                </div>
              </div>
            ))}
          </div>
          )}
          </div>
        )}
      </div>

      {openTpl && <TemplateFilesModal template={openTpl} onClose={() => setOpenTpl(null)} />}
      {renameTpl && <RenameTemplateModal template={renameTpl} onClose={() => setRenameTpl(null)} onSaved={() => { setRenameTpl(null); load(); }} />}
      {openBlock && <BlockModal block={openBlock} onClose={() => setOpenBlock(null)} onSaved={() => { setOpenBlock(null); load(); }} />}
      {openFrag && <FragmentModal fragment={openFrag} onClose={() => setOpenFrag(null)} onSaved={() => { setOpenFrag(null); load(); }} />}
    </>
  );
}

// FragmentModal creates, edits or views a shared definition. Built-in fragments
// open read-only; user fragments are editable; "new" starts blank.
function FragmentModal({ fragment, onClose, onSaved }: { fragment: ComposeFragmentMeta | "new"; onClose: () => void; onSaved: () => void }) {
  const isNew = fragment === "new";
  const readOnly = !isNew && fragment.source !== "user";
  const [name, setName] = useState(isNew ? "" : fragment.name);
  const [description, setDescription] = useState(isNew ? "" : fragment.description);
  const [content, setContent] = useState("");
  const [loading, setLoading] = useState(!isNew);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    if (isNew) return;
    api.composeFragment(fragment.id).then((d) => { setContent(d.content); setLoading(false); })
      .catch(() => { setErr("could not load definition"); setLoading(false); });
  }, [isNew, fragment]);

  const save = async () => {
    if (!name.trim() || !content.trim()) { setErr("name and YAML are required"); return; }
    setBusy(true); setErr("");
    const body = { name: name.trim(), description: description.trim(), content };
    try {
      if (isNew) await api.createComposeFragment(body);
      else await api.updateComposeFragment(fragment.id, body);
      onSaved();
    } catch (e) { setErr(e instanceof ApiError ? e.message : "could not save"); setBusy(false); }
  };

  const title = isNew ? "New shared definition" : readOnly ? "Shared definition" : "Edit shared definition";
  return (
    <div className="fixed inset-0 z-[60] bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <div className="card w-full max-w-xl flex flex-col max-h-[88vh]" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <Anchor className="h-4 w-4 text-accent" /><div className="font-medium">{title}</div>
          <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        {loading ? (
          <div className="p-6 flex items-center gap-2 text-muted"><Spinner /> Loading…</div>
        ) : (
          <>
            <div className="p-4 space-y-3 overflow-y-auto">
              <label className="block"><span className="label">Name</span><input className="input" value={name} readOnly={readOnly} placeholder="Postgres security" onChange={(e) => setName(e.target.value)} /></label>
              <label className="block"><span className="label">Description</span><input className="input" value={description} readOnly={readOnly} placeholder="What it shares" onChange={(e) => setDescription(e.target.value)} /></label>
              <label className="block">
                <span className="label">Top-level YAML (define an anchor with <code>&amp;name</code>)</span>
                <textarea className="input font-mono text-xs" rows={9} value={content} readOnly={readOnly} placeholder={"x-pg-common: &pg-common\n  restart: unless-stopped\n  volumes:\n    - ./certs:/certs:ro"} onChange={(e) => setContent(e.target.value)} />
              </label>
              {err && <p className="text-sm text-danger">{err}</p>}
            </div>
            <div className="flex justify-end gap-2 p-4 border-t border-border">
              <button type="button" className="btn-ghost px-3 py-1.5 text-sm" onClick={onClose}>{readOnly ? "Close" : "Cancel"}</button>
              {!readOnly && (
                <button type="button" className="btn-primary px-3 py-1.5 text-sm disabled:opacity-40" disabled={busy} onClick={save}>
                  {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />} Save
                </button>
              )}
            </div>
          </>
        )}
      </div>
    </div>
  );
}

// RenameTemplateModal edits a user preset's display name and description.
function RenameTemplateModal({ template, onClose, onSaved }: { template: ProjectTemplateMeta; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(template.name);
  const [description, setDescription] = useState(template.description);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setBusy(true); setErr("");
    try { await api.updateProjectTemplate(template.id, name.trim(), description.trim()); onSaved(); }
    catch (e2) { setErr(e2 instanceof ApiError ? e2.message : "could not save"); setBusy(false); }
  };
  return (
    <div className="fixed inset-0 z-[60] bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <form className="card w-full max-w-md flex flex-col" onClick={(e) => e.stopPropagation()} onSubmit={save}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <Pencil className="h-4 w-4 text-accent" /><div className="font-medium">Rename template</div>
          <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        <div className="p-4 space-y-3">
          <label className="block"><span className="label">Name</span><input autoFocus className="input" value={name} onChange={(e) => setName(e.target.value)} /></label>
          <label className="block"><span className="label">Description</span><input className="input" value={description} onChange={(e) => setDescription(e.target.value)} /></label>
          <p className="text-xs text-muted">The identifier (slug) won’t change, so existing references keep working.</p>
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

// BlockModal creates, edits or views a service block. Built-in blocks open
// read-only; user blocks are editable; "new" starts a blank block.
function BlockModal({ block, onClose, onSaved }: { block: ServiceBlockMeta | "new"; onClose: () => void; onSaved: () => void }) {
  const isNew = block === "new";
  const readOnly = !isNew && block.source !== "user";
  const [name, setName] = useState(isNew ? "" : block.name);
  const [service, setService] = useState("");
  const [description, setDescription] = useState(isNew ? "" : block.description);
  const [yaml, setYaml] = useState("");
  const [vols, setVols] = useState("");
  const [loading, setLoading] = useState(!isNew);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    if (isNew) return;
    api.serviceBlock(block.id).then((d) => {
      setService(d.service); setYaml(d.serviceYaml); setVols((d.volumes ?? []).join(", ")); setLoading(false);
    }).catch(() => { setErr("could not load block"); setLoading(false); });
  }, [isNew, block]);

  const save = async () => {
    if (!name.trim() || !service.trim() || !yaml.trim()) { setErr("name, service key and YAML are required"); return; }
    setBusy(true); setErr("");
    const body = { name: name.trim(), description: description.trim(), service: service.trim(), serviceYaml: yaml, volumes: vols.split(",").map((s) => s.trim()).filter(Boolean) };
    try {
      if (isNew) await api.createServiceBlock(body);
      else await api.updateServiceBlock(block.id, body);
      onSaved();
    } catch (e) { setErr(e instanceof ApiError ? e.message : "could not save block"); setBusy(false); }
  };

  const title = isNew ? "New service block" : readOnly ? "Service block" : "Edit service block";
  return (
    <div className="fixed inset-0 z-[60] bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <div className="card w-full max-w-xl flex flex-col max-h-[88vh]" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <Puzzle className="h-4 w-4 text-accent" /><div className="font-medium">{title}</div>
          <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>
        {loading ? (
          <div className="p-6 flex items-center gap-2 text-muted"><Spinner /> Loading…</div>
        ) : (
          <>
            <div className="p-4 space-y-3 overflow-y-auto">
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
                <label className="block"><span className="label">Name</span><input className="input" value={name} readOnly={readOnly} placeholder="My worker" onChange={(e) => setName(e.target.value)} /></label>
                <label className="block"><span className="label">Service key</span><input className="input font-mono" value={service} readOnly={readOnly} placeholder="worker" onChange={(e) => setService(e.target.value)} /></label>
              </div>
              <label className="block"><span className="label">Description</span><input className="input" value={description} readOnly={readOnly} placeholder="What it adds" onChange={(e) => setDescription(e.target.value)} /></label>
              <label className="block">
                <span className="label">Service YAML (indented under <code>services:</code>)</span>
                <textarea className="input font-mono text-xs" rows={8} value={yaml} readOnly={readOnly} placeholder={"  worker:\n    image: alpine\n    command: [\"sleep\", \"infinity\"]"} onChange={(e) => setYaml(e.target.value)} />
              </label>
              <label className="block"><span className="label">Named volumes (comma-separated, optional)</span><input className="input font-mono" value={vols} readOnly={readOnly} placeholder="workerdata" onChange={(e) => setVols(e.target.value)} /></label>
              {err && <p className="text-sm text-danger">{err}</p>}
            </div>
            <div className="flex justify-end gap-2 p-4 border-t border-border">
              <button type="button" className="btn-ghost px-3 py-1.5 text-sm" onClick={onClose}>{readOnly ? "Close" : "Cancel"}</button>
              {!readOnly && (
                <button type="button" className="btn-primary px-3 py-1.5 text-sm disabled:opacity-40" disabled={busy} onClick={save}>
                  {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />} Save
                </button>
              )}
            </div>
          </>
        )}
      </div>
    </div>
  );
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

// TemplateFilesModal is a multi-file editor over a preset's files. User presets
// are editable (save/add/delete/upload); built-ins open read-only. It reuses the
// project editor's file tree but has no compose/deploy tooling — a preset is just
// a snapshot, validated when a project is created from it.
function TemplateFilesModal({ template, onClose }: { template: ProjectTemplateMeta; onClose: () => void }) {
  const readOnly = template.source !== "user";
  const [files, setFiles] = useState<ProjectFile[] | null>(null);
  const [active, setActive] = useState("");
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState("");
  const [collapsedDirs, setCollapsedDirs] = useState<Set<string>>(new Set());
  const [currentDir, setCurrentDir] = useState("");
  const dialogs = useDialogs();
  const uploadRef = useRef<HTMLInputElement>(null);

  const dirOf = (path: string) => (path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : "");

  const loadFiles = useCallback((select?: string) => {
    const p = readOnly
      ? api.projectTemplate(template.id).then((d) => d.files.map((f): ProjectFile => ({ name: f.path, content: f.content, size: f.content.length, isDir: false })))
      : api.templateFiles(template.id);
    return p.then((fs) => {
      setFiles(fs);
      setActive((cur) => {
        const want = select ?? cur;
        const pick = fs.find((f) => !f.isDir && f.name === want) ?? fs.find((f) => f.name === "compose.yml") ?? fs.find((f) => !f.isDir);
        if (pick) setDraft(pick.content ?? "");
        return pick?.name ?? "";
      });
      return fs;
    }).catch(() => { setFiles([]); return [] as ProjectFile[]; });
  }, [template.id, readOnly]);
  useEffect(() => { loadFiles(); }, [loadFiles]);

  const original = files?.find((f) => f.name === active)?.content ?? "";
  const dirty = !readOnly && files != null && active !== "" && draft !== original;
  const activeFile = files?.find((f) => f.name === active);

  const toggleDir = (path: string) => setCollapsedDirs((s) => { const n = new Set(s); n.has(path) ? n.delete(path) : n.add(path); return n; });
  const enterDir = (path: string) => {
    setCurrentDir(path);
    setCollapsedDirs((s) => { if (!s.has(path)) return s; const n = new Set(s); n.delete(path); return n; });
  };

  const select = async (name: string) => {
    if (name === active) return;
    if (dirty && !(await dialogs.confirm({ title: "Discard unsaved changes?", message: "This file has unsaved edits.", danger: true, confirmLabel: "Discard" }))) return;
    setActive(name);
    setCurrentDir(dirOf(name));
    setDraft(files?.find((x) => x.name === name)?.content ?? "");
  };

  const save = async () => {
    setBusy("save");
    try { await api.writeTemplateFile(template.id, active, draft); loadFiles(active); }
    catch (e) { dialogs.alert({ title: "Save failed", message: e instanceof Error ? e.message : "unknown error" }); }
    finally { setBusy(""); }
  };
  const addFile = async () => {
    const name = await dialogs.prompt({ title: "New file", label: currentDir ? `File name (in ${currentDir}/)` : "File name", placeholder: "nginx.conf or scripts/init.sh" });
    if (!name) return;
    const full = currentDir ? `${currentDir}/${name}` : name;
    setBusy("add");
    try { await api.writeTemplateFile(template.id, full, ""); loadFiles(full); }
    catch (e) { dialogs.alert({ title: "Could not add file", message: e instanceof Error ? e.message : "unknown error" }); }
    finally { setBusy(""); }
  };
  const addDir = async () => {
    const name = await dialogs.prompt({ title: "New folder", label: currentDir ? `Folder name (in ${currentDir}/)` : "Folder name", placeholder: "config" });
    if (!name) return;
    const full = currentDir ? `${currentDir}/${name}` : name;
    setBusy("dir");
    try { await api.makeTemplateDir(template.id, full); setCurrentDir(full); loadFiles(); }
    catch (e) { dialogs.alert({ title: "Could not create folder", message: e instanceof Error ? e.message : "unknown error" }); }
    finally { setBusy(""); }
  };
  const removeEntry = async (f: { name: string; isDir?: boolean }) => {
    if (!(await dialogs.confirm({ title: `Delete ${f.isDir ? "folder" : "file"}`, message: <>Really delete <code className="font-mono text-text">{f.name}</code>?</>, danger: true, confirmLabel: "Delete" }))) return;
    setBusy("del");
    try { await api.deleteTemplateFile(template.id, f.name); loadFiles(f.name === active ? undefined : active); }
    catch (e) { dialogs.alert({ title: "Delete failed", message: e instanceof Error ? e.message : "unknown error" }); }
    finally { setBusy(""); }
  };
  const upload = async (file: File) => {
    const full = currentDir ? `${currentDir}/${file.name}` : file.name;
    setBusy("upload");
    try { await api.uploadTemplateFile(template.id, full, file); loadFiles(full); }
    catch (e) { dialogs.alert({ title: "Upload failed", message: e instanceof Error ? e.message : "unknown error" }); }
    finally { setBusy(""); if (uploadRef.current) uploadRef.current.value = ""; }
  };
  const downloadActive = () => {
    if (activeFile?.binary || activeFile?.tooLarge) {
      const a = document.createElement("a");
      a.href = api.templateFileDownloadUrl(template.id, active);
      a.click();
    } else { downloadText(active, draft); }
  };

  return (
    <div className="fixed inset-0 z-50 bg-black/60 grid place-items-center p-6" onClick={onClose}>
      <div className="card relative w-[85vw] h-[85vh] flex flex-col" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center gap-3 p-4 border-b border-border">
          <LayoutTemplate className="h-4 w-4 text-accent shrink-0" />
          <div className="min-w-0">
            <div className="font-medium truncate flex items-center gap-2">{template.name} {sourceBadge(template.source)}</div>
            <div className="text-xs text-muted">{readOnly ? "Read-only built-in preset" : "Editing template files"}</div>
          </div>
          <button className="btn-ghost px-2 h-8 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
        </div>

        <div className="flex-1 flex min-h-0">
          {/* File tree */}
          <div className="w-56 shrink-0 border-r border-border flex flex-col">
            {!readOnly && (
              <div className="flex items-center gap-1 p-2 border-b border-border">
                <span className="text-xs uppercase tracking-wide text-muted px-1">Files</span>
                <button className="btn-ghost px-1.5 py-1 ml-auto" title={`New file${currentDir ? ` in ${currentDir}/` : ""}`} onClick={addFile}><FilePlus className="h-4 w-4" /></button>
                <button className="btn-ghost px-1.5 py-1" title={`New folder${currentDir ? ` in ${currentDir}/` : ""}`} onClick={addDir}><FolderPlus className="h-4 w-4" /></button>
                <button className="btn-ghost px-1.5 py-1" title="Upload file" onClick={() => uploadRef.current?.click()}><Upload className="h-4 w-4" /></button>
                <input ref={uploadRef} type="file" className="hidden" onChange={(e) => e.target.files?.[0] && upload(e.target.files[0])} />
              </div>
            )}
            {!readOnly && currentDir && (
              <div className="flex items-center gap-1 px-2 py-1 border-b border-border text-[11px] text-accent font-mono" title={`New items are created in ${currentDir}/`}>
                <Folder className="h-3 w-3 shrink-0" /><span className="truncate">{currentDir}/</span>
                <button className="ml-auto text-muted hover:text-text" title="Create in the root" onClick={() => setCurrentDir("")}><X className="h-3 w-3" /></button>
              </div>
            )}
            <div className="flex-1 overflow-auto p-1">
              {files === null ? <div className="p-3 text-muted text-sm flex items-center gap-2"><Spinner /> …</div> :
                files.length === 0 ? <div className="p-3 text-muted text-xs">No files</div> :
                buildTree(files).map((n) => (
                  <TreeItem key={n.path} node={n} depth={0} active={active} dirty={dirty} collapsed={collapsedDirs} currentDir={currentDir} onToggle={toggleDir} onSelect={select} onEnterDir={enterDir} onDelete={readOnly ? undefined : removeEntry} />
                ))}
            </div>
          </div>

          {/* Editor */}
          <div className="flex-1 flex flex-col min-w-0">
            <div className="flex items-center gap-2 p-2 border-b border-border">
              <span className="text-xs font-mono text-muted truncate">{active || "—"}</span>
              <div className="ml-auto flex items-center gap-1">
                <button className="btn-ghost px-2 py-1 text-xs disabled:opacity-40" disabled={!active} title="Download this file" onClick={downloadActive}><Download className="h-3.5 w-3.5" /></button>
                {!readOnly && (
                  <button className="btn-primary px-3 py-1 text-xs disabled:opacity-40" disabled={!dirty || busy === "save" || !active || activeFile?.binary} onClick={save}>
                    {busy === "save" ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />} Save
                  </button>
                )}
              </div>
            </div>
            {activeFile?.tooLarge ? (
              <div className="p-4 text-sm text-muted">This file is too large to edit here.</div>
            ) : activeFile?.binary ? (
              <div className="p-4 flex flex-col items-start gap-3 text-sm text-muted">
                <div className="flex items-center gap-2"><FileBox className="h-4 w-4" /> Binary file ({fmtBytes(activeFile.size)}) — can’t be edited as text.</div>
                <button className="btn-ghost px-3 py-1.5 text-xs" onClick={downloadActive}><Download className="h-3.5 w-3.5" /> Download</button>
              </div>
            ) : active ? (
              <div className="flex-1 min-h-0 overflow-hidden">
                <Suspense fallback={<div className="h-full grid place-items-center text-muted"><Spinner /></div>}>
                  <CodeEditor filename={active} value={draft} onChange={setDraft} readOnly={readOnly} />
                </Suspense>
              </div>
            ) : (
              <div className="flex-1 grid place-items-center text-sm text-muted">Select a file</div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
