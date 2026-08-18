// Small formatting helpers shared across views.

export function bytes(n: number): string {
  if (!n) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.floor(Math.log(n) / Math.log(1024));
  return `${(n / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

export function shortId(id: string): string {
  return id.replace(/^sha256:/, "").slice(0, 12);
}

export function relTime(unixSeconds: number): string {
  const diff = Date.now() / 1000 - unixSeconds;
  if (diff < 60) return "just now";
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return `${Math.floor(diff / 86400)}d ago`;
}

export function stateColor(state: string): string {
  switch (state) {
    case "running":
      return "text-ok";
    case "paused":
    case "partial":
      return "text-warn";
    case "exited":
    case "dead":
      return "text-danger";
    default:
      return "text-muted";
  }
}

// netRates turns a window of cumulative network counters into per-second rates.
//
// Docker reports totals since the container started, so a rate only exists as a
// difference between two samples. Two things make that less trivial than it
// looks:
//
//   - A counter RESET (the container was recreated, so it starts again from
//     zero) would otherwise render as a huge negative rate, or as a spike if the
//     sign were dropped. A decrease is treated as a restart and reported as no
//     traffic for that interval rather than an invented number.
//   - Samples do not arrive on an exact interval, so the elapsed time between
//     each pair is used rather than an assumed period.
export interface NetRate {
  t: number;
  rx: number; // bytes/s
  tx: number; // bytes/s
}

export function netRates(samples: { timestamp: number; netRx: number; netTx: number }[]): NetRate[] {
  const out: NetRate[] = [];
  for (let i = 1; i < samples.length; i++) {
    const prev = samples[i - 1];
    const cur = samples[i];
    const secs = (cur.timestamp - prev.timestamp) / 1000;
    if (secs <= 0) continue; // duplicate or out-of-order frame
    const dRx = cur.netRx - prev.netRx;
    const dTx = cur.netTx - prev.netTx;
    out.push({
      t: cur.timestamp,
      rx: dRx < 0 ? 0 : dRx / secs,
      tx: dTx < 0 ? 0 : dTx / secs,
    });
  }
  return out;
}

// rate renders a bytes-per-second figure.
export function rate(bytesPerSec: number): string {
  return `${bytes(bytesPerSec)}/s`;
}
