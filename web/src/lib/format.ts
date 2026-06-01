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
      return "text-warn";
    case "exited":
    case "dead":
      return "text-danger";
    default:
      return "text-muted";
  }
}
