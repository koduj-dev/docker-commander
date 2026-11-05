import { useEffect, useState } from "react";
import { hostParam } from "./host";
import type { EventMsg } from "./types";
import { wsUrl } from "./base";

// useDockerEventTick subscribes to the Docker events stream and returns a
// counter that bumps shortly after a relevant lifecycle event — container
// start/stop/die, image/volume/network changes — so views can refresh
// near-instantly without fast polling. Bursts (e.g. `compose up`) coalesce into
// a single bump.
//
// All views share one socket. It reconnects whenever it drops: the server ends
// the stream when the Docker daemon restarts or DC itself does, and before this
// was shared every view silently stopped updating until a full page reload. A
// reconnect also bumps the tick, because events that happened while the socket
// was down are gone and the view has to re-read the truth.

const DEBOUNCE_MS = 250;
const RECONNECT_MIN_MS = 1000;
const RECONNECT_MAX_MS = 15000;

/** isRelevantDockerEvent reports whether an event changes what views show. */
export function isRelevantDockerEvent(e: EventMsg): boolean {
  // Exec frames (terminal/probe/healthcheck) are noise.
  return (
    (e.type === "container" && !(e.action ?? "").startsWith("exec_")) ||
    e.type === "image" ||
    e.type === "volume" ||
    e.type === "network"
  );
}

type Listener = () => void;

const listeners = new Set<Listener>();
let ws: WebSocket | null = null;
let debounce: ReturnType<typeof setTimeout> | undefined;
let reconnect: ReturnType<typeof setTimeout> | undefined;
let delay = RECONNECT_MIN_MS;
// Whether a socket of the current subscription period has opened before; the
// next open is then a reconnect after a gap and has to resync the views.
let opened = false;

function notify() {
  clearTimeout(debounce);
  debounce = setTimeout(() => {
    for (const l of listeners) l();
  }, DEBOUNCE_MS);
}

function connect() {
  const sock = new WebSocket(wsUrl(`/api/events${hostParam("?")}`));
  ws = sock;

  sock.onopen = () => {
    if (ws !== sock) return;
    delay = RECONNECT_MIN_MS;
    if (opened) notify();
    opened = true;
  };
  sock.onmessage = (ev) => {
    if (ws !== sock) return;
    let e: EventMsg;
    try {
      e = JSON.parse(ev.data as string) as EventMsg;
    } catch {
      return;
    }
    if ((e as { error?: string }).error) return;
    if (isRelevantDockerEvent(e)) notify();
  };
  sock.onerror = () => sock.close();
  sock.onclose = () => {
    // A socket we already replaced or tore down on purpose is not a drop.
    if (ws !== sock) return;
    ws = null;
    if (listeners.size === 0 || reconnect) return;
    reconnect = setTimeout(() => {
      reconnect = undefined;
      if (listeners.size > 0 && !ws) connect();
    }, delay);
    delay = Math.min(delay * 2, RECONNECT_MAX_MS);
  };
}

function subscribe(fn: Listener): () => void {
  listeners.add(fn);
  if (!ws && !reconnect) connect();
  return () => {
    listeners.delete(fn);
    if (listeners.size > 0) return;
    clearTimeout(debounce);
    clearTimeout(reconnect);
    reconnect = undefined;
    delay = RECONNECT_MIN_MS;
    opened = false;
    const sock = ws;
    ws = null;
    sock?.close();
  };
}

export function useDockerEventTick(): number {
  const [tick, setTick] = useState(0);
  useEffect(() => subscribe(() => setTick((t) => t + 1)), []);
  return tick;
}
