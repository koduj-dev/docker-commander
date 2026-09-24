/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { StacksTab, stackRows } from "./StacksTab";
import { api } from "../../lib/api";
import type { ResourceOverview, ResourceUsage, Stack } from "../../lib/types";

vi.mock("../../lib/api", () => ({ api: { stacks: vi.fn(), savePrefs: () => Promise.resolve() } }));

const GB = 1024 ** 3;
const u = (id: string, name: string, cpuPercent: number, memBytes: number): ResourceUsage => ({
  id, name, cpuPercent, memBytes, memPercent: (memBytes / (16 * GB)) * 100, netRxRate: 100, netTxRate: 50,
});
const ct = (id: string, state = "running") => ({ id, name: id, service: id, state, status: "", image: "x" });
const stacks: Stack[] = [
  { project: "shop", running: 2, containers: [ct("a"), ct("b")] },
  { project: "blog", running: 1, containers: [ct("c"), ct("d", "exited")] },
  { project: "old", running: 0, containers: [ct("e", "exited")] },
];
const overview: ResourceOverview = {
  cpus: 8, memTotal: 16 * GB,
  containers: [u("a", "shop-web", 10, 2 * GB), u("b", "shop-db", 5, 4 * GB), u("c", "blog-web", 1, GB)],
};

describe("stackRows", () => {
  it("sums the members' usage per stack and ignores containers without a sample", () => {
    const rows = stackRows(stacks, overview.containers);
    const shop = rows.find((r) => r.name === "shop")!;
    expect(shop.memBytes).toBe(6 * GB);
    expect(shop.cpuPercent).toBe(15);
    expect(shop.netRxRate).toBe(200);
    expect(shop.members.map((m) => m.name)).toEqual(["shop-db", "shop-web"]); // biggest memory first
    const blog = rows.find((r) => r.name === "blog")!;
    expect(blog.memBytes).toBe(GB); // the exited container has no sample
    expect(blog.running).toBe(1);
    expect(blog.total).toBe(2);
    expect(rows.find((r) => r.name === "old")!.memBytes).toBe(0);
  });
});

let container: HTMLDivElement;
let root: Root;
const names = () => [...container.querySelectorAll("tbody tr td:first-child")].map((td) => td.textContent?.trim());

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.mocked(api.stacks).mockResolvedValue(stacks);
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => { root.render(<MemoryRouter><StacksTab data={overview} /></MemoryRouter>); });
});
afterEach(() => { act(() => root.unmount()); container.remove(); vi.clearAllMocks(); });

describe("StacksTab", () => {
  it("shows running stacks by default, biggest memory first, without the stopped one", () => {
    expect(names()).toEqual(["shop", "blog"]);
    expect(container.textContent).toContain("6.0 GB");
    expect(container.textContent).toContain("2/2");
  });

  it("expands a stack into its containers on click", async () => {
    expect(container.textContent).not.toContain("shop-db");
    await act(async () => (container.querySelector("tbody tr") as HTMLElement).click());
    expect(container.textContent).toContain("shop-db");
    expect(container.textContent).toContain("4.0 GB");
  });
});
