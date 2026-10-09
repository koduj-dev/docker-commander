/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { Troubleshooting } from "./Troubleshooting";
import { clearPrefs } from "../lib/prefs";
import type { DiagnosticsReport } from "../lib/types";

// KPI strip must count by status, and a check's details must default to
// collapsed for a healthy result and expanded for one that needs attention —
// the whole point of the collapse is that a mostly-OK report doesn't force a
// long scroll, while a real problem is never hidden behind an extra click.

const runDiagnostics = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return { ...actual, api: { ...actual.api, runDiagnostics, savePrefs: () => Promise.resolve() } };
});

const report: DiagnosticsReport = {
  hostId: 0,
  generatedAt: "2026-01-01T00:00:00Z",
  checks: [
    { id: "net", name: "Network overlap", status: "ok", message: "no overlapping subnets", details: ["br-abc: 172.18.0.0/16"] },
    { id: "mtu", name: "MTU mismatch", status: "warn", message: "host MTU differs from a bridge network", details: ["eth0: 1500", "br-xyz: 1450"] },
    { id: "disk", name: "Disk space", status: "fail", message: "root filesystem is nearly full", details: ["/: 2% free"] },
    { id: "rotation", name: "Log rotation", status: "skipped", message: "not applicable" },
  ],
};

let container: HTMLDivElement;
let root: Root | undefined;

async function render() {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter>
        <Troubleshooting />
      </MemoryRouter>,
    );
  });
  await act(async () => {}); // let runDiagnostics() resolve
}

beforeEach(() => {
  clearPrefs(); // open/closed choices are remembered; don't leak them between tests
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  runDiagnostics.mockReset().mockResolvedValue(report);
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container?.remove();
});

function checkCard(name: string): HTMLElement {
  const heading = [...container.querySelectorAll(".font-medium")].find((e) => e.textContent === name);
  if (!heading) throw new Error(`no check card named ${name}`);
  return heading.closest(".card") as HTMLElement;
}

describe("Troubleshooting KPI strip", () => {
  it("counts each status correctly", async () => {
    await render();
    expect(container.textContent).toContain("1 of 4");
  });
});

describe("Troubleshooting collapsible details", () => {
  it("a warn/fail check's details are visible without clicking anything", async () => {
    await render();
    expect(checkCard("MTU mismatch").textContent).toContain("br-xyz: 1450");
    expect(checkCard("Disk space").textContent).toContain("/: 2% free");
  });

  it("an ok check's details are collapsed by default, and expand on click", async () => {
    await render();
    const card = checkCard("Network overlap");
    expect(card.textContent).not.toContain("br-abc: 172.18.0.0/16");

    const header = card.querySelector("button[aria-expanded]") as HTMLElement;
    expect(header.getAttribute("aria-expanded")).toBe("false");
    await act(async () => header.click());

    expect(card.textContent).toContain("br-abc: 172.18.0.0/16");
    expect(header.getAttribute("aria-expanded")).toBe("true");
  });

  it("a check with no details is never clickable", async () => {
    await render();
    const card = checkCard("Log rotation");
    expect(card.querySelector("button")).toBeNull();
  });

  it("re-applies the open/closed default when a rerun changes a check's status", async () => {
    await render();
    const flipped: DiagnosticsReport = {
      ...report,
      checks: report.checks.map((c) => (c.id === "net" ? { ...c, status: "warn" as const } : c)),
    };
    expect(checkCard("Network overlap").textContent).not.toContain("br-abc: 172.18.0.0/16");
    runDiagnostics.mockResolvedValue(flipped);
    const run = [...container.querySelectorAll("button")].find((b) => b.textContent?.includes("Run diagnostics")) as HTMLElement;
    await act(async () => run.click());
    expect(checkCard("Network overlap").textContent).toContain("br-abc: 172.18.0.0/16");
  });
});

describe("Troubleshooting remembers what you collapsed", () => {
  const toggleOf = (name: string) => checkCard(name).querySelector("button[aria-expanded]") as HTMLElement;
  async function remount() {
    act(() => root!.unmount());
    root = undefined;
    container.remove();
    await render();
  }

  it("a collapsed warning stays collapsed after a reload", async () => {
    await render();
    expect(checkCard("MTU mismatch").textContent).toContain("br-xyz: 1450"); // open by default
    await act(async () => toggleOf("MTU mismatch").click());
    expect(checkCard("MTU mismatch").textContent).not.toContain("br-xyz: 1450");
    await remount();
    expect(checkCard("MTU mismatch").textContent).not.toContain("br-xyz: 1450");
  });

  it("an OK check you opened stays open after a reload", async () => {
    await render();
    await act(async () => toggleOf("Network overlap").click());
    await remount();
    expect(checkCard("Network overlap").textContent).toContain("br-abc: 172.18.0.0/16");
  });

  it("does not let an old choice hide a check whose status has since changed", async () => {
    await render();
    await act(async () => toggleOf("Network overlap").click()); // opened while OK
    await act(async () => toggleOf("Network overlap").click()); // collapsed while OK
    runDiagnostics.mockResolvedValue({
      ...report,
      checks: report.checks.map((c) => (c.id === "net" ? { ...c, status: "fail" as const } : c)),
    });
    await remount();
    expect(checkCard("Network overlap").textContent).toContain("br-abc: 172.18.0.0/16"); // failing → open again
  });
});
