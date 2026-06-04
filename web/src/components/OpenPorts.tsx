import { useState } from "react";
import { Radar, Loader2, Lock } from "lucide-react";
import { api } from "../lib/api";
import type { HostPortProbe } from "../lib/types";

// OpenPorts is a host-wide map of published ports across all running
// containers, with active service detection. It only scans on demand (probing
// every port is an active network action, so it isn't run automatically).
export function OpenPorts() {
  const [rows, setRows] = useState<HostPortProbe[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const scan = async () => {
    setBusy(true);
    setErr("");
    try {
      setRows((await api.hostPorts()) ?? []);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "scan failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <div className="flex items-baseline justify-between mb-3">
        <h2 className="text-sm font-semibold text-muted">Open ports</h2>
        <button className="btn-ghost px-2 py-1 text-xs" onClick={scan} disabled={busy} title="Connect to every published port and detect the service">
          {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Radar className="h-3.5 w-3.5" />} {rows ? "Rescan" : "Scan"}
        </button>
      </div>

      {err && <p className="text-sm text-danger mb-2">{err}</p>}

      {rows === null ? (
        <p className="text-sm text-muted">Scan the host's published ports to see what's listening on each one.</p>
      ) : rows.length === 0 ? (
        <p className="text-sm text-muted">No published ports on this host.</p>
      ) : (
        <div className="card overflow-hidden">
          <table className="w-full text-sm">
            <thead className="text-xs uppercase tracking-wide text-muted bg-panel2">
              <tr>
                <th className="text-left font-medium px-3 py-2">Container</th>
                <th className="text-left font-medium px-3 py-2">Port</th>
                <th className="text-left font-medium px-3 py-2">Service</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r, i) => (
                <tr key={i} className="border-t border-border">
                  <td className="px-3 py-2 font-medium truncate max-w-[14rem]">{r.containerName}</td>
                  <td className="px-3 py-2 font-mono text-xs whitespace-nowrap">
                    {r.privatePort}/{r.type} → :{r.publicPort}
                  </td>
                  <td className="px-3 py-2">
                    <ServiceCell r={r} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function ServiceCell({ r }: { r: HostPortProbe }) {
  if (!r.open) {
    return <span className="text-xs rounded px-1.5 py-0.5 bg-danger/15 text-danger" title={r.error}>{r.error || "closed"}</span>;
  }
  return (
    <span className="inline-flex items-center gap-1.5 flex-wrap">
      <span className="text-xs rounded px-1.5 py-0.5 bg-ok/15 text-ok">{r.detected || "open · unknown"}</span>
      {r.tls && <Lock className="h-3 w-3 text-ok" />}
      {r.info && <span className="text-xs text-muted">{r.info}</span>}
      {r.guessByPort && r.guessByPort !== r.detected && <span className="text-[11px] text-muted">~ {r.guessByPort}</span>}
    </span>
  );
}
