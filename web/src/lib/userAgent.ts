// Turning a user-agent string into something a person recognises.
//
// This exists for one screen: the list of sessions in your profile, where the
// question is "is that me?". Nothing here is a security decision — the string is
// whatever the client chose to send — so the parser is allowed to be a handful of
// substring checks. It must never *look* authoritative: an unrecognised agent is
// shown verbatim rather than guessed at, because a wrong-but-confident "Chrome on
// Windows" is worse for that question than "unrecognised client".

export type ClientKind = "desktop" | "mobile" | "tablet" | "tool" | "unknown";

export interface ClientDescription {
  /** Short label for the row: "Firefox on Linux". */
  label: string;
  /** Which icon to draw. */
  kind: ClientKind;
  /** The raw string, for the title attribute — the parse is a summary, not a replacement. */
  raw: string;
}

// Order matters: Edge and Opera both claim to be Chrome, Chrome claims Safari,
// so the more specific brand has to win.
const BROWSERS: Array<[RegExp, string]> = [
  [/\bEdg(?:e|A|iOS)?\//, "Edge"],
  [/\bOPR\/|\bOpera\b/, "Opera"],
  [/\bVivaldi\//, "Vivaldi"],
  [/\bBrave\//, "Brave"],
  [/\bFirefox\/|\bFxiOS\//, "Firefox"],
  // Headless Chrome brands itself HeadlessChrome/ and drops the Chrome/ token, so
  // without this it falls through to Safari — which is what every Chromium agent
  // claims. Naming it is also useful: a headless browser in your own session list
  // is worth noticing.
  [/\bHeadlessChrome\//, "Headless Chrome"],
  [/\bChrome\/|\bCriOS\//, "Chrome"],
  [/\bSafari\//, "Safari"],
];

const SYSTEMS: Array<[RegExp, string]> = [
  [/\bAndroid\b/, "Android"],
  [/\biPhone\b/, "iPhone"],
  [/\biPad\b/, "iPad"],
  [/\bWindows NT\b/, "Windows"],
  [/\bMac OS X\b|\bMacintosh\b/, "macOS"],
  [/\bCrOS\b/, "ChromeOS"],
  [/\bLinux\b|\bX11\b/, "Linux"],
];

// Non-browser clients are worth naming rather than parsing: seeing "curl" in your
// own session list is exactly the kind of thing this screen exists to surface.
const TOOLS: Array<[RegExp, string]> = [
  [/^curl\//i, "curl"],
  [/^Wget\//i, "wget"],
  [/\bHTTPie\b/i, "HTTPie"],
  [/\bpython-requests\b/i, "python-requests"],
  [/\bGo-http-client\b/i, "Go HTTP client"],
  [/\bPostmanRuntime\b/i, "Postman"],
  [/\bdocker-commander\b/i, "Docker Commander CLI"],
];

export function describeClient(ua: string): ClientDescription {
  const raw = (ua ?? "").trim();
  if (!raw) return { label: "Unknown client", kind: "unknown", raw: "" };

  for (const [re, name] of TOOLS) {
    if (re.test(raw)) return { label: name, kind: "tool", raw };
  }

  const browser = BROWSERS.find(([re]) => re.test(raw))?.[1];
  const os = SYSTEMS.find(([re]) => re.test(raw))?.[1];

  // Neither half recognised: show what the client actually said. A guess here
  // would be a label that looks certain and is wrong.
  if (!browser && !os) {
    return { label: raw.length > 60 ? `${raw.slice(0, 60)}…` : raw, kind: "unknown", raw };
  }

  const label = browser && os ? `${browser} on ${os}` : (browser ?? os) as string;
  return { label, kind: deviceKind(raw, os), raw };
}

function deviceKind(raw: string, os?: string): ClientKind {
  if (os === "iPad" || (os === "Android" && !/\bMobile\b/.test(raw))) return "tablet";
  if (os === "iPhone" || os === "Android" || /\bMobile\b/.test(raw)) return "mobile";
  if (!os) return "unknown";
  return "desktop";
}

// sinceLabel renders a timestamp the way this screen needs it: recent times as
// "3 minutes ago" (which is what tells you whether a session is live right now),
// older ones as a date (where the exact minute stopped mattering).
export function sinceLabel(iso: string, now: number = Date.now()): string {
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return "unknown";
  const secs = Math.round((now - t) / 1000);
  if (secs < 0) return "just now"; // clock skew; "in 3 minutes" would just look broken
  if (secs < 90) return "just now";
  const mins = Math.round(secs / 60);
  if (mins < 60) return `${mins} minutes ago`;
  const hours = Math.round(mins / 60);
  if (hours < 24) return hours === 1 ? "an hour ago" : `${hours} hours ago`;
  const days = Math.round(hours / 24);
  if (days < 7) return days === 1 ? "yesterday" : `${days} days ago`;
  return new Date(t).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });
}
