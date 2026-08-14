// Collapsed/expanded state of the sidebar nav groups (Workloads, Storage, …).
// Persisted in localStorage — this is per-browser UI chrome, not an account
// setting, so it follows the same pattern as the host switcher (lib/host.ts)
// rather than the server-backed prefs store.

const KEY = "dc.sidebar.collapsed";

function read(): Set<string> {
  try {
    const raw = localStorage.getItem(KEY);
    return new Set(raw ? (JSON.parse(raw) as string[]) : []);
  } catch {
    return new Set();
  }
}

let collapsed: Set<string> = read();

/** The titles of groups currently collapsed. Callers should copy before mutating. */
export function getCollapsedGroups(): Set<string> {
  return collapsed;
}

export function setGroupCollapsed(title: string, isCollapsed: boolean): void {
  const next = new Set(collapsed);
  if (isCollapsed) next.add(title);
  else next.delete(title);
  collapsed = next;
  try {
    localStorage.setItem(KEY, JSON.stringify([...collapsed]));
  } catch {
    /* ignore quota errors */
  }
}
