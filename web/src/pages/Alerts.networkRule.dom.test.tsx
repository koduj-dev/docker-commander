/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Rules } from "./Alerts";
import { DialogProvider } from "../components/Dialog";
import type { AlertRule, Webhook } from "../lib/types";

// Two new rule shapes added for NEXT.md's "network alerting" item: a
// throughput metric on the existing resource-rule type (entered as MB/s,
// stored as bytes/s), and a brand new "network" rule type that fires on a
// cumulative counter's INCREASE over a window rather than its absolute
// value. Both are exercised through the real form, not by hand-building the
// config, so a form/buildConfig drift shows up here.

const alertRules = vi.hoisted(() => vi.fn());
const webhooks = vi.hoisted(() => vi.fn());
const createAlertRule = vi.hoisted(() => vi.fn());

vi.mock("../auth/AuthContext", () => ({
  useAuth: () => ({ user: { id: 1, username: "admin", role: "admin", readOnly: false, sections: [] } }),
}));

vi.mock("../lib/api", () => ({
  api: {
    alertRules,
    webhooks,
    createAlertRule,
    updateAlertRule: vi.fn(),
    toggleAlertRule: vi.fn(),
    deleteAlertRule: vi.fn(),
    importAlertRules: vi.fn(),
    exportAlertRulesUrl: () => "",
  },
}));

let container: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  alertRules.mockResolvedValue([] as AlertRule[]);
  webhooks.mockResolvedValue([] as Webhook[]);
  createAlertRule.mockResolvedValue({ id: 1 });

  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root.render(
      <DialogProvider>
        <Rules />
      </DialogProvider>,
    );
  });
  // Open the form.
  const newRuleBtn = [...container.querySelectorAll("button")].find((b) => b.textContent?.includes("New rule"));
  if (!newRuleBtn) throw new Error("New rule button not found");
  await act(async () => newRuleBtn.click());
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

function selectWithOption(optionValue: string): HTMLSelectElement {
  const found = [...container.querySelectorAll("select")].find(
    (s) => [...(s as HTMLSelectElement).options].some((o) => o.value === optionValue),
  );
  if (!found) throw new Error(`no <select> offers option ${JSON.stringify(optionValue)}`);
  return found as HTMLSelectElement;
}

function setSelect(select: HTMLSelectElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, "value")!.set!;
  setter.call(select, value);
  select.dispatchEvent(new Event("change", { bubbles: true }));
}

function inputForLabel(labelText: string): HTMLInputElement {
  const label = [...container.querySelectorAll("label")].find((l) => l.textContent?.trim() === labelText);
  if (!label) throw new Error(`label ${JSON.stringify(labelText)} not found`);
  const input = label.parentElement?.querySelector("input");
  if (!input) throw new Error(`no input under label ${JSON.stringify(labelText)}`);
  return input as HTMLInputElement;
}

function typeInto(el: Element, value: string) {
  const input = el as HTMLInputElement;
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")?.set;
  setter?.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

function submit() {
  const btn = [...container.querySelectorAll("button")].find((b) => b.textContent === "Create rule");
  if (!btn) throw new Error("Create rule button not found");
  return act(async () => btn.click());
}

describe("network alert rules", () => {
  it("builds an increase-over-window config for the network rule type", async () => {
    typeInto(inputForLabel("Rule name"), "Drops spike");
    setSelect(selectWithOption("network"), "network");
    setSelect(selectWithOption("neterrors"), "neterrors");
    typeInto(inputForLabel("Increase by at least"), "20");
    typeInto(inputForLabel("Within (seconds)"), "120");

    await submit();

    expect(createAlertRule).toHaveBeenCalledTimes(1);
    const body = createAlertRule.mock.calls[0][0];
    expect(body.type).toBe("network");
    expect(body.config).toEqual({ metric: "neterrors", threshold: 20, windowSec: 120 });
  });

  it("stays silent about drops that never increase — config never encodes an absolute-value comparison", async () => {
    // The config shape itself is the guard here: a "network" rule has no
    // `op` field at all (unlike "resource"), so it CANNOT be built as an
    // absolute threshold — this pins that the two types stay structurally
    // distinct rather than one being a relabeled copy of the other.
    typeInto(inputForLabel("Rule name"), "Drops spike");
    setSelect(selectWithOption("network"), "network");

    await submit();

    const body = createAlertRule.mock.calls[0][0];
    expect(body.config).not.toHaveProperty("op");
  });

  it("converts a network throughput threshold from MB/s to bytes/s", async () => {
    typeInto(inputForLabel("Rule name"), "RX spike");
    setSelect(selectWithOption("resource"), "resource");
    setSelect(selectWithOption("netrx_rate"), "netrx_rate");
    typeInto(inputForLabel("Threshold (MB/s)"), "10");

    await submit();

    const body = createAlertRule.mock.calls[0][0];
    expect(body.type).toBe("resource");
    expect(body.config.metric).toBe("netrx_rate");
    expect(body.config.threshold).toBe(10 * 1024 * 1024);
  });

  it("leaves a plain percentage metric (mem) unconverted", async () => {
    typeInto(inputForLabel("Rule name"), "Mem high");
    setSelect(selectWithOption("resource"), "resource");
    setSelect(selectWithOption("mem"), "mem");
    typeInto(inputForLabel("Threshold %"), "75");

    await submit();

    const body = createAlertRule.mock.calls[0][0];
    expect(body.config.threshold).toBe(75);
  });
});
