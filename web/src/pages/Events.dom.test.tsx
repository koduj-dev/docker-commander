/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { Events } from "./Events";

// A live feed that has stopped being live must say so.
//
// The socket had no reconnect and the badge reflected only the pause toggle, so a
// dropped connection — a server restart, a proxy idle timeout, a laptop waking up
// — left a pulsing green "Live" over a list that would never move again. That is
// the worst shape for this bug: an empty feed reads as "nothing is happening", so
// nobody investigates.

vi.mock("../lib/api", () => ({
  api: {
    hosts: () => Promise.resolve([]),
    version: () => Promise.resolve({ version: "test" }),
  },
}));

// A WebSocket stand-in the test drives by hand.
class FakeSocket {
  static instances: FakeSocket[] = [];
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  closed = false;

  constructor(public url: string) {
    FakeSocket.instances.push(this);
  }
  close() {
    this.closed = true;
    this.onclose?.();
  }
  /** The server going away, as opposed to the page navigating off. */
  drop() {
    this.onclose?.();
  }
}

let container: HTMLDivElement;
let root: Root | undefined;

const badge = () => container.querySelector("button")?.textContent ?? "";

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  FakeSocket.instances = [];
  vi.stubGlobal("WebSocket", FakeSocket as unknown as typeof WebSocket);
  vi.useFakeTimers();
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(<MemoryRouter><Events /></MemoryRouter>);
  });
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container.remove();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("the events feed", () => {
  it("does not claim to be live before the socket opens", () => {
    expect(badge()).not.toContain("Live");
  });

  it("says Live once connected", async () => {
    await act(async () => FakeSocket.instances[0].onopen?.());
    expect(badge()).toContain("Live");
  });

  it("stops claiming to be live when the connection drops", async () => {
    await act(async () => FakeSocket.instances[0].onopen?.());
    expect(badge()).toContain("Live");

    await act(async () => FakeSocket.instances[0].drop());
    expect(badge()).not.toContain("Live");
    expect(badge()).toContain("Reconnecting");
  });

  it("reconnects after a drop, and says Live again", async () => {
    await act(async () => FakeSocket.instances[0].onopen?.());
    await act(async () => FakeSocket.instances[0].drop());
    expect(FakeSocket.instances).toHaveLength(1);

    await act(async () => {
      vi.advanceTimersByTime(2000);
    });
    expect(FakeSocket.instances).toHaveLength(2);

    await act(async () => FakeSocket.instances[1].onopen?.());
    expect(badge()).toContain("Live");
  });

  it("does not reconnect after the page navigates away", async () => {
    await act(async () => FakeSocket.instances[0].onopen?.());
    await act(async () => root!.unmount());
    root = undefined;

    await act(async () => {
      vi.advanceTimersByTime(5000);
    });
    // Leaving the page must not leave a socket reopening forever behind it.
    expect(FakeSocket.instances).toHaveLength(1);
  });
});
