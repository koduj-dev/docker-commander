import { useEffect, useMemo, useState } from "react";
import { api } from "../lib/api";
import type { AuditEntry, Host } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";
import { useListControls, SearchBar, Pager } from "../components/ListControls";

// Load a generous recent window and paginate it client-side, so the audit log
// gets the same search + prev/next pagination as the other lists.
const RECENT = 1000;

// hostLabel names the host an entry reached. 0 means the action has no host
// (entries written before 1.7.0 also used 0 for the local daemon). An id missing
// from the list is a host this account can't see or one deleted since; it is
// shown by id rather than guessed at.
export function hostLabel(id: number, names: Map<number, string>): string {
  if (!id) return "—";
  return names.get(id) ?? `#${id}`;
}

function matchAudit(e: AuditEntry, q: string, names: Map<number, string>): boolean {
  return (
    (e.username ?? "").toLowerCase().includes(q) ||
    e.action.toLowerCase().includes(q) ||
    (e.target ?? "").toLowerCase().includes(q) ||
    (e.detail ?? "").toLowerCase().includes(q) ||
    (e.ip ?? "").toLowerCase().includes(q) ||
    (e.hostId ? hostLabel(e.hostId, names).toLowerCase().includes(q) : false)
  );
}

export function Audit() {
  const [entries, setEntries] = useState<AuditEntry[] | null>(null);
  const [hosts, setHosts] = useState<Host[]>([]);

  useEffect(() => {
    // Both before the first render of the list: the search filter is memoised on
    // the entries, so host names arriving later wouldn't reach a remembered query.
    // Only the hosts this account may see are listed; any other id shows as #id.
    Promise.all([api.audit(RECENT), api.hosts().catch(() => [] as Host[])])
      .then(([e, h]) => { setHosts(h); setEntries(e); })
      .catch(() => setEntries([]));
  }, []);

  const names = useMemo(() => new Map(hosts.map((h) => [h.id, h.name])), [hosts]);
  const controls = useListControls(entries ?? [], (e, q) => matchAudit(e, q, names), { storageKey: "audit" });

  return (
    <>
      <PageHeader title="Audit log" />
      <div className="p-6 space-y-3">
        {!entries ? (
          <div className="flex items-center gap-2 text-muted"><Spinner /> Loading…</div>
        ) : entries.length === 0 ? (
          <EmptyState title="No audit entries yet" />
        ) : (
          <>
            <SearchBar controls={controls} placeholder="Search by user, action, target, detail, host, IP…" />
            <div className="card overflow-hidden">
              <table className="w-full text-sm">
                <thead className="text-muted text-xs uppercase tracking-wide">
                  <tr className="border-b border-border">
                    <th className="text-left font-medium px-4 py-3">Time</th>
                    <th className="text-left font-medium px-4 py-3">User</th>
                    <th className="text-left font-medium px-4 py-3">Action</th>
                    <th className="text-left font-medium px-4 py-3">Target</th>
                    <th className="text-left font-medium px-4 py-3 hidden md:table-cell">Host</th>
                    <th className="text-left font-medium px-4 py-3 hidden md:table-cell">IP</th>
                  </tr>
                </thead>
                <tbody>
                  {controls.pageItems.map((e) => (
                    <tr key={e.id} className="border-b border-border/50 hover:bg-panel2/40">
                      <td className="px-4 py-2.5 text-muted whitespace-nowrap">{e.createdAt.slice(0, 19).replace("T", " ")}</td>
                      <td className="px-4 py-2.5">{e.username || "—"}</td>
                      <td className="px-4 py-2.5"><code className="font-mono text-xs text-accent">{e.action}</code></td>
                      <td className="px-4 py-2.5 break-all">
                        <span className="font-mono text-xs text-muted">{e.target}</span>
                        {e.detail && <div className="text-xs text-muted/80 mt-0.5 break-words">{e.detail}</div>}
                      </td>
                      <td
                        className="px-4 py-2.5 hidden md:table-cell text-muted whitespace-nowrap"
                        title={e.hostId ? undefined : "No host. Entries from before 1.7.0 show this for the local daemon too."}
                      >
                        {hostLabel(e.hostId, names)}
                      </td>
                      <td className="px-4 py-2.5 hidden md:table-cell text-muted">{e.ip}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <Pager controls={controls} />
            {entries.length >= RECENT && (
              <p className="text-xs text-muted">Showing the {RECENT} most recent entries.</p>
            )}
          </>
        )}
      </div>
    </>
  );
}
