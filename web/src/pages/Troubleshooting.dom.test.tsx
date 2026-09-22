/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { Troubleshooting } from "./Troubleshooting";
import type { DiagnosticsReport } from "../lib/types";

// KPI strip must count by status, and a check's details must default to
// collapsed for a healthy result and expanded for one that needs attention —
// the whole point of the collapse is that a mostly-OK report doesn't force a
// long scroll, while a real problem is never hidden behind an extra click.

const runDiagnostics = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return { ...actual, api: { ...actual.api, runDiagnostics } };
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

    const header = card.querySelector(".cursor-pointer") as HTMLElement;
    await act(async () => header.click());

    expect(card.textContent).toContain("br-abc: 172.18.0.0/16");
  });

  it("a check with no details is never clickable", async () => {
    await render();
    const card = checkCard("Log rotation");
    expect(card.querySelector(".cursor-pointer")).toBeNull();
  });
});
