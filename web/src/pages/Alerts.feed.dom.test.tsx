/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { Alerts } from "./Alerts";
import { DialogProvider } from "../components/Dialog";
import { api } from "../lib/api";
import { clearPrefs } from "../lib/prefs";
import { resetAlertStream } from "../lib/alertStream";
import type { AlertEvent } from "../lib/types";

// The feed hides "repeat" events by default (a condition still being true,
// re-announced every cooldown, buries the firing/resolved events people look
// for) and shows what kind of event a row is, and whether it was silenced, as
// icon flags in a column of their own instead of words beside the severity.

vi.mock("../lib/api", () => ({
  api: {
    alerts: vi.fn(),
    hosts: () => Promise.resolve([]),
    savePrefs: () => Promise.resolve({ ok: true }),
  },
}));
vi.mock("../auth/AuthContext", () => ({ useAuth: () => ({ user: { id: 1, username: "admin", role: "admin", sections: ["alerts"] } }) }));

const row = (id: number, kind: string, suppressed = false) =>
  ({
    id, ruleName: "Memory", severity: "warning", kind, hostId: 0, hostName: "local", containerName: "es01",
    message: "MEM high", createdAt: "2026-09-25T10:00:00Z", acknowledged: false, suppressed, type: "resource", deliveries: [],
  }) as unknown as AlertEvent;

let container: HTMLDivElement;
let root: Root;
const lastCall = () => vi.mocked(api.alerts).mock.calls.filter((c) => c[0] && (c[0] as { limit?: number }).limit === 50).at(-1)![0]!;

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  clearPrefs();
  resetAlertStream();
  vi.mocked(api.alerts).mockResolvedValue({ events: [row(1, "firing"), row(2, "repeat", true)], total: 2, unread: 0, outstanding: 0 } as never);
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root.render(<MemoryRouter><DialogProvider><Alerts /></DialogProvider></MemoryRouter>);
  });
});
afterEach(() => { act(() => root.unmount()); container.remove(); vi.clearAllMocks(); });

describe("Alerts feed: repeats", () => {
  it("hides repeats by default", () => {
    expect((lastCall() as { hideRepeats?: boolean }).hideRepeats).toBe(true);
  });

  it("'Show repeats' includes them again, and the choice is remembered", async () => {
    const box = [...container.querySelectorAll("label")].find((l) => l.textContent?.includes("Show repeats"))!.querySelector("input") as HTMLInputElement;
    await act(async () => box.click());
    expect((lastCall() as { hideRepeats?: boolean }).hideRepeats).toBe(false);
    const { getPref } = await import("../lib/prefs");
    expect(getPref("alerts.showRepeats", false)).toBe(true);
  });

  it("picking Repeat as the lifecycle shows them without the switch — you asked for exactly those", async () => {
    const select = [...container.querySelectorAll("label")].find((l) => l.textContent?.includes("Lifecycle"))!.querySelector("select") as HTMLSelectElement;
    const setter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, "value")!.set!;
    await act(async () => { setter.call(select, "repeat"); select.dispatchEvent(new Event("change", { bubbles: true })); });
    const p = lastCall() as { kind?: string; hideRepeats?: boolean };
    expect(p.kind).toBe("repeat");
    expect(p.hideRepeats).toBe(false);
  });
});

describe("Alerts feed: event flags", () => {
  it("shows repeat and silenced as icon flags in their own cell, not as words in the severity cell", () => {
    const repeatRow = [...container.querySelectorAll("tbody tr")][1];
    const cells = repeatRow.querySelectorAll("td");
    const severityCell = cells[1];
    const flagsCell = cells[2];
    expect(severityCell.textContent?.trim().toLowerCase()).toBe("warning"); // just the severity
    expect(flagsCell.querySelector('[aria-label="repeat"]')).not.toBeNull();
    expect(flagsCell.querySelector('[aria-label="silenced"]')).not.toBeNull();
    expect(repeatRow.textContent).not.toContain("silenced"); // no spelled-out badge anywhere
  });

  it("an ordinary firing event carries no flags", () => {
    const flags = [...container.querySelectorAll("tbody tr")][0].querySelectorAll("td")[2];
    expect(flags.querySelector("[aria-label]")).toBeNull();
  });
});
