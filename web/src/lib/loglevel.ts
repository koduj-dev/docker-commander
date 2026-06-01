// Lightweight client-side log level detection. Covers the common cases across
// app logs (level keywords) and access logs (HTTP status codes). The backend
// alert engine has its own matcher; this is purely for display/filtering.

export type Level = "error" | "warn" | "info" | "debug" | "other";

const RE_ERROR = /\b(error|err|fatal|panic|exception|fail(ed|ure)?|critical|emerg)\b/i;
const RE_WARN = /\b(warn(ing)?|deprecat)/i;
const RE_INFO = /\b(info|notice)\b/i;
const RE_DEBUG = /\b(debug|trace|verbose)\b/i;
// HTTP status in common access-log position: `" 500 ` / `" 404 `.
const RE_HTTP = /"\s*[A-Z]+ [^"]*"\s+(\d{3})\b|\s(\d{3})\s/;

export function detectLevel(msg: string): Level {
  const m = msg.match(RE_HTTP);
  if (m) {
    const code = Number(m[1] ?? m[2]);
    if (code >= 500) return "error";
    if (code >= 400) return "warn";
  }
  if (RE_ERROR.test(msg)) return "error";
  if (RE_WARN.test(msg)) return "warn";
  if (RE_DEBUG.test(msg)) return "debug";
  if (RE_INFO.test(msg)) return "info";
  return "other";
}

export const levelClass: Record<Level, string> = {
  error: "text-danger",
  warn: "text-warn",
  info: "text-accent",
  debug: "text-muted",
  other: "text-text/80",
};

export const levelBadge: Record<Level, string> = {
  error: "bg-danger/15 text-danger",
  warn: "bg-warn/15 text-warn",
  info: "bg-accent/15 text-accent",
  debug: "bg-panel2 text-muted",
  other: "bg-panel2 text-muted",
};

// A small categorical palette to color-code log sources by container.
export const sourcePalette = [
  "#2496ed",
  "#2dd4a7",
  "#f5b14c",
  "#c084fc",
  "#f0616d",
  "#38bdf8",
  "#a3e635",
  "#fb923c",
];
