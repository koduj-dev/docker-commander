/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { Shell } from "./Shell";
import { DialogProvider } from "../components/Dialog";
import { api } from "../lib/api";
import { clearPrefs } from "../lib/prefs";
import type { UpdateStatus } from "../lib/types";

// The "update available" banner offers Update & restart only when the server
// says the install can run. When it can't (a .deb/.rpm install, or a directory
// the service can't write), it still announces the release but says how to
// update by hand instead of offering a button that is bound to fail.

vi.mock("../lib/api", () => ({
  api: {
    hosts: () => Promise.resolve([]),
    version: () => Promise.resolve({ version: "test" }),
    alerts: () => Promise.resolve({ events: [], unread: 0 }),
    updateStatus: vi.fn(),
    prefs: () => Promise.resolve({}),
    savePrefs: () => Promise.resolve({ ok: true }),
  },
}));

vi.mock("../auth/AuthContext", () => ({
  useAuth: () => ({
    user: { id: 1, username: "admin", role: "admin", sections: [] },
    logout: () => Promise.resolve(),
  }),
}));

let container: HTMLDivElement;
let root: Root | undefined;

async function renderShell(status: Partial<UpdateStatus>) {
  vi.mocked(api.updateStatus).mockResolvedValue({
    current: "1.6.2",
    latest: "1.7.2",
    updateAvailable: true,
    url: "https://example/r",
    selfUpdatePolicy: { enabled: false, granularity: "minor" },
    ...status,
  } as UpdateStatus);
  await act(async () => {
    root!.render(
      <MemoryRouter>
        <DialogProvider>
          <Shell>
            <div />
          </Shell>
        </DialogProvider>
      </MemoryRouter>,
    );
  });
}

const updateButton = () => [...container.querySelectorAll("button")].find((b) => b.textContent?.includes("Update & restart"));

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  clearPrefs();
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container.remove();
  clearPrefs();
  vi.clearAllMocks();
});

describe("UpdateBanner", () => {
  it("offers Update & restart when the install can run", async () => {
    await renderShell({ selfUpdate: true });
    expect(container.textContent).toContain("1.7.2 is available");
    expect(updateButton()).toBeDefined();
  });

  it("for a packaged install, announces the release and gives the package-manager command — no button", async () => {
    await renderShell({
      selfUpdate: false,
      selfUpdateBlocked: "Installed from a package, so the package manager updates it.",
      updateCommand: "sudo apt update && sudo apt install --only-upgrade dockercmd",
    });
    expect(container.textContent).toContain("1.7.2 is available");
    expect(updateButton()).toBeUndefined();
    expect(container.textContent).toContain("Installed from a package");
    const code = [...container.querySelectorAll("code")].map((c) => c.textContent);
    expect(code).toContain("sudo apt update && sudo apt install --only-upgrade dockercmd");
  });

  it("shows no reason line when self-update is simply turned off", async () => {
    await renderShell({ selfUpdate: false });
    expect(updateButton()).toBeUndefined();
    expect(container.querySelector("code")).toBeNull();
  });
});
