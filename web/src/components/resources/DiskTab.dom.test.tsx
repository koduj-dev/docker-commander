/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { DiskTab, sizeLabel } from "./DiskTab";
import { api } from "../../lib/api";
import { clearPrefs } from "../../lib/prefs";
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
  reclaimable: { images: 2 * GB, containers: 0, volumes: 0, buildCache: GB, total: 3 * GB },
};

let container: HTMLDivElement;
let root: Root;
const click = async (label: string) => {
  const b = [...container.querySelectorAll("button")].find((x) => x.textContent?.includes(label)) as HTMLElement;
  await act(async () => b.click());
};

beforeEach(async () => {
  clearPrefs();
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

  const names = () => [...container.querySelectorAll("tbody tr td:first-child")].map((td) => td.textContent?.trim());
  const header = (label: string) => [...container.querySelectorAll("th")].find((t) => t.textContent?.trim() === label) as HTMLElement;

  it("highlights the active sort column, and starts on unique size descending", () => {
    expect(header("Unique").getAttribute("aria-sort")).toBe("descending");
    expect(header("Size").getAttribute("aria-sort")).toBeNull();
    expect(header("Unique").querySelector("button")!.className).toContain("text-accent");
  });

  it("re-sorts by name (A→Z first, then flips), and by used-by count", async () => {
    await act(async () => (header("Image").querySelector("button") as HTMLElement).click());
    expect(names()).toEqual(["big:1", "bbbbbbbbbbbb (untagged)"]); // alphabetical: "big:1" < "sha256..."/id
    expect(header("Image").getAttribute("aria-sort")).toBe("ascending");
    await act(async () => (header("Image").querySelector("button") as HTMLElement).click());
    expect(header("Image").getAttribute("aria-sort")).toBe("descending");
    await act(async () => (header("Used by").querySelector("button") as HTMLElement).click());
    expect(names()[0]).toContain("untagged"); // 2 containers first
  });

  it("keeps an unknown size last in either direction", async () => {
    const unique = () => header("Unique").querySelector("button") as HTMLElement;
    expect(names()[names().length - 1]).toContain("untagged"); // descending: unknown last
    await act(async () => unique().click()); // → ascending
    expect(header("Unique").getAttribute("aria-sort")).toBe("ascending");
    expect(names()[names().length - 1]).toContain("untagged"); // still last, not first
  });

  it("puts Refresh in the same row as the section switcher", () => {
    const refresh = [...container.querySelectorAll("button")].find((b) => b.textContent?.includes("Refresh")) as HTMLElement;
    const images = [...container.querySelectorAll("button")].find((b) => b.textContent?.includes("Images")) as HTMLElement;
    expect(refresh.closest(".flex-wrap")).toBe(images.closest(".flex-wrap"));
  });

  it("shows Docker's reclaimable estimate, with what it consists of, and says it is a lower bound", () => {
    const text = container.textContent ?? "";
    expect(text).toContain("Reclaimable");
    expect(text).toContain("3.0 GB"); // the total
    expect(text).toContain("what a prune would free");
    expect(text).toContain("lower bound");
    expect(text).toContain("1 unused"); // exactly one of the two images has 0 containers
  });

  it("'Unused only' narrows images to those no container uses — an unknown count is not unused", async () => {
    const select = [...container.querySelectorAll("select")].find((sel) => [...(sel as HTMLSelectElement).options].some((o) => o.value === "unused")) as HTMLSelectElement;
    const setter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, "value")!.set!;
    await act(async () => { setter.call(select, "unused"); select.dispatchEvent(new Event("change", { bubbles: true })); });
    expect(names()).toEqual(["big:1"]); // the other image is used by 2 containers
  });

  it("sizeLabel never turns unknown into zero", () => {
    expect(sizeLabel(-1)).toBe("unknown");
    expect(sizeLabel(0)).toBe("0 B");
  });
});
