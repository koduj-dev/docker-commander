// Multiplexed WebSocket client matching the Go hub protocol. One socket carries
// many subscriptions (stats/logs), each identified by a caller-chosen subId.

import { hostIdOrZero } from "./host";

type Frame =
  | { type: "stats"; subId: string; data: unknown }
  | { type: "log"; subId: string; data: unknown }
  | { type: "error"; subId: string; message: string }
  | { type: "end"; subId: string }
  | { type: "pong"; subId: string };

type Handler = (frame: Frame) => void;

export class LiveSocket {
  private ws: WebSocket | null = null;
  private handlers = new Map<string, Handler>();
  private pending: string[] = [];
  private reconnectTimer: number | null = null;
  private closedByUser = false;

  connect() {
    this.closedByUser = false;
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${proto}://${location.host}/api/ws`);
    this.ws = ws;

    ws.onopen = () => {
      // Flush any subscribe messages queued while disconnected.
      for (const msg of this.pending) ws.send(msg);
      this.pending = [];
    };
    ws.onmessage = (ev) => {
      const frame = JSON.parse(ev.data) as Frame;
      this.handlers.get(frame.subId)?.(frame);
    };
    ws.onclose = () => {
      this.ws = null;
      if (!this.closedByUser) this.scheduleReconnect();
    };
    ws.onerror = () => ws.close();
  }

  private scheduleReconnect() {
    if (this.reconnectTimer != null) return;
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = null;
      this.connect();
      // Re-subscribe everything that still has a handler.
      for (const subId of this.handlers.keys()) {
        const resend = this.resubscribe.get(subId);
        if (resend) this.send(resend);
      }
    }, 1500);
  }

  private resubscribe = new Map<string, object>();

  private send(obj: object) {
    const msg = JSON.stringify(obj);
    if (this.ws && this.ws.readyState === WebSocket.OPEN) this.ws.send(msg);
    else this.pending.push(msg);
  }

  subscribeStats(subId: string, containerId: string, onFrame: Handler) {
    this.handlers.set(subId, onFrame);
    const msg = { type: "subscribe", channel: "stats", subId, containerId, hostId: hostIdOrZero() };
    this.resubscribe.set(subId, msg);
    this.send(msg);
  }

  subscribeLogs(subId: string, containerId: string, tail: string, onFrame: Handler) {
    this.handlers.set(subId, onFrame);
    const msg = { type: "subscribe", channel: "logs", subId, containerId, tail, hostId: hostIdOrZero() };
    this.resubscribe.set(subId, msg);
    this.send(msg);
  }

  unsubscribe(subId: string) {
    this.handlers.delete(subId);
    this.resubscribe.delete(subId);
    this.send({ type: "unsubscribe", subId });
  }

  close() {
    this.closedByUser = true;
    if (this.reconnectTimer != null) window.clearTimeout(this.reconnectTimer);
    this.ws?.close();
    this.ws = null;
    this.handlers.clear();
    this.resubscribe.clear();
  }
}
