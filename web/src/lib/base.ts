// The URL path prefix the UI is served under, e.g. "/dockercmd" behind
// `location /dockercmd/ { proxy_pass … }` on a shared host; "" at the root.
// The server writes it into index.html as <base href>, from X-Forwarded-Prefix
// or DC_BASE_PATH. The Vite dev server writes none, so dev runs at the root.
export function readBasePath(doc: Document | undefined = globalThis.document): string {
  const href = doc?.querySelector("base")?.getAttribute("href") ?? "";
  // Only a plain absolute path is a prefix; anything else is not ours to follow.
  if (!/^\/(?!\/)/.test(href)) return "";
  return href.replace(/\/+$/, "");
}

export const BASE_PATH = readBasePath();

// withBase puts the prefix in front of a root-relative path ("/api/…").
export function withBase(path: string, base: string = BASE_PATH): string {
  return path.startsWith("/") ? base + path : path;
}

// wsUrl is the WebSocket URL for a root-relative path on this server.
export function wsUrl(path: string, base: string = BASE_PATH): string {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  return `${proto}://${location.host}${withBase(path, base)}`;
}
