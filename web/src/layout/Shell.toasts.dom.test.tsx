/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { Shell } from "./Shell";
import { DialogProvider } from "../components/Dialog";
import { ToastProvider } from "../components/Toasts";
import { resetAlertStream } from "../lib/alertStream";
import { api } from "../lib/api";
import type { AlertEvent } from "../lib/types";

// A maintenance window suppresses delivery but still RECORDS the event; the
// toast is a notification too, so a silenced event must not pop up — it stays
// in the Alerts feed with its "silenced" badge.

vi.mock("../lib/api", () => ({
  api: {
    hosts: () => Promise.resolve([]),
    version: () => Promise.resolve({ version: "test" }),
    alerts: vi.fn(),
    updateStatus: () => Promise.resolve({ updateAvailable: false }),
    prefs: () => Promise.resolve({}),
    savePrefs: () => Promise.resolve({ ok: true }),
  },
}));
vi.mock("../auth/AuthContext", () => ({
  useAuth: () => ({ user: { id: 1, username: "admin", role: "admin", sections: ["alerts"] }, logout: () => Promise.resolve() }),
}));

const ev = (id: number, ruleName: string, suppressed = false) =>
  ({ id, ruleName, containerName: "", message: "", severity: "warning", kind: "firing", suppressed } as unknown as AlertEvent);

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers();
  resetAlertStream();
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.useRealTimers();
  vi.clearAllMocks();
});

describe("Shell alert toasts", () => {
  it("toasts a fresh event but not one a maintenance window silenced", async () => {
    vi.mocked(api.alerts)
      .mockResolvedValueOnce({ events: [ev(1, "baseline")], unread: 0 } as never) // first poll only sets the baseline
      .mockResolvedValue({ events: [ev(3, "loud-rule"), ev(2, "silenced-rule", true)], unread: 2 } as never);
    await act(async () => {
      root.render(
        <MemoryRouter>
          <DialogProvider>
            <ToastProvider>
              <Shell><div /></Shell>
            </ToastProvider>
          </DialogProvider>
        </MemoryRouter>,
      );
    });
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    const text = container.textContent ?? "";
    expect(text).toContain("loud-rule");
    expect(text).not.toContain("silenced-rule");
  });
});
