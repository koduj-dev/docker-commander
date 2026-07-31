import type { StatsSample } from "../lib/types";
import { bytes } from "../lib/format";

// Cumulative network totals for a container, with the per-interface breakdown.
//
// This complements the live throughput chart: the chart answers "what is it doing
// right now", this answers "what has it done, and is anything going wrong". Drops
// and errors are usually zero and easy to dismiss as noise — but when they are
// not zero they are frequently the only visible sign of the problem, so they get
// their own colour rather than being buried in a table nobody reads.
export function NetworkStats({ latest }: { latest?: StatsSample }) {
  if (!latest) return null;

  const drops = latest.netRxDropped + latest.netTxDropped;
  const errors = latest.netRxErrors + latest.netTxErrors;
  const ifaces = latest.interfaces ?? [];

  return (
    <div className="card p-4 space-y-3">
      <div className="flex items-baseline gap-2">
        <span className="text-xs uppercase tracking-wide text-muted">Network totals</span>
        <span className="text-xs text-muted">since the container started</span>
      </div>

      <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
        <Stat label="Received" value={bytes(latest.netRx)} sub={`${latest.netRxPackets.toLocaleString()} packets`} />
        <Stat label="Sent" value={bytes(latest.netTx)} sub={`${latest.netTxPackets.toLocaleString()} packets`} />
        <Stat label="Dropped" value={drops.toLocaleString()} tone={drops > 0 ? "warn" : undefined} />
        <Stat label="Errors" value={errors.toLocaleString()} tone={errors > 0 ? "danger" : undefined} />
      </div>

      {ifaces.length > 1 && (
        <div className="pt-1">
          <div className="text-xs uppercase tracking-wide text-muted mb-1.5">Per interface</div>
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead className="text-muted">
                <tr className="border-b border-border">
                  <th className="text-left font-medium py-1.5 pr-3">Interface</th>
                  <th className="text-right font-medium py-1.5 px-3">RX</th>
                  <th className="text-right font-medium py-1.5 px-3">TX</th>
                  <th className="text-right font-medium py-1.5 px-3">Dropped</th>
                  <th className="text-right font-medium py-1.5 pl-3">Errors</th>
                </tr>
              </thead>
              <tbody>
                {ifaces.map((i) => (
                  <tr key={i.name} className="border-b border-border/40 last:border-0">
                    <td className="py-1.5 pr-3 font-mono">{i.name}</td>
                    <td className="py-1.5 px-3 text-right">{bytes(i.rxBytes)}</td>
                    <td className="py-1.5 px-3 text-right">{bytes(i.txBytes)}</td>
                    <td className="py-1.5 px-3 text-right">{(i.rxDropped + i.txDropped).toLocaleString()}</td>
                    <td className="py-1.5 pl-3 text-right">{(i.rxErrors + i.txErrors).toLocaleString()}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {/* Stated rather than implied: Docker gives interface names, not the
              network each belongs to, and guessing would be worse than saying so. */}
          <p className="text-xs text-muted mt-1.5">
            Docker reports interface names only — it does not say which Docker network each one is attached to.
          </p>
        </div>
      )}
    </div>
  );
}

function Stat({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: "warn" | "danger" }) {
  return (
    <div>
      <div className="text-xs text-muted">{label}</div>
      <div className={tone === "danger" ? "text-danger font-semibold" : tone === "warn" ? "text-warn font-semibold" : "font-semibold"}>
        {value}
      </div>
      {sub && <div className="text-xs text-muted">{sub}</div>}
    </div>
  );
}
