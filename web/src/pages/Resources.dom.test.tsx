/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { Resources } from "./Resources";
import { api } from "../lib/api";
import type { ResourceOverview, ResourceUsage } from "../lib/types";

vi.mock("../lib/api", () => ({ api: { statsOverview: vi.fn(), hosts: () => Promise.resolve([]) } }));

const GB = 1024 ** 3;
const u = (name: string, cpuPercent: number, memBytes: number): ResourceUsage => ({
  id: `id-${name}`, name, cpuPercent, memBytes, memPercent: (memBytes / (16 * GB)) * 100, netRxRate: 1000, netTxRate: 500,
});
// 8 cores, 16 GB host. db: 25% of host = 2 cores, 4 GB. web: 5% = 0.4 cores, 1 GB.
const overview: ResourceOverview = { cpus: 8, memTotal: 16 * GB, containers: [u("web", 5, GB), u("db", 25, 4 * GB)] };

let container: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.mocked(api.statsOverview).mockResolvedValue(overview);
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root.render(<MemoryRouter><Resources /></MemoryRouter>);
  });
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
  vi.useRealTimers();
});

const names = () => [...container.querySelectorAll("tbody tr td:first-child")].map((td) => td.textContent);

function typeInto(el: Element, value: string) {
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")!.set!;
  setter.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

describe("Resources page", () => {
  it("shows absolute cores and bytes, not just percentages", () => {
    const text = container.textContent ?? "";
    expect(text).toContain("2.00 cores"); // db: 25% of 8 cores
    expect(text).toContain("4.0 GB");
    expect(text).toContain("of 8"); // total cores in the KPI
    expect(text).toContain("5.0 GB"); // summed memory
  });

  it("defaults to memory descending", () => {
    expect(names()).toEqual(["db", "web"]);
  });

  it("re-sorts when a header is clicked, and flips direction on a second click", async () => {
    const header = (label: string) => [...container.querySelectorAll("th button")].find((b) => b.textContent?.trim() === label) as HTMLElement;
    await act(async () => header("CPU").click()); // new column → highest first
    expect(names()).toEqual(["db", "web"]);
    await act(async () => header("CPU").click());
    expect(names()).toEqual(["web", "db"]);
  });

  it("keeps equal rows in a stable (name) order regardless of direction", async () => {
    const rx = [...container.querySelectorAll("th button")].find((b) => b.textContent?.trim() === "Received") as HTMLElement;
    await act(async () => rx.click());
    expect(names()).toEqual(["db", "web"]); // both 1000 B/s
    await act(async () => rx.click());
    expect(names()).toEqual(["db", "web"]);
  });

  it("filters by name and shows a specific empty state for no match", async () => {
    await act(async () => typeInto(container.querySelector('input[type="search"]')!, "web"));
    expect(names()).toEqual(["web"]);
    await act(async () => typeInto(container.querySelector('input[type="search"]')!, "zzz"));
    expect(container.textContent).toContain("No container matches that name");
  });

  it("re-reads the snapshot every 5 seconds", async () => {
    act(() => root.unmount());
    vi.useFakeTimers();
    vi.mocked(api.statsOverview).mockClear();
    root = createRoot(container);
    await act(async () => { root.render(<MemoryRouter><Resources /></MemoryRouter>); });
    expect(api.statsOverview).toHaveBeenCalledTimes(1); // initial load
    await act(async () => { vi.advanceTimersByTime(5000); });
    expect(api.statsOverview).toHaveBeenCalledTimes(2);
  });
});
