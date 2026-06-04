import { useEffect, useState } from "react";
import { hostParam } from "./host";
import type { EventMsg } from "./types";

// useDockerEventTick subscribes to the Docker events stream (the WS we already
// have) and returns a counter that bumps shortly after a relevant lifecycle
// event — container start/stop/die, image/volume/network changes — so views can
// refresh near-instantly without fast polling. Bursts (e.g. `compose up`)
// coalesce into a single bump.
export function useDockerEventTick(): number {
  const [tick, setTick] = useState(0);

  useEffect(() => {
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${proto}://${location.host}/api/events${hostParam("?")}`);
    let timer: ReturnType<typeof setTimeout> | undefined;

    ws.onmessage = (ev) => {
      let e: EventMsg;
      try {
        e = JSON.parse(ev.data as string) as EventMsg;
      } catch {
        return;
      }
      if ((e as { error?: string }).error) return;

      // Exec frames (terminal/probe) are noise; everything else that changes
      // what the dashboard shows counts.
      const relevant =
        (e.type === "container" && !(e.action ?? "").startsWith("exec_")) ||
        e.type === "image" ||
        e.type === "volume" ||
        e.type === "network";
      if (!relevant) return;

      clearTimeout(timer);
      timer = setTimeout(() => setTick((t) => t + 1), 250);
    };

    return () => {
      clearTimeout(timer);
      ws.close();
    };
  }, []);

  return tick;
}
