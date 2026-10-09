/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { SelfUpdatePolicyEditor } from "./SelfUpdatePolicyEditor";
import { api } from "../lib/api";
import type { UpdateStatus } from "../lib/types";

// The editor must reflect the server's own capability flags — a saved
// "enabled" policy that DC_UPDATE_CHECK=0 or DC_SELF_UPDATE=0 (or an
// unsupported restart mode, e.g. Windows) guarantees will never run is
// misleading, since it looks identical to a working one.

vi.mock("../lib/api", () => ({
  api: {
    updateStatus: vi.fn(),
    setSelfUpdatePolicy: vi.fn(),
  },
}));

let container: HTMLDivElement;
let root: Root | undefined;

async function renderEditor(status: Partial<UpdateStatus>) {
  vi.mocked(api.updateStatus).mockResolvedValue({
    current: "1.0.0",
    updateAvailable: false,
    selfUpdatePolicy: { enabled: false, granularity: "minor" },
    ...status,
  } as UpdateStatus);
  await act(async () => {
    root!.render(<SelfUpdatePolicyEditor />);
  });
}

function checkbox(): HTMLInputElement {
  return container.querySelector("input[type=checkbox]") as HTMLInputElement;
}

function select(): HTMLSelectElement {
  return container.querySelector("select") as HTMLSelectElement;
}

function saveButton(): HTMLButtonElement {
  return [...container.querySelectorAll("button")].find((b) => b.textContent?.includes("Save"))! as HTMLButtonElement;
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container.remove();
  vi.clearAllMocks();
});

describe("SelfUpdatePolicyEditor", () => {
  it("is interactive when the update check and self-update are both available", async () => {
    await renderEditor({ disabled: false, selfUpdate: true });
    expect(checkbox().disabled).toBe(false);
    expect(saveButton().disabled).toBe(false);
    expect(container.textContent).not.toContain("can never run");
  });

  it("disables every control and explains why when the update check itself is off", async () => {
    await renderEditor({ disabled: true, selfUpdate: false });
    expect(checkbox().disabled).toBe(true);
    expect(select().disabled).toBe(true);
    expect(saveButton().disabled).toBe(true);
    expect(container.textContent).toContain("DC_UPDATE_CHECK=0");
  });

  it("disables every control and explains why when self-update is unavailable (e.g. Windows, or DC_SELF_UPDATE=0)", async () => {
    await renderEditor({ disabled: false, selfUpdate: false });
    expect(checkbox().disabled).toBe(true);
    expect(select().disabled).toBe(true);
    expect(saveButton().disabled).toBe(true);
    expect(container.textContent).toContain("DC_SELF_UPDATE=0");
  });

  it("never sends a save request while unavailable, even if a stale click slips through", async () => {
    await renderEditor({ disabled: false, selfUpdate: false });
    // The button is disabled, but assert the guard is in the logic too, not
    // just the DOM attribute a test could accidentally not exercise.
    saveButton().click();
    await new Promise((r) => setTimeout(r, 0));
    expect(api.setSelfUpdatePolicy).not.toHaveBeenCalled();
  });
});
