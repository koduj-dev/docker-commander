/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { Shell } from "./Shell";
import { DialogProvider } from "../components/Dialog";
import { api } from "../lib/api";
import { clearPrefs } from "../lib/prefs";

// The one-time "you're now on vX.Y.Z" notice after a policy-driven self-update:
// it must show for an admin who hasn't seen this specific auto-apply event,
// disappear once dismissed, and resurface for a *new* auto-apply event even
// for an admin who already dismissed the previous one.

vi.mock("../lib/api", () => ({
  api: {
    hosts: () => Promise.resolve([]),
    version: () => Promise.resolve({ version: "test" }),
    alerts: () => Promise.resolve({ events: [], unread: 0 }),
    updateStatus: vi.fn(() => Promise.resolve({ updateAvailable: false })),
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

async function renderShell() {
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

function noticeText(): string | undefined {
  return [...container.querySelectorAll("div")]
    .find((d) => d.textContent?.includes("applied automatically"))
    ?.textContent ?? undefined;
}

function dismissButton(): HTMLButtonElement {
  const el = [...container.querySelectorAll("button")].find((b) => b.getAttribute("title") === "Dismiss");
  if (!el) throw new Error("no dismiss button for the auto-update notice");
  return el as HTMLButtonElement;
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  clearPrefs();
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  vi.mocked(api.updateStatus).mockReset();
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container.remove();
  clearPrefs();
  vi.clearAllMocks();
});

describe("AutoUpdateNotice", () => {
  it("stays hidden when no auto-update has ever happened", async () => {
    vi.mocked(api.updateStatus).mockResolvedValue({ updateAvailable: false });
    await renderShell();
    expect(noticeText()).toBeUndefined();
  });

  it("shows the version and applied date after a policy-driven auto-apply", async () => {
    vi.mocked(api.updateStatus).mockResolvedValue({
      updateAvailable: false,
      lastAutoUpdate: { version: "1.8.0", appliedAt: "2026-01-02T03:04:05Z" },
    });
    await renderShell();
    expect(noticeText()).toContain("1.8.0");
  });

  it("disappears once dismissed, and does not reappear on a later render of the same event", async () => {
    vi.mocked(api.updateStatus).mockResolvedValue({
      updateAvailable: false,
      lastAutoUpdate: { version: "1.8.0", appliedAt: "2026-01-02T03:04:05Z" },
    });
    await renderShell();
    expect(noticeText()).toContain("1.8.0");

    await act(async () => dismissButton().click());
    expect(noticeText()).toBeUndefined();

    // Remount (simulates a page reload) with the exact same lastAutoUpdate.
    await act(async () => root!.unmount());
    root = createRoot(container);
    await renderShell();
    expect(noticeText()).toBeUndefined();
  });

  it("resurfaces for a new auto-apply event even after the previous one was dismissed", async () => {
    vi.mocked(api.updateStatus).mockResolvedValue({
      updateAvailable: false,
      lastAutoUpdate: { version: "1.8.0", appliedAt: "2026-01-02T03:04:05Z" },
    });
    await renderShell();
    await act(async () => dismissButton().click());
    expect(noticeText()).toBeUndefined();

    await act(async () => root!.unmount());
    root = createRoot(container);
    vi.mocked(api.updateStatus).mockResolvedValue({
      updateAvailable: false,
      lastAutoUpdate: { version: "1.9.0", appliedAt: "2026-02-03T04:05:06Z" },
    });
    await renderShell();
    expect(noticeText()).toContain("1.9.0");
  });
});
