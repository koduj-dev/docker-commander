import { LiveSocket } from "./ws";

// A single shared WebSocket for the whole app. Components subscribe/unsubscribe
// by a unique subId; the socket reconnects automatically.
export const live = new LiveSocket();

let started = false;
export function ensureLive() {
  if (!started) {
    started = true;
    live.connect();
  }
}
