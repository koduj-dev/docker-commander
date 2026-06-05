import { useEffect, useState } from "react";
import { Radar, Loader2, Lock } from "lucide-react";
import { api } from "../lib/api";
import { getHostId } from "../lib/host";
import type { HostPortProbe } from "../lib/types";

type Cached = { rows: HostPortProbe[]; at: number };

// Scan results are cached per host (published ports rarely change between
// restarts), so revisiting the dashboard shows the last scan instead of
// re-probing every time. Persisted in localStorage so it survives reloads.
function cacheKey(): string {
  return `dc.portscan.${getHostId() ?? "local"}`;
}
function readCache(): Cached | null {
  try {
    const raw = localStorage.getItem(cacheKey());
    return raw ? (JSON.parse(raw) as Cached) : null;
  } catch {
    return null;
  }
}
function writeCache(rows: HostPortProbe[]) {
  try {
    localStorage.setItem(cacheKey(), JSON.stringify({ rows, at: Date.now() }));
  } catch {
    /* quota / private mode — ignore */
  }
}

// OpenPorts is a host-wide map of published ports across all running
// containers, with active service detection. It scans on demand (probing every
// port is an active network action) and remembers the last scan per host. The
// cached scan is filtered to the containers still running (it `tick`s with the
// dashboard's Docker events) so a stopped container's stale ports drop out.
export function OpenPorts({ tick = 0 }: { tick?: number }) {
  const [rows, setRows] = useState<HostPortProbe[] | null>(null);
  const [scannedAt, setScannedAt] = useState<number | null>(null);
  const [running, setRunning] = useState<Set<string> | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    const c = readCache();
    if (c) {
      setRows(c.rows);
      setScannedAt(c.at);
    }
  }, []);

  // Track which containers are currently running; refresh on lifecycle events.
  useEffect(() => {
    api
      .containers()
      .then((cs) => setRunning(new Set(cs.filter((c) => c.state === "running").map((c) => c.id))))
      .catch(() => {});
  }, [tick]);

  const scan = async () => {
    setBusy(true);
    setErr("");
    try {
      const r = (await api.hostPorts()) ?? [];
      setRows(r);
      setScannedAt(Date.now());
      writeCache(r);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "scan failed");
    } finally {
      setBusy(false);
    }
  };

  // Only show ports of containers that are still running (the scan is cached, so
  // a container stopped since the scan would otherwise linger).
  const visible = rows && running ? rows.filter((r) => running.has(r.containerId)) : rows ?? [];

  return (
    <div>
      <div className="flex items-center gap-3 mb-3">
        <h2 className="text-sm font-semibold text-muted">Open ports</h2>
        <button className="btn-ghost px-2 py-1 text-xs" onClick={scan} disabled={busy} title="Connect to every published port and detect the service">
          {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Radar className="h-3.5 w-3.5" />} {rows ? "Rescan" : "Scan"}
        </button>
        {scannedAt && <span className="text-xs text-muted">scanned {new Date(scannedAt).toLocaleTimeString()}</span>}
      </div>

      {err && <p className="text-sm text-danger mb-2">{err}</p>}

      {rows === null ? (
        <p className="text-sm text-muted">Scan the host's published ports to see what's listening on each one.</p>
      ) : visible.length === 0 ? (
        <p className="text-sm text-muted">No published ports on running containers — rescan after changes.</p>
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
              {visible.map((r, i) => (
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
