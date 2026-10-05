import type { HostPortProbe } from "./types";

// The last port scan per account and host, kept in localStorage so the
// dashboard shows it after a reload instead of re-probing.
//
// Per ACCOUNT, not per browser: a scan is a map of what listens on a host, and
// it used to be keyed by host alone, so the next person to sign in on the same
// browser saw it — a read-only account included, which may not scan at all.
// Logout clears every scan (clearUserState); the account in the key covers a
// session that simply expired.

export type CachedScan = { rows: HostPortProbe[]; at: number };

const PREFIX = "dc.portscan.";

function key(userId: number, hostId: number | null): string {
  return `${PREFIX}u${userId}.${hostId ?? "local"}`;
}

export function readScan(userId: number, hostId: number | null): CachedScan | null {
  try {
    const raw = localStorage.getItem(key(userId, hostId));
    return raw ? (JSON.parse(raw) as CachedScan) : null;
  } catch {
    return null;
  }
}

export function writeScan(userId: number, hostId: number | null, rows: HostPortProbe[]): void {
  try {
    localStorage.setItem(key(userId, hostId), JSON.stringify({ rows, at: Date.now() }));
  } catch {
    /* quota / private mode — ignore */
  }
}

// clearScans removes every stored scan, for every account and host, including
// ones written in the old per-host-only format.
export function clearScans(): void {
  try {
    const doomed: string[] = [];
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i);
      if (k?.startsWith(PREFIX)) doomed.push(k);
    }
    doomed.forEach((k) => localStorage.removeItem(k));
  } catch {
    /* storage unavailable — nothing to clear */
  }
}
