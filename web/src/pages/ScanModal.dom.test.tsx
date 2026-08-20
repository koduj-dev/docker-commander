/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ScanModal } from "./Images";
import { DialogProvider } from "../components/Dialog";
import type { ImageSummary } from "../lib/types";

// The scan modal lets a reviewer triage findings: select rows, ignore them in
// bulk, and reveal/un-ignore them again. Ignoring is GLOBAL (by CVE id), so a
// finding hidden here would be hidden on every other image's scan too — the
// filtering logic is exactly what these tests pin.

const scanImage = vi.hoisted(() => vi.fn());
const ignoredCVEs = vi.hoisted(() => vi.fn());
const ignoreCVEs = vi.hoisted(() => vi.fn());
const unignoreCVE = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return { ...actual, api: { scanImage, ignoredCVEs, ignoreCVEs, unignoreCVE } };
});

const img: ImageSummary = {
  id: "sha256:abc",
  repoTags: ["myapp:latest"],
  size: 0,
  created: 0,
  inUse: false,
} as ImageSummary;

const scanResult = {
  ref: "myapp:latest",
  summary: { CRITICAL: 1, HIGH: 1 },
  vulns: [
    { id: "CVE-2024-1111", severity: "CRITICAL", package: "libfoo", version: "1.0" },
    { id: "CVE-2024-2222", severity: "HIGH", package: "libbar", version: "2.0" },
  ],
};

let container: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  scanImage.mockReset().mockResolvedValue({ available: true, ok: true, result: scanResult });
  ignoredCVEs.mockReset().mockResolvedValue([]);
  ignoreCVEs.mockReset().mockResolvedValue({ ok: true });
  unignoreCVE.mockReset().mockResolvedValue({ ok: true });
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root.render(
      <DialogProvider>
        <ScanModal img={img} onClose={() => {}} />
      </DialogProvider>,
    );
  });
  await act(async () => {}); // let the scan + ignored-list fetches resolve
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

function rowFor(cve: string): HTMLTableRowElement {
  const row = [...container.querySelectorAll("tr")].find((r) => r.textContent?.includes(cve));
  if (!row) throw new Error(`no row for ${cve}`);
  return row as HTMLTableRowElement;
}

describe("scan findings triage", () => {
  it("shows both findings when nothing is ignored yet", () => {
    expect(container.textContent).toContain("CVE-2024-1111");
    expect(container.textContent).toContain("CVE-2024-2222");
    expect(container.textContent).not.toContain("ignored");
  });

  it("bulk-ignores the selected rows and hides them", async () => {
    const checkbox = rowFor("CVE-2024-1111").querySelector('input[type="checkbox"]') as HTMLInputElement;
    await act(async () => checkbox.click());

    const ignoreBtn = [...container.querySelectorAll("button")].find((b) => b.textContent?.includes("Ignore selected"));
    if (!ignoreBtn) throw new Error("Ignore selected button not rendered");
    await act(async () => ignoreBtn.click());

    // The confirm dialog is now open; accept it.
    const confirmBtn = [...container.querySelectorAll("button")].find((b) => b.textContent === "Ignore");
    if (!confirmBtn) throw new Error("confirm dialog's Ignore button not found");
    // After confirming, the component re-fetches ignoredCVEs — make it report
    // the CVE we just ignored so the re-render actually filters it.
    ignoredCVEs.mockResolvedValue([{ id: "CVE-2024-1111", reason: "", addedBy: "admin", createdAt: "" }]);
    await act(async () => confirmBtn.click());
    await act(async () => {}); // let refreshIgnored's fetch resolve

    expect(ignoreCVEs).toHaveBeenCalledWith(["CVE-2024-1111"], "");
    expect(container.textContent).not.toContain("CVE-2024-1111");
    expect(container.textContent).toContain("CVE-2024-2222");
    expect(container.textContent).toContain("Show ignored (1)");
  });

  it("cancelling the confirm dialog does not call the API", async () => {
    const checkbox = rowFor("CVE-2024-1111").querySelector('input[type="checkbox"]') as HTMLInputElement;
    await act(async () => checkbox.click());
    const ignoreBtn = [...container.querySelectorAll("button")].find((b) => b.textContent?.includes("Ignore selected"));
    await act(async () => ignoreBtn!.click());

    const cancelBtn = [...container.querySelectorAll("button")].find((b) => b.textContent === "Cancel");
    await act(async () => cancelBtn!.click());

    expect(ignoreCVEs).not.toHaveBeenCalled();
    expect(container.textContent).toContain("CVE-2024-1111");
  });

  it("reveals an already-ignored finding via Show ignored, with an Un-ignore action", async () => {
    ignoredCVEs.mockResolvedValue([{ id: "CVE-2024-1111", reason: "accepted", addedBy: "admin", createdAt: "" }]);
    await act(async () => {
      root.render(
        <DialogProvider>
          <ScanModal img={img} onClose={() => {}} key="reload" />
        </DialogProvider>,
      );
    });
    await act(async () => {});

    // Ignored by default: hidden from the plain view.
    expect(container.textContent).not.toContain("CVE-2024-1111");

    const toggle = [...container.querySelectorAll("button")].find((b) => b.textContent?.includes("Show ignored"));
    if (!toggle) throw new Error("Show ignored toggle not rendered");
    await act(async () => toggle.click());

    expect(container.textContent).toContain("CVE-2024-1111");
    const unignoreBtn = rowFor("CVE-2024-1111").querySelector('button[title="Un-ignore"]') as HTMLButtonElement;
    expect(unignoreBtn).toBeTruthy();

    await act(async () => unignoreBtn.click());
    expect(unignoreCVE).toHaveBeenCalledWith("CVE-2024-1111");
  });
});
