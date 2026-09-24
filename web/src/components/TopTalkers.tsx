import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../lib/api";
import type { TopTalker } from "../lib/types";
import { rate } from "../lib/format";
import { Spinner } from "./ui";

const WIDGET_LIMIT = 10; // matches the Top consumers table it sits beside

// TopTalkers is the dashboard's small ranked preview — the full ranked table
// with a window/metric selector lives on its own page. Ranked over a STORED
// window, never a point-in-time poll sample: see ResourceBreakdown's own
// comment on why a live per-poll ranking is unreadable (throughput is
// bursty, so the order would reorder itself on every poll).
export function TopTalkers({ tick = 0 }: { tick?: number }) {
  const [talkers, setTalkers] = useState<TopTalker[] | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    const load = () =>
      api
        .topTalkers("5m", "total", WIDGET_LIMIT)
        .then((d) => {
          setTalkers(d.containers ?? []);
          setError("");
        })
        .catch((e) => setError(e instanceof Error ? e.message : "could not rank containers"));
    load();
    const t = setInterval(load, 15000);
    return () => clearInterval(t);
  }, [tick]);

  // Same table design as ResourceTable ("Top consumers"), which sits beside
  // this on the dashboard: card, title row, header row, same row height.
  return (
    <div className="card overflow-hidden">
      <div className="flex items-baseline justify-between px-4 pt-4">
        <div className="text-xs uppercase tracking-wide text-muted">Top talkers · last 5 min</div>
        <Link to="/resources?tab=network" className="text-xs text-accent hover:underline">
          View all →
        </Link>
      </div>
      {/* Only the loading/error/empty states reserve a box; a ranked table is as
          tall as its rows, so a short list doesn't leave dead space below it. */}
      {error && !talkers ? (
        <div className="h-32 grid place-items-center text-sm text-danger">{error}</div>
      ) : !talkers ? (
        <div className="h-32 grid place-items-center">
          <Spinner />
        </div>
      ) : talkers.length === 0 ? (
        <div className="h-32 grid place-items-center text-sm text-muted text-center px-4">
          Not enough history yet — check back in a few minutes.
        </div>
      ) : (
        <table className="w-full text-sm mt-2">
          <thead className="text-muted text-xs">
            <tr className="border-b border-border">
              <th className="text-left font-medium px-4 py-3 uppercase tracking-wide">Container</th>
              <th className="text-right font-medium px-4 py-3 uppercase tracking-wide">Received</th>
              <th className="text-right font-medium px-4 py-3 uppercase tracking-wide">Sent</th>
            </tr>
          </thead>
          <tbody>
            {talkers.map((t) => (
              <tr key={t.id} className="border-b border-border/50 last:border-0">
                <td className="px-4 py-2 font-medium w-full max-w-0 truncate">
                  <Link to={`/containers/${t.id}`} className="hover:underline" title={t.name}>{t.name}</Link>
                </td>
                <td className="px-4 py-2 text-right font-mono text-xs whitespace-nowrap">{rate(t.rxRate)}</td>
                <td className="px-4 py-2 text-right font-mono text-xs whitespace-nowrap">{rate(t.txRate)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
