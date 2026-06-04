import { useEffect, useState } from "react";
import { api } from "../lib/api";
import type { AuditEntry } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";
import { useListControls, SearchBar, Pager } from "../components/ListControls";

// Load a generous recent window and paginate it client-side, so the audit log
// gets the same search + prev/next pagination as the other lists.
const RECENT = 1000;

function matchAudit(e: AuditEntry, q: string): boolean {
  return (
    (e.username ?? "").toLowerCase().includes(q) ||
    e.action.toLowerCase().includes(q) ||
    (e.target ?? "").toLowerCase().includes(q) ||
    (e.ip ?? "").toLowerCase().includes(q)
  );
}

export function Audit() {
  const [entries, setEntries] = useState<AuditEntry[] | null>(null);

  useEffect(() => {
    api.audit(RECENT).then(setEntries).catch(() => setEntries([]));
  }, []);

  const controls = useListControls(entries ?? [], matchAudit, { storageKey: "audit" });

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
            <SearchBar controls={controls} placeholder="Search by user, action, target, IP…" />
            <div className="card overflow-hidden">
              <table className="w-full text-sm">
                <thead className="text-muted text-xs uppercase tracking-wide">
                  <tr className="border-b border-border">
                    <th className="text-left font-medium px-4 py-3">Time</th>
                    <th className="text-left font-medium px-4 py-3">User</th>
                    <th className="text-left font-medium px-4 py-3">Action</th>
                    <th className="text-left font-medium px-4 py-3">Target</th>
                    <th className="text-left font-medium px-4 py-3 hidden md:table-cell">IP</th>
                  </tr>
                </thead>
                <tbody>
                  {controls.pageItems.map((e) => (
                    <tr key={e.id} className="border-b border-border/50 hover:bg-panel2/40">
                      <td className="px-4 py-2.5 text-muted whitespace-nowrap">{e.createdAt.slice(0, 19).replace("T", " ")}</td>
                      <td className="px-4 py-2.5">{e.username || "—"}</td>
                      <td className="px-4 py-2.5"><code className="font-mono text-xs text-accent">{e.action}</code></td>
                      <td className="px-4 py-2.5 font-mono text-xs text-muted break-all">{e.target}</td>
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
