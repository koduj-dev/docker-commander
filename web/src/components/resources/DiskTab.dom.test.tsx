/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { DiskTab, sizeLabel } from "./DiskTab";
import { api } from "../../lib/api";
import type { DiskReport } from "../../lib/types";

vi.mock("../../lib/api", () => ({ api: { diskReport: vi.fn(), savePrefs: () => Promise.resolve() } }));

const GB = 1024 ** 3;
const report: DiskReport = {
  generatedAt: 1_700_000_000,
  images: [
    { id: "sha256:aaaaaaaaaaaaaaaa", tags: ["big:1"], size: 3 * GB, unique: 2 * GB, containers: 0 },
    { id: "sha256:bbbbbbbbbbbbbbbb", tags: null, size: GB, unique: -1, containers: 2 },
  ],
  containers: [{ id: "c1", name: "web", project: "shop", state: "running", sizeRw: 5 * 1024 ** 2, sizeRoot: GB }],
  volumes: [
    { name: "data", driver: "local", project: "shop", size: 4 * GB, refCount: 1 },
    { name: "nfs-vol", driver: "nfs", size: -1, refCount: -1 },
  ],
  buildCache: { count: 3, size: 2 * GB, reclaimable: GB },
};

let container: HTMLDivElement;
let root: Root;
const click = async (label: string) => {
  const b = [...container.querySelectorAll("button")].find((x) => x.textContent?.includes(label)) as HTMLElement;
  await act(async () => b.click());
};

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.mocked(api.diskReport).mockResolvedValue(report);
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => { root.render(<MemoryRouter><DiskTab /></MemoryRouter>); });
});
afterEach(() => { act(() => root.unmount()); container.remove(); vi.clearAllMocks(); vi.useRealTimers(); });

describe("DiskTab", () => {
  it("loads without forcing a refresh", () => {
    expect(api.diskReport).toHaveBeenCalledWith(false);
  });

  it("ranks images with unique and total size, marks unused, and never shows an image total", () => {
    const text = container.textContent ?? "";
    expect(text).toContain("big:1");
    expect(text).toContain("2.0 GB"); // unique
    expect(text).toContain("unused");
    expect(text).toContain("unknown"); // the image whose shared size wasn't computed
    expect(text).toContain("(untagged)");
  });

  it("shows an unmeasured volume as unknown, not 0, and counts it in the KPI", async () => {
    await click("Volumes");
    const text = container.textContent ?? "";
    expect(text).toContain("nfs-vol");
    expect(text).toContain("unknown");
    expect(text).toContain("1 unknown");
    expect(text).toContain("4.0 GB");
  });

  it("Refresh asks the server for a fresh report", async () => {
    vi.mocked(api.diskReport).mockClear();
    await click("Refresh");
    expect(api.diskReport).toHaveBeenCalledWith(true);
  });

  it("re-reads on its own only once a minute", async () => {
    vi.useFakeTimers();
    act(() => root.unmount());
    vi.mocked(api.diskReport).mockClear();
    root = createRoot(container);
    await act(async () => { root.render(<MemoryRouter><DiskTab /></MemoryRouter>); });
    expect(api.diskReport).toHaveBeenCalledTimes(1);
    await act(async () => { vi.advanceTimersByTime(59_000); });
    expect(api.diskReport).toHaveBeenCalledTimes(1);
    await act(async () => { vi.advanceTimersByTime(2_000); });
    expect(api.diskReport).toHaveBeenCalledTimes(2);
  });

  it("sizeLabel never turns unknown into zero", () => {
    expect(sizeLabel(-1)).toBe("unknown");
    expect(sizeLabel(0)).toBe("0 B");
  });
});
