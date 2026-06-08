import { useCallback, useEffect, useRef, useState } from "react";
import { Folder, File as FileIcon, Link2, Download, Trash2, Upload, RefreshCw, ChevronRight, Loader2, FolderPlus, FileArchive } from "lucide-react";
import type { FileEntry, FileApi } from "../lib/types";
import { bytes } from "../lib/format";
import { Spinner } from "./ui";
import { useDialogs } from "./Dialog";
import { triggerDownload } from "./LoadModal";

function joinPath(dir: string, name: string): string {
  return dir === "/" ? "/" + name : dir + "/" + name;
}

// FileBrowser is a file manager over a FileApi adapter: navigate directories,
// download files/dirs, upload into the current directory, and delete paths.
// The same UI serves containers (docker cp) and volumes (helper container).
export function FileBrowser({ fs }: { fs: FileApi }) {
  const [path, setPath] = useState("/");
  const [entries, setEntries] = useState<FileEntry[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const fileRef = useRef<HTMLInputElement>(null);
  const extractRef = useRef<HTMLInputElement>(null);
  const dialogs = useDialogs();

  const load = useCallback(async (p: string) => {
    setError(""); setEntries(null);
    try {
      const r = await fs.list(p);
      if (r.ok) { setEntries(r.entries ?? []); setPath(r.path ?? p); }
      else { setError(r.error ?? "could not list directory"); setEntries([]); }
    } catch {
      setError("request failed"); setEntries([]);
    }
  }, [fs]);

  useEffect(() => { load("/"); }, [load]);

  const go = (p: string) => load(p);

  const onUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    setBusy("upload");
    try {
      const r = await fs.upload(path, file);
      if (!r.ok) setError(r.error ?? "upload failed");
      await load(path);
    } catch {
      setError("upload failed");
    } finally {
      setBusy(""); if (fileRef.current) fileRef.current.value = "";
    }
  };

  const onExtract = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    setBusy("extract");
    try {
      const r = await fs.uploadExtract(path, file);
      if (!r.ok) setError(r.error ?? "extract failed");
      await load(path);
    } catch {
      setError("extract failed");
    } finally {
      setBusy(""); if (extractRef.current) extractRef.current.value = "";
    }
  };

  const newFolder = async () => {
    const name = await dialogs.prompt({ title: "New folder", label: "Folder name", placeholder: "data" });
    if (!name) return;
    setBusy("mkdir");
    try {
      const r = await fs.mkdir(joinPath(path, name));
      if (!r.ok) setError(r.error ?? "could not create folder");
      await load(path);
    } catch {
      setError("request failed");
    } finally {
      setBusy("");
    }
  };

  const del = async (entry: FileEntry) => {
    if (!(await dialogs.confirm({ title: `Delete ${entry.isDir ? "folder" : "file"}`, message: <>Delete <code className="font-mono text-text">{entry.name}</code>?</>, danger: true, confirmLabel: "Delete" }))) return;
    const full = joinPath(path, entry.name);
    setBusy(full);
    try {
      const r = await fs.del(full);
      if (!r.ok) setError(r.error ?? "delete failed");
      await load(path);
    } catch {
      setError("delete failed");
    } finally {
      setBusy("");
    }
  };

  // Breadcrumb segments from the current path.
  const segments = path.split("/").filter(Boolean);
  const crumb = (i: number) => "/" + segments.slice(0, i + 1).join("/");

  const sorted = (entries ?? []).slice().sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
    return a.name.localeCompare(b.name);
  });

  return (
    <div className="card flex flex-col min-h-0">
      {/* toolbar: breadcrumb + actions */}
      <div className="flex items-center gap-2 p-3 border-b border-border flex-wrap">
        <div className="flex items-center gap-1 text-sm flex-1 min-w-0 flex-wrap">
          <button className="text-accent hover:underline" onClick={() => go("/")}>/</button>
          {segments.map((seg, i) => (
            <span key={i} className="flex items-center gap-1 min-w-0">
              <ChevronRight className="h-3.5 w-3.5 text-muted shrink-0" />
              <button className="text-accent hover:underline truncate" onClick={() => go(crumb(i))}>{seg}</button>
            </span>
          ))}
        </div>
        <button className="btn-ghost px-2 py-1.5" title="Refresh" onClick={() => load(path)}><RefreshCw className="h-4 w-4" /></button>
        <button className="btn-ghost px-2 py-1.5 text-xs" title="Download current directory as tar" onClick={() => triggerDownload(fs.downloadUrl(path))}>
          <Download className="h-4 w-4" /> Dir
        </button>
        <button className="btn-ghost px-2 py-1.5" title="New folder" onClick={newFolder} disabled={busy === "mkdir"}>
          {busy === "mkdir" ? <Loader2 className="h-4 w-4 animate-spin" /> : <FolderPlus className="h-4 w-4" />}
        </button>
        <button className="btn-ghost px-2.5 py-1.5 text-xs" onClick={() => extractRef.current?.click()} disabled={busy === "extract"} title="Upload a .zip / .tar / .tar.gz and extract it here">
          {busy === "extract" ? <Loader2 className="h-4 w-4 animate-spin" /> : <FileArchive className="h-4 w-4" />} Extract
        </button>
        <input ref={extractRef} type="file" accept=".zip,.tar,.tar.gz,.tgz,application/zip,application/x-tar,application/gzip" className="hidden" onChange={onExtract} />
        <button className="btn-primary px-2.5 py-1.5 text-xs" onClick={() => fileRef.current?.click()} disabled={busy === "upload"}>
          {busy === "upload" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Upload className="h-4 w-4" />} Upload
        </button>
        <input ref={fileRef} type="file" className="hidden" onChange={onUpload} />
      </div>

      {error && <div className="px-3 py-2 text-xs text-danger border-b border-border break-all">{error}</div>}

      <div className="overflow-auto max-h-112">
        {entries === null ? (
          <div className="flex items-center gap-2 text-muted text-sm p-4"><Spinner className="h-4 w-4" /> Loading…</div>
        ) : sorted.length === 0 ? (
          <div className="text-sm text-muted p-4">Empty directory.</div>
        ) : (
          <table className="w-full text-sm">
            <tbody>
              {sorted.map((e) => {
                const full = joinPath(path, e.name);
                return (
                  <tr key={e.name} className="border-b border-border/40 hover:bg-panel2/40">
                    <td className="px-3 py-1.5 w-6">
                      {e.isDir ? <Folder className="h-4 w-4 text-accent" /> : e.isLink ? <Link2 className="h-4 w-4 text-warn" /> : <FileIcon className="h-4 w-4 text-muted" />}
                    </td>
                    <td className="px-1 py-1.5">
                      {e.isDir ? (
                        <button className="hover:text-accent text-left" onClick={() => go(full)}>{e.name}</button>
                      ) : (
                        <span className="break-all">{e.name}</span>
                      )}
                      {e.isLink && e.target && <span className="text-muted/60 text-xs"> → {e.target}</span>}
                    </td>
                    <td className="px-3 py-1.5 text-right text-xs text-muted font-mono whitespace-nowrap">{e.isDir ? "" : bytes(e.size)}</td>
                    <td className="px-3 py-1.5 text-xs text-muted font-mono whitespace-nowrap hidden md:table-cell">{e.mode}</td>
                    <td className="px-3 py-1.5">
                      <div className="flex items-center justify-end gap-1">
                        <button className="btn-ghost px-1.5 py-1" title="Download" onClick={() => triggerDownload(fs.downloadUrl(full))}><Download className="h-3.5 w-3.5" /></button>
                        <button className="btn-ghost px-1.5 py-1 text-danger" title="Delete" disabled={busy === full} onClick={() => del(e)}>
                          {busy === full ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Trash2 className="h-3.5 w-3.5" />}
                        </button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
