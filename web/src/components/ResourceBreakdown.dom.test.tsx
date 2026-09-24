/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { ResourceBreakdown } from "./ResourceBreakdown";
import { api } from "../lib/api";
import type { ResourceOverview, ResourceUsage } from "../lib/types";

vi.mock("../lib/api", () => ({ api: { statsOverview: vi.fn() } }));

const GB = 1024 ** 3;
const u = (i: number): ResourceUsage => ({
  id: `c${i}`, name: `svc-${String(i).padStart(2, "0")}`, cpuPercent: 1, memBytes: i * GB, memPercent: (i * GB / (64 * GB)) * 100, netRxRate: 0, netTxRate: 0,
});
// 16 cores, 64 GB host; 12 containers at 1% CPU each (12% of the host = 1.92 cores).
const many: ResourceOverview = { cpus: 16, memTotal: 64 * GB, containers: Array.from({ length: 12 }, (_, i) => u(i % 8 + 1)).map((c, i) => ({ ...c, id: `c${i}`, name: `svc-${String(i).padStart(2, "0")}` })) };

let container: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.mocked(api.statsOverview).mockResolvedValue(many);
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => { root.render(<MemoryRouter><ResourceBreakdown /></MemoryRouter>); });
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

describe("ResourceBreakdown absolute figures", () => {
  it("puts the absolute totals in the CPU and memory titles, not only shares", () => {
    const text = container.textContent ?? "";
    expect(text).toContain("CPU · 1.92 of 16 cores");
    expect(text).toContain("of 64.0 GB");
  });

  it("caps the consumers table at 10 rows and says how many there are", () => {
    expect(container.querySelectorAll("tbody tr")).toHaveLength(10);
    expect(container.textContent).toContain("Top consumers · 10 of 12");
  });

  it("links to the full Resources page", () => {
    expect(container.querySelector('a[href="/resources"]')).not.toBeNull();
  });
});
