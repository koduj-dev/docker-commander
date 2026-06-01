// Active Docker host selection, shared across the app. Persisted so it sticks
// across reloads. null means "the default local host" (the server resolves it).

const KEY = "dc.host";

function read(): number | null {
  try {
    const raw = localStorage.getItem(KEY);
    return raw == null || raw === "null" ? null : Number(raw);
  } catch {
    return null;
  }
}

let hostId: number | null = read();

export function getHostId(): number | null {
  return hostId;
}

export function setHostId(id: number | null) {
  hostId = id;
  try {
    localStorage.setItem(KEY, id == null ? "null" : String(id));
  } catch {
    /* ignore */
  }
}

// hostParam returns "?host=<id>" / "&host=<id>" (or "") for appending to URLs.
export function hostParam(sep: "?" | "&" = "?"): string {
  return hostId == null ? "" : `${sep}host=${hostId}`;
}

// hostIdOrZero is used in WebSocket messages where 0 means "default local".
export function hostIdOrZero(): number {
  return hostId ?? 0;
}
