import { ChevronRight, FileBox, FileText, Folder, Trash2 } from "lucide-react";
import type { ProjectFile } from "../lib/types";

// Shared file-tree for the project and template multi-file editors. buildTree
// turns the flat file list (paths like "config/app.conf") into a nested tree;
// TreeItem renders one node (folder or file) recursively.

export type TreeNode = { name: string; path: string; isDir: boolean; binary?: boolean; children: TreeNode[] };

// buildTree materialises intermediate folders so a file at "a/b/c.txt" nests
// under folders a → b even when those folders aren't listed explicitly.
export function buildTree(files: ProjectFile[]): TreeNode[] {
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

// TreeItem renders one tree node (folder or file) recursively. onDelete is
// optional — omit it (read-only viewers) to hide the delete affordance.
export function TreeItem({ node, depth, active, dirty, collapsed, currentDir, onToggle, onSelect, onEnterDir, onDelete }: {
  node: TreeNode; depth: number; active: string; dirty: boolean; currentDir: string;
  collapsed: Set<string>; onToggle: (path: string) => void;
  onSelect: (path: string) => void; onEnterDir: (path: string) => void; onDelete?: (n: { name: string; isDir?: boolean }) => void;
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
          {onDelete && <button className="ml-auto opacity-0 group-hover:opacity-100 text-danger" title="Delete folder" onClick={(e) => { e.stopPropagation(); onDelete({ name: node.path, isDir: true }); }}><Trash2 className="h-3.5 w-3.5" /></button>}
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
      {onDelete && <button className="ml-auto opacity-0 group-hover:opacity-100 text-danger" title="Delete file" onClick={(e) => { e.stopPropagation(); onDelete({ name: node.path, isDir: false }); }}><Trash2 className="h-3.5 w-3.5" /></button>}
    </div>
  );
}
