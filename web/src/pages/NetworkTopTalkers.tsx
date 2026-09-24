import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../lib/api";
import type { TopTalker } from "../lib/types";
import { rate } from "../lib/format";
import { PageHeader } from "../layout/Shell";
import { EmptyState, Spinner } from "../components/ui";

const WINDOWS = [
  { value: "5m", label: "Last 5 min" },
  { value: "15m", label: "Last 15 min" },
  { value: "1h", label: "Last hour" },
];
const METRICS = [
  { value: "total", label: "Total (RX + TX)" },
  { value: "netrx", label: "Received" },
  { value: "nettx", label: "Sent" },
];

// The full ranked table behind the Dashboard's small preview widget. Ranked
// over a STORED window (averaged rate, oldest-to-newest point in the
// window), never a point-in-time poll sample — a live ranking reorders
// itself every 8-15s and is unreadable, the same reason the dashboard's
// ResourceBreakdown doesn't rank network there either.
const RESULT_LIMIT = 50;

export function NetworkTopTalkers() {
  const [window, setWindowSel] = useState("5m");
  const [metric, setMetric] = useState("total");
  const [talkers, setTalkers] = useState<TopTalker[] | null>(null);
  const [total, setTotal] = useState(0);
  const [error, setError] = useState("");

  // text is debounced into query so typing doesn't fire a request per
  // keystroke (same pattern as Alerts' text filter).
  const [text, setText] = useState("");
  const [query, setQuery] = useState("");
  useEffect(() => {
    const t = setTimeout(() => setQuery(text), 350);
    return () => clearTimeout(t);
  }, [text]);

  useEffect(() => {
    setTalkers(null);
    setError("");
    // Guards against an in-flight request for the PREVIOUS window/metric/query
    // resolving after a newer one already landed — without this, switching
    // selectors quickly enough could let the stale response overwrite the
    // fresh one, showing e.g. "Last hour" selected while the table still
    // holds the 5-minute ranking until the next poll quietly corrects it.
    let cancelled = false;
    const load = () =>
      api
        .topTalkers(window, metric, RESULT_LIMIT, query.trim() || undefined)
        .then((d) => { if (!cancelled) { setTalkers(d.containers ?? []); setTotal(d.total ?? 0); } })
        .catch((e) => { if (!cancelled) setError(e instanceof Error ? e.message : "could not rank containers"); });
    load();
    const t = setInterval(load, 15000);
    return () => { cancelled = true; clearInterval(t); };
  }, [window, metric, query]);

  return (
    <>
      <PageHeader title="Top talkers" />
      <div className="p-6 space-y-4">
        <p className="text-sm text-muted">
          Containers ranked by network throughput, averaged over a stored window — not a snapshot of the current poll,
          which is too bursty to rank meaningfully. See the{" "}
          <Link to="/" className="text-accent hover:underline">Dashboard</Link> for a live host-wide total.
        </p>
        <div className="flex flex-wrap items-center gap-3">
          <select className="input w-auto" value={window} onChange={(e) => setWindowSel(e.target.value)}>
            {WINDOWS.map((w) => (
              <option key={w.value} value={w.value}>{w.label}</option>
            ))}
          </select>
          <select className="input w-auto" value={metric} onChange={(e) => setMetric(e.target.value)}>
            {METRICS.map((m) => (
              <option key={m.value} value={m.value}>{m.label}</option>
            ))}
          </select>
          <input
            type="search"
            className="input w-auto min-w-[12rem]"
            placeholder="Filter by container name…"
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
          {talkers && total > talkers.length && (
            <span className="text-xs text-muted ml-auto">
              Showing {talkers.length} of {total} — narrow the filter to find one outside this list.
            </span>
          )}
        </div>

        {error && !talkers && <div className="text-sm text-danger">{error}</div>}
        {!talkers ? (
          <div className="flex items-center gap-2 text-muted">
            <Spinner /> Loading…
          </div>
        ) : talkers.length === 0 ? (
          <EmptyState
            title={query.trim() ? "No container matches that name" : "Not enough history yet"}
            hint={
              query.trim()
                ? "Try a shorter or different filter."
                : "Throughput needs at least two samples inside the chosen window — check back in a few minutes, or pick a longer window."
            }
          />
        ) : (
          <div className="card overflow-hidden">
            <table className="w-full text-sm">
              <thead className="text-muted text-xs uppercase tracking-wide">
                <tr className="border-b border-border">
                  <th className="text-left font-medium px-4 py-3">#</th>
                  <th className="text-left font-medium px-4 py-3">Container</th>
                  <th className="text-left font-medium px-4 py-3">Host</th>
                  <th className="text-right font-medium px-4 py-3">Received</th>
                  <th className="text-right font-medium px-4 py-3">Sent</th>
                </tr>
              </thead>
              <tbody>
                {talkers.map((t, i) => (
                  <tr key={t.id} className="border-b border-border/50">
                    <td className="px-4 py-2.5 text-muted">{i + 1}</td>
                    <td className="px-4 py-2.5 font-medium">
                      <Link to={`/containers/${t.id}`} className="hover:underline">{t.name}</Link>
                    </td>
                    <td className="px-4 py-2.5 text-muted">{t.hostName}</td>
                    <td className="px-4 py-2.5 text-right font-mono text-xs">{rate(t.rxRate)}</td>
                    <td className="px-4 py-2.5 text-right font-mono text-xs">{rate(t.txRate)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}
