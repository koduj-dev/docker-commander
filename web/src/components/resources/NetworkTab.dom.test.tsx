/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { NetworkTab } from "./NetworkTab";
import { api } from "../../lib/api";
import type { TopTalkers as TopTalkersResponse } from "../../lib/types";

vi.mock("../../lib/api", () => ({
  api: { topTalkers: vi.fn(), hosts: () => Promise.resolve([]) },
}));

const response: TopTalkersResponse = {
  window: "5m",
  metric: "total",
  containers: [
    { id: "c1", name: "busy-proxy", hostId: 0, hostName: "local", rxRate: 4_000_000, txRate: 1_000_000, rate: 5_000_000 },
  ],
  total: 1,
};

let container: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.mocked(api.topTalkers).mockResolvedValue(response);
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root.render(
      <MemoryRouter>
        <NetworkTab />
      </MemoryRouter>,
    );
  });
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

function selectorFor(labelSubstring: string): HTMLSelectElement {
  const select = [...container.querySelectorAll("select")].find((s) =>
    [...(s as HTMLSelectElement).options].some((o) => o.textContent?.includes(labelSubstring)),
  );
  if (!select) throw new Error(`no <select> found with an option containing ${JSON.stringify(labelSubstring)}`);
  return select as HTMLSelectElement;
}

function setSelect(select: HTMLSelectElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, "value")!.set!;
  setter.call(select, value);
  select.dispatchEvent(new Event("change", { bubbles: true }));
}

function typeInto(el: Element, value: string) {
  const input = el as HTMLInputElement;
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")?.set;
  setter?.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

function searchInput(): HTMLInputElement {
  return container.querySelector('input[type="search"]') as HTMLInputElement;
}

describe("NetworkTab page", () => {
  it("loads the default 5-minute / total-metric ranking on mount", () => {
    expect(api.topTalkers).toHaveBeenCalledWith("5m", "total", 50, undefined);
  });

  it("renders the ranked table with received/sent columns", async () => {
    expect(container.textContent).toContain("busy-proxy");
    expect(container.querySelector("th:nth-of-type(4)")?.textContent).toContain("Received");
    expect(container.querySelector("th:nth-of-type(5)")?.textContent).toContain("Sent");
  });

  it("re-fetches with the new window when the window selector changes", async () => {
    vi.mocked(api.topTalkers).mockClear();
    await act(async () => setSelect(selectorFor("Last hour"), "1h"));
    expect(api.topTalkers).toHaveBeenCalledWith("1h", "total", 50, undefined);
  });

  it("re-fetches with the new metric when the metric selector changes", async () => {
    vi.mocked(api.topTalkers).mockClear();
    await act(async () => setSelect(selectorFor("Received"), "netrx"));
    expect(api.topTalkers).toHaveBeenCalledWith("5m", "netrx", 50, undefined);
  });

  it("debounces the name filter, then re-fetches with q set", async () => {
    vi.useFakeTimers();
    try {
      vi.mocked(api.topTalkers).mockClear();
      typeInto(searchInput(), "nginx");
      // Not yet — still inside the 350ms debounce window.
      expect(api.topTalkers).not.toHaveBeenCalled();
      await act(async () => { vi.advanceTimersByTime(350); });
      expect(api.topTalkers).toHaveBeenCalledWith("5m", "total", 50, "nginx");
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows a 'showing N of total' hint only when the result was actually truncated", async () => {
    vi.mocked(api.topTalkers).mockResolvedValue({ ...response, total: 200 });
    await act(async () => setSelect(selectorFor("Last hour"), "1h")); // force a re-fetch
    expect(container.textContent).toContain("Showing 1 of 200");
  });

  it("shows no 'showing N of total' hint when nothing was truncated", () => {
    // The default mount fixture already has total === containers.length.
    expect(container.textContent).not.toContain("Showing");
  });

  it("ignores a stale response that resolves after a newer selector change", async () => {
    // Regression for a race the PR's own review caught: an in-flight
    // request for a PREVIOUS window can resolve after a newer one already
    // landed, and must not be allowed to overwrite it.
    let resolveStale!: (v: TopTalkersResponse) => void;
    let resolveFresh!: (v: TopTalkersResponse) => void;
    vi.mocked(api.topTalkers)
      .mockImplementationOnce(() => new Promise((res) => { resolveStale = res; }))
      .mockImplementationOnce(() => new Promise((res) => { resolveFresh = res; }));

    // First selector change: an in-flight, not-yet-resolved request for "1h".
    await act(async () => setSelect(selectorFor("Last hour"), "1h"));
    // Second selector change before the first resolves: its effect cleanup
    // must mark the "1h" request stale.
    await act(async () => setSelect(selectorFor("Last 15"), "15m"));

    // Resolve the NEWER ("15m") request first, then the stale ("1h") one.
    await act(async () => {
      resolveFresh({ window: "15m", metric: "total", containers: [{ id: "new", name: "fresh-container", hostId: 0, hostName: "local", rxRate: 2, txRate: 2, rate: 2 }], total: 1 });
    });
    await act(async () => {
      resolveStale({ window: "1h", metric: "total", containers: [{ id: "old", name: "stale-container", hostId: 0, hostName: "local", rxRate: 1, txRate: 1, rate: 1 }], total: 1 });
    });

    expect(container.textContent).toContain("fresh-container");
    expect(container.textContent).not.toContain("stale-container");
  });

  it("shows an empty state instead of a blank table when nothing has enough history", async () => {
    vi.mocked(api.topTalkers).mockResolvedValue({ window: "5m", metric: "total", containers: [], total: 0 });
    await act(async () => setSelect(selectorFor("Last hour"), "1h")); // force a re-fetch
    expect(container.textContent).toContain("Not enough history yet");
  });

  it("shows a name-specific empty state when a filter matches nothing, not the generic one", async () => {
    vi.useFakeTimers();
    try {
      vi.mocked(api.topTalkers).mockResolvedValue({ window: "5m", metric: "total", containers: [], total: 0 });
      typeInto(searchInput(), "does-not-exist");
      await act(async () => { vi.advanceTimersByTime(350); });
      expect(container.textContent).toContain("No container matches that name");
      expect(container.textContent).not.toContain("Not enough history yet");
    } finally {
      vi.useRealTimers();
    }
  });
});
