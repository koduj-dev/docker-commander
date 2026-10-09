/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { TopTalkers } from "./TopTalkers";
import { api } from "../lib/api";
import type { TopTalkers as TopTalkersResponse } from "../lib/types";

// Ranked over a stored window, never a live poll sample — see
// ResourceBreakdown's own comment on why a point-in-time ranking would be
// unreadable. This is the dashboard's small preview; the full table with a
// window/metric selector is NetworkTopTalkers.tsx.

vi.mock("../lib/api", () => ({
  api: { topTalkers: vi.fn() },
}));

const response: TopTalkersResponse = {
  window: "5m",
  metric: "total",
  containers: [
    { id: "c1", name: "busy-proxy", hostId: 0, hostName: "local", rxRate: 4_000_000, txRate: 1_000_000, rate: 5_000_000 },
    { id: "c2", name: "quiet-db", hostId: 0, hostName: "local", rxRate: 1000, txRate: 500, rate: 1500 },
  ],
  total: 2,
};

let container: HTMLDivElement;
let root: Root;

async function render() {
  await act(async () => {
    root.render(
      <MemoryRouter>
        <TopTalkers />
      </MemoryRouter>,
    );
  });
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

describe("TopTalkers widget", () => {
  it("renders ranked rows from the API, most active first", async () => {
    vi.mocked(api.topTalkers).mockResolvedValue(response);
    await render();
    const items = [...container.querySelectorAll("tbody tr")].map((tr) => tr.textContent ?? "");
    expect(items[0]).toContain("busy-proxy");
    expect(items[1]).toContain("quiet-db");
  });

  it("shows each row's rank next to its name", async () => {
    vi.mocked(api.topTalkers).mockResolvedValue(response);
    await render();
    const first = container.querySelectorAll("tbody tr")[0].querySelector("td")!.textContent ?? "";
    const second = container.querySelectorAll("tbody tr")[1].querySelector("td")!.textContent ?? "";
    expect(first).toMatch(/^1\s*busy-proxy/);
    expect(second).toMatch(/^2\s*quiet-db/);
  });

  it("uses the same table design as Top consumers: a header row and matching row padding", async () => {
    vi.mocked(api.topTalkers).mockResolvedValue(response);
    await render();
    expect([...container.querySelectorAll("thead th")].map((th) => th.textContent)).toEqual(["Container", "Received", "Sent"]);
    // Row height comes from these classes; ResourceTable uses the same ones, so
    // the two cards line up when they share a dashboard row.
    expect(container.querySelector("tbody td")!.className).toContain("py-2");
    expect(container.querySelector("tbody td")!.className).toContain("px-4");
  });

  it("asks for the 5-minute window by default, not a live snapshot", async () => {
    vi.mocked(api.topTalkers).mockResolvedValue(response);
    await render();
    expect(api.topTalkers).toHaveBeenCalledWith("5m", "total", expect.any(Number));
  });

  it("shows a 'view all' link to the full ranked page", async () => {
    vi.mocked(api.topTalkers).mockResolvedValue(response);
    await render();
    const link = container.querySelector("a");
    expect(link?.getAttribute("href")).toBe("/resources?tab=network");
  });

  it("says there isn't enough history yet rather than showing an empty table", async () => {
    vi.mocked(api.topTalkers).mockResolvedValue({ window: "5m", metric: "total", containers: [], total: 0 });
    await render();
    expect(container.textContent).toContain("Not enough history yet");
  });
});
