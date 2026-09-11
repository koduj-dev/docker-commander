/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MaintenanceWindows } from "./Alerts";
import { DialogProvider } from "../components/Dialog";
import type { MaintenanceWindow, AlertRule, Host } from "../lib/types";

// Maintenance windows suppress alert delivery — this UI is how an operator
// silences a planned-work window and, just as importantly, how they end one
// early. Ending/deleting are destructive to the paging behaviour a window
// controls, so both must go through the app's confirm dialog, never a
// one-click action, per this repo's confirm-destructive-actions convention.

const maintenanceWindows = vi.hoisted(() => vi.fn());
const createMaintenanceWindow = vi.hoisted(() => vi.fn());
const endMaintenanceWindow = vi.hoisted(() => vi.fn());
const deleteMaintenanceWindow = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", () => ({
  api: {
    maintenanceWindows,
    alertRules: () => Promise.resolve([] as AlertRule[]),
    hosts: () => Promise.resolve([{ id: 0, name: "local" }] as Host[]),
    createMaintenanceWindow,
    endMaintenanceWindow,
    deleteMaintenanceWindow,
  },
}));

const window1: MaintenanceWindow = {
  id: 1, name: "DB upgrade", reason: "planned Postgres bump", authorId: 1, author: "alice",
  hostIds: [], project: "", container: "", ruleId: null, severities: [],
  recurring: false,
  startsAt: new Date(Date.now() - 60_000).toISOString(),
  endsAt: new Date(Date.now() + 3_600_000).toISOString(),
  ended: false, createdAt: new Date().toISOString(),
};

let container: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  maintenanceWindows.mockResolvedValue([window1]);
  endMaintenanceWindow.mockResolvedValue({ ok: true });
  deleteMaintenanceWindow.mockResolvedValue({ ok: true });
  createMaintenanceWindow.mockResolvedValue({ id: 2 });

  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root.render(
      <DialogProvider>
        <MaintenanceWindows />
      </DialogProvider>,
    );
  });
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

function buttons(): HTMLButtonElement[] {
  return [...container.querySelectorAll("button")] as HTMLButtonElement[];
}

function typeInto(el: Element, value: string) {
  const input = el as HTMLInputElement;
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")?.set;
  setter?.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

function rowButton(rowText: string, title: string): HTMLButtonElement {
  const row = [...container.querySelectorAll("tr")].find((r) => r.textContent?.includes(rowText));
  if (!row) throw new Error(`row containing ${JSON.stringify(rowText)} not found`);
  const btn = row.querySelector(`button[title="${title}"]`);
  if (!btn) throw new Error(`button titled ${title} not found in row`);
  return btn as HTMLButtonElement;
}

describe("MaintenanceWindows", () => {
  it("lists an existing window with its name and reason", async () => {
    expect(container.textContent).toContain("DB upgrade");
    expect(container.textContent).toContain("planned Postgres bump");
  });

  it("ending a window requires confirmation and only then calls the API", async () => {
    await act(async () => rowButton("DB upgrade", "End now").click());

    expect(endMaintenanceWindow).not.toHaveBeenCalled();

    const confirm = buttons().find((b) => b.textContent === "End now");
    if (!confirm) throw new Error("confirm dialog button not found");
    await act(async () => confirm.click());

    expect(endMaintenanceWindow).toHaveBeenCalledWith(1);
  });

  it("cancelling the end-window confirm dialog never calls the API", async () => {
    await act(async () => rowButton("DB upgrade", "End now").click());

    const cancel = buttons().find((b) => b.textContent === "Cancel");
    if (!cancel) throw new Error("cancel button not found");
    await act(async () => cancel.click());

    expect(endMaintenanceWindow).not.toHaveBeenCalled();
  });

  it("deleting a window requires confirmation and only then calls the API", async () => {
    await act(async () => rowButton("DB upgrade", "Delete").click());

    expect(deleteMaintenanceWindow).not.toHaveBeenCalled();

    const confirm = buttons().find((b) => b.textContent === "Delete" && !b.title);
    if (!confirm) throw new Error("confirm dialog button not found");
    await act(async () => confirm.click());

    expect(deleteMaintenanceWindow).toHaveBeenCalledWith(1);
  });

  it("creating a one-off window submits name, reason and a start/end derived from the duration", async () => {
    const newBtn = buttons().find((b) => b.textContent?.includes("New window"));
    if (!newBtn) throw new Error("New window button not found");
    await act(async () => newBtn.click());

    const nameInput = container.querySelector('input[required]') as HTMLInputElement;
    const reasonInput = [...container.querySelectorAll("input")].find((i) => i.placeholder?.startsWith("Why")) as HTMLInputElement;
    await act(async () => {
      typeInto(nameInput, "Router reboot");
      typeInto(reasonInput, "swapping the edge router");
    });

    const form = container.querySelector("form");
    if (!form) throw new Error("form not found");
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });

    expect(createMaintenanceWindow).toHaveBeenCalledTimes(1);
    const body = createMaintenanceWindow.mock.calls[0][0];
    expect(body.name).toBe("Router reboot");
    expect(body.reason).toBe("swapping the edge router");
    expect(body.recurring).toBe(false);
    expect(new Date(body.endsAt).getTime()).toBeGreaterThan(new Date(body.startsAt).getTime());
  });
});
