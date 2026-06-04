import { useCallback, useEffect, useState } from "react";
import { api } from "../lib/api";
import type { AuditEntry } from "../lib/types";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";

const PAGE = 50;

export function Audit() {
  const [entries, setEntries] = useState<AuditEntry[] | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [done, setDone] = useState(false); // no older entries left

  useEffect(() => {
    api
      .audit(PAGE)
      .then((rows) => {
        setEntries(rows);
        setDone(rows.length < PAGE);
      })
      .catch(() => setEntries([]));
  }, []);

  const loadMore = useCallback(async () => {
    if (!entries || entries.length === 0) return;
    setLoadingMore(true);
    try {
      const before = entries[entries.length - 1].id; // oldest loaded id
      const older = await api.audit(PAGE, before);
      setEntries((cur) => [...(cur ?? []), ...older]);
      if (older.length < PAGE) setDone(true);
    } finally {
      setLoadingMore(false);
    }
  }, [entries]);

  return (
    <>
      <PageHeader title="Audit log" />
      <div className="p-6 space-y-4">
        {!entries ? (
          <div className="flex items-center gap-2 text-muted"><Spinner /> Loading…</div>
        ) : entries.length === 0 ? (
          <EmptyState title="No audit entries yet" />
        ) : (
          <>
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
                  {entries.map((e) => (
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
            <div className="flex items-center gap-3">
              {!done ? (
                <button className="btn-ghost text-sm" onClick={loadMore} disabled={loadingMore}>
                  {loadingMore ? "Loading…" : "Load more"}
                </button>
              ) : (
                <span className="text-xs text-muted">End of log.</span>
              )}
              <span className="text-xs text-muted">{entries.length} shown</span>
            </div>
          </>
        )}
      </div>
    </>
  );
}
