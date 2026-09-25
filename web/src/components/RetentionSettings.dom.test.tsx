/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { RetentionSettings } from "./RetentionSettings";
import { DialogProvider } from "./Dialog";
import type { RetentionState } from "../lib/types";

const retention = vi.hoisted(() => vi.fn());
const setRetention = vi.hoisted(() => vi.fn());
const purgeRetention = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", () => ({ api: { retention, setRetention, purgeRetention } }));

const state = (over: Partial<RetentionState> = {}): RetentionState => ({
  policy: { alertEventsDays: 90, alertDeliveriesDays: 90, auditDays: 365, revisionsKeep: 50 },
  defaults: { alertEventsDays: 90, alertDeliveriesDays: 90, auditDays: 365, revisionsKeep: 50 },
  limits: { minAuditDays: 30, minAlertDays: 1, minRevisionsKeep: 3, maxDays: 36500 },
  stats: {
    alertEvents: { rows: 1234, oldest: "2026-06-01T00:00:00Z" },
    alertDeliveries: { rows: 5, oldest: "2026-07-01T00:00:00Z" },
    audit: { rows: 40, oldest: "2026-01-02T00:00:00Z" },
    revisions: { rows: 0 },
    dbBytes: 5 * 1024 * 1024, dbFreeBytes: 1024 * 1024,
  },
  lastRun: null,
  ...over,
});

let container: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  retention.mockResolvedValue(state());
  setRetention.mockResolvedValue({ ok: true });
  purgeRetention.mockResolvedValue({
    at: new Date().toISOString(), trigger: "manual", durationMs: 3, alertEvents: 2, alertDeliveries: 1, audit: 0,
    revisions: 0, revisionFiles: 0, dbBytesBefore: 10, dbBytesAfter: 10,
  });
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => root.render(<DialogProvider><RetentionSettings /></DialogProvider>));
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

const input = (label: string) => container.querySelector(`#retention-${label}`) as HTMLInputElement;
const button = (text: string) => [...container.querySelectorAll("button")].find((b) => b.textContent?.includes(text)) as HTMLButtonElement;

async function type(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")?.set;
  await act(async () => {
    setter?.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function keepForever(label: string) {
  const box = input(label).parentElement!.querySelector('input[type="checkbox"]') as HTMLInputElement;
  await act(async () => box.click());
}

describe("RetentionSettings", () => {
  it("shows the saved policy with what is stored and the database size", () => {
    expect(input("alertEventsDays").value).toBe("90");
    expect(input("auditDays").value).toBe("365");
    expect(input("revisionsKeep").value).toBe("50");
    expect(container.textContent).toContain("1,234 stored");
    expect(container.textContent).toContain("oldest 2026-06-01");
    expect(container.textContent).toContain("5.0 MB");
    expect(container.textContent).toContain("Nothing has been purged yet");
  });

  it("saves an edited value, and only once something changed", async () => {
    expect(button("Save").disabled).toBe(true);
    await type(input("auditDays"), "180");
    expect(button("Save").disabled).toBe(false);
    await act(async () => button("Save").click());
    expect(setRetention).toHaveBeenCalledWith({ alertEventsDays: 90, alertDeliveriesDays: 90, auditDays: 180, revisionsKeep: 50 });
  });

  it("'keep forever' sends 0, and clearing a field to retype does not turn it on", async () => {
    await keepForever("auditDays");
    expect(input("auditDays").disabled).toBe(true);
    await act(async () => button("Save").click());
    expect(setRetention.mock.calls[0][0].auditDays).toBe(0);

    await keepForever("auditDays"); // back to a number
    await type(input("revisionsKeep"), "");
    expect(input("revisionsKeep").disabled).toBe(false); // an empty box is not "forever"
  });

  it("refuses an audit TTL under the floor and explains why", async () => {
    await type(input("auditDays"), "7");
    expect(container.textContent).toContain("Audit log must be between 30");
    expect(button("Save").disabled).toBe(true);
  });

  it("refuses delivery records that would outlive their events", async () => {
    await type(input("alertEventsDays"), "30");
    expect(container.textContent).toContain("cannot be kept longer than the alert events");
    expect(button("Save").disabled).toBe(true);
  });

  it("resets the form to the defaults without saving", async () => {
    await type(input("auditDays"), "90");
    await act(async () => button("Reset to defaults").click());
    expect(input("auditDays").value).toBe("365");
    expect(setRetention).not.toHaveBeenCalled();
  });

  it("purges only after a confirmation, and never while there are unsaved changes", async () => {
    await type(input("auditDays"), "90");
    expect(button("Purge now").disabled).toBe(true); // it would purge the SAVED policy, not this form
    await act(async () => button("Reset to defaults").click());
    expect(button("Purge now").disabled).toBe(false);

    await act(async () => button("Purge now").click());
    expect(purgeRetention).not.toHaveBeenCalled();
    await act(async () => button("Cancel").click());
    expect(purgeRetention).not.toHaveBeenCalled();

    await act(async () => button("Purge now").click());
    await act(async () => [...container.ownerDocument.querySelectorAll("button")].find((b) => b.textContent === "Purge")!.click());
    expect(purgeRetention).toHaveBeenCalledTimes(1);
    expect(container.textContent).toContain("Purged 3 rows");
  });

  it("shows what the last purge did", async () => {
    retention.mockResolvedValue(state({
      lastRun: { at: "2026-09-25T02:00:00Z", trigger: "scheduled", durationMs: 12, alertEvents: 7, alertDeliveries: 3, audit: 2, revisions: 1, revisionFiles: 1, dbBytesBefore: 2048, dbBytesAfter: 1024, error: "boom" },
    }));
    act(() => root.unmount());
    root = createRoot(container);
    await act(async () => root.render(<DialogProvider><RetentionSettings /></DialogProvider>));
    expect(container.textContent).toContain("Deleted 7 alert events, 3 deliveries");
    expect(container.textContent).toContain("Errors: boom");
  });
});
