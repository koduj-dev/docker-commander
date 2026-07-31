import { useEffect, useState } from "react";
import { api } from "./api";
import type { AlertEvent } from "./types";

// A single app-wide poll of the alert feed.
//
// There used to be two: the Shell polled every 8s for the unread badge and
// raised toasts, while the Alerts page polled every 5s for its table. Because
// they were independent, an alert appeared in the list and the toast for it
// arrived up to eight seconds later — the same event announced twice, out of
// order with itself. One poller removes the skew by construction rather than by
// tuning two intervals to match, and halves the requests.
//
// Subscribers get: the newest events (for toasts), the unread count (for the
// badge), and a tick that bumps on every poll so a filtered view can refresh
// itself in step.

const INTERVAL_MS = 5000;
const PEEK = 5; // newest few, enough to toast a burst without fetching a page

export interface AlertPulse {
  /** Bumps on every completed poll — views refresh on this. */
  tick: number;
  /** Unacknowledged count within the viewer's host scope. */
  unread: number;
  /** Events that arrived since the previous poll, oldest first. Empty on the first. */
  fresh: AlertEvent[];
}

type Listener = (p: AlertPulse) => void;

let listeners: Listener[] = [];
let timer: ReturnType<typeof setInterval> | undefined;
let lastSeenId: number | null = null;
let state: AlertPulse = { tick: 0, unread: 0, fresh: [] };

async function poll(): Promise<void> {
  let r: Awaited<ReturnType<typeof api.alerts>>;
  try {
    r = await api.alerts({ limit: PEEK });
  } catch {
    return; // a failed poll is not news; the next one will tell the truth
  }
  const newest = r.events[0]?.id ?? 0;
  const baseline = lastSeenId;
  lastSeenId = newest;

  // The first poll only establishes a baseline. Without this, opening the app
  // would announce every alert already sitting in the feed.
  const fresh = baseline === null || newest <= baseline ? [] : r.events.filter((e) => e.id > baseline).reverse();

  state = { tick: state.tick + 1, unread: r.unread, fresh };
  for (const l of listeners) l(state);
}

function subscribe(fn: Listener): () => void {
  listeners.push(fn);
  if (!timer) {
    void poll();
    timer = setInterval(() => void poll(), INTERVAL_MS);
  }
  return () => {
    listeners = listeners.filter((l) => l !== fn);
    if (listeners.length === 0 && timer) {
      clearInterval(timer);
      timer = undefined;
      // Keep lastSeenId: navigating away and back should not re-announce
      // everything, but a fresh login should. resetAlertStream handles that.
    }
  };
}

/** Forget the baseline and counts — call on logout so the next user starts clean. */
export function resetAlertStream(): void {
  lastSeenId = null;
  state = { tick: 0, unread: 0, fresh: [] };
}

export function useAlertPulse(): AlertPulse {
  const [p, setP] = useState<AlertPulse>(state);
  useEffect(() => subscribe(setP), []);
  return p;
}
