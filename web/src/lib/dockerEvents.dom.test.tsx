/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { isRelevantDockerEvent, useDockerEventTick } from "./dockerEvents";
import type { EventMsg } from "./types";

// The hook is what keeps Topology, Images, Volumes, Networks, Containers,
// Stacks, Projects and the dashboard in step with the daemon. What has to hold:
// one shared socket, a debounced bump per burst, and — the part that used to be
// missing — a reconnect (with a resync bump) after the server drops the stream.

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: (() => void) | null = null;
  closed = false;
  constructor(public url: string) {
    FakeWebSocket.instances.push(this);
  }
  open() {
    this.onopen?.();
  }
  close() {
    if (this.closed) return;
    this.closed = true;
    this.onclose?.();
  }
  emit(frame: unknown) {
    this.onmessage?.({ data: JSON.stringify(frame) });
  }
}

const open = () => FakeWebSocket.instances.filter((s) => !s.closed);
const latest = () => FakeWebSocket.instances[FakeWebSocket.instances.length - 1];

const ticks: Record<string, number> = {};
function Probe({ name }: { name: string }) {
  ticks[name] = useDockerEventTick();
  return null;
}

let container: HTMLDivElement;
let root: Root | undefined;

function mount(...names: string[]) {
  root = createRoot(container);
  act(() => root!.render(<>{names.map((n) => <Probe key={n} name={n} />)}</>));
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers();
  vi.stubGlobal("WebSocket", FakeWebSocket);
  FakeWebSocket.instances = [];
  for (const k of Object.keys(ticks)) delete ticks[k];
  container = document.createElement("div");
  document.body.appendChild(container);
});

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  container.remove();
  vi.runOnlyPendingTimers();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("isRelevantDockerEvent", () => {
  const ev = (type: string, action: string) => ({ type, action }) as EventMsg;

  it("counts lifecycle, image, volume and network changes", () => {
    for (const e of [ev("container", "start"), ev("container", "die"), ev("image", "pull"), ev("volume", "create"), ev("network", "connect")]) {
      expect(isRelevantDockerEvent(e)).toBe(true);
    }
  });

  it("ignores exec frames and other event types", () => {
    expect(isRelevantDockerEvent(ev("container", "exec_start: sh"))).toBe(false);
    expect(isRelevantDockerEvent(ev("container", "exec_die"))).toBe(false);
    expect(isRelevantDockerEvent(ev("plugin", "enable"))).toBe(false);
  });
});

describe("useDockerEventTick", () => {
  it("shares one socket between every view on the page", () => {
    mount("a", "b", "c");
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(latest().url).toContain("/api/events");
  });

  it("bumps every subscriber once per burst, after the debounce", () => {
    mount("a", "b");
    act(() => latest().open());
    act(() => {
      latest().emit({ type: "container", action: "start" });
      latest().emit({ type: "network", action: "connect" });
      latest().emit({ type: "container", action: "start" });
    });
    expect(ticks.a).toBe(0);
    act(() => vi.advanceTimersByTime(300));
    expect(ticks).toEqual({ a: 1, b: 1 });
  });

  it("does not bump on exec noise or a server error frame", () => {
    mount("a");
    act(() => latest().open());
    act(() => {
      latest().emit({ type: "container", action: "exec_start: sh" });
      latest().emit({ error: "docker unreachable" });
      latest().onmessage?.({ data: "not json" });
      vi.advanceTimersByTime(1000);
    });
    expect(ticks.a).toBe(0);
  });

  it("reconnects after the server drops the stream and resyncs the views", () => {
    mount("a");
    const first = latest();
    act(() => first.open());

    // Docker daemon (or DC) restarted: the server ends the stream.
    act(() => first.close());
    expect(open()).toHaveLength(0);

    act(() => vi.advanceTimersByTime(1000));
    expect(FakeWebSocket.instances).toHaveLength(2);
    const second = latest();

    // Events during the gap are lost — the reopen itself must refresh.
    act(() => second.open());
    act(() => vi.advanceTimersByTime(300));
    expect(ticks.a).toBe(1);

    // And the new socket delivers events as before.
    act(() => second.emit({ type: "container", action: "die" }));
    act(() => vi.advanceTimersByTime(300));
    expect(ticks.a).toBe(2);
  });

  it("backs off while the server stays unreachable", () => {
    mount("a");
    act(() => latest().close()); // never opened
    act(() => vi.advanceTimersByTime(1000));
    expect(FakeWebSocket.instances).toHaveLength(2);

    act(() => latest().close());
    act(() => vi.advanceTimersByTime(1000));
    expect(FakeWebSocket.instances).toHaveLength(2); // waits 2 s now
    act(() => vi.advanceTimersByTime(1000));
    expect(FakeWebSocket.instances).toHaveLength(3);
  });

  it("does not bump on the first open — the view loads on mount anyway", () => {
    mount("a");
    act(() => latest().open());
    act(() => vi.advanceTimersByTime(1000));
    expect(ticks.a).toBe(0);
  });

  it("closes the socket when the last view unmounts and does not reconnect", () => {
    mount("a");
    act(() => latest().open());
    act(() => root!.unmount());
    root = undefined;
    expect(open()).toHaveLength(0);
    act(() => vi.advanceTimersByTime(20000));
    expect(FakeWebSocket.instances).toHaveLength(1);
  });

  it("navigating between views reuses a fresh socket without a resync bump", () => {
    mount("a");
    act(() => latest().open());
    act(() => root!.unmount());
    mount("b");
    expect(FakeWebSocket.instances).toHaveLength(2);
    act(() => latest().open());
    act(() => vi.advanceTimersByTime(1000));
    expect(ticks.b).toBe(0);
  });
});
