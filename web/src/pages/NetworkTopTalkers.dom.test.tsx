/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { NetworkTopTalkers } from "./NetworkTopTalkers";
import { api } from "../lib/api";
import type { TopTalkers as TopTalkersResponse } from "../lib/types";

vi.mock("../lib/api", () => ({
  api: { topTalkers: vi.fn(), hosts: () => Promise.resolve([]) },
}));

const response: TopTalkersResponse = {
  window: "5m",
  metric: "total",
  containers: [
    { id: "c1", name: "busy-proxy", hostId: 0, hostName: "local", rxRate: 4_000_000, txRate: 1_000_000, rate: 5_000_000 },
  ],
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
        <NetworkTopTalkers />
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

describe("NetworkTopTalkers page", () => {
  it("loads the default 5-minute / total-metric ranking on mount", () => {
    expect(api.topTalkers).toHaveBeenCalledWith("5m", "total", 50);
  });

  it("renders the ranked table with received/sent columns", async () => {
    expect(container.textContent).toContain("busy-proxy");
    expect(container.querySelector("th:nth-of-type(4)")?.textContent).toContain("Received");
    expect(container.querySelector("th:nth-of-type(5)")?.textContent).toContain("Sent");
  });

  it("re-fetches with the new window when the window selector changes", async () => {
    vi.mocked(api.topTalkers).mockClear();
    await act(async () => setSelect(selectorFor("Last hour"), "1h"));
    expect(api.topTalkers).toHaveBeenCalledWith("1h", "total", 50);
  });

  it("re-fetches with the new metric when the metric selector changes", async () => {
    vi.mocked(api.topTalkers).mockClear();
    await act(async () => setSelect(selectorFor("Received"), "netrx"));
    expect(api.topTalkers).toHaveBeenCalledWith("5m", "netrx", 50);
  });

  it("shows an empty state instead of a blank table when nothing has enough history", async () => {
    vi.mocked(api.topTalkers).mockResolvedValue({ window: "5m", metric: "total", containers: [] });
    await act(async () => setSelect(selectorFor("Last hour"), "1h")); // force a re-fetch
    expect(container.textContent).toContain("Not enough history yet");
  });
});
