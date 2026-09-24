import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../lib/api";
import type { TopTalker } from "../lib/types";
import { rate } from "../lib/format";
import { Spinner } from "./ui";

const WIDGET_LIMIT = 6;

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

  return (
    <div className="card p-4">
      <div className="flex items-baseline justify-between mb-2">
        <div className="text-xs uppercase tracking-wide text-muted">Top talkers · last 5 min</div>
        <Link to="/resources?tab=network" className="text-xs text-accent hover:underline">
          View all →
        </Link>
      </div>
      {/* Only the loading/error/empty states reserve a box; a ranked list is as
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
        <ul className="divide-y divide-border/50">
          {talkers.map((t, i) => (
            <li key={t.id} className="flex items-center gap-2 text-sm py-1.5 first:pt-0 last:pb-0">
              <span className="text-muted text-xs w-4 shrink-0 text-right">{i + 1}</span>
              <span className="truncate flex-1" title={t.name}>{t.name}</span>
              <span className="text-muted shrink-0 font-mono text-xs">{rate(t.rate)}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
