/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { Shell } from "./Shell";
import { DialogProvider } from "../components/Dialog";
import { setGroupCollapsed } from "../lib/sidebarGroups";

// The sidebar groups (Workloads, Storage, Network, Observability, System) used
// to always render open. This covers the fold/unfold toggle, that it persists
// (lib/sidebarGroups, backed by localStorage — see its own test for the
// reload-from-scratch path), and that a collapsed group still surfaces the
// active route rather than hiding where you are.

vi.mock("../lib/api", () => ({
  api: {
    hosts: () => Promise.resolve([]),
    version: () => Promise.resolve({ version: "test" }),
    alerts: () => Promise.resolve({ events: [], unread: 0 }),
    updateStatus: () => Promise.resolve({ updateAvailable: false }),
  },
}));

vi.mock("../auth/AuthContext", () => ({
  useAuth: () => ({
    user: {
      id: 1,
      username: "admin",
      role: "admin",
      sections: ["dashboard", "containers", "projects", "images", "volumes", "networks", "topology", "logs", "events", "alerts", "hosts", "registries", "audit"],
    },
    logout: () => Promise.resolve(),
  }),
}));

let container: HTMLDivElement;
let root: Root | undefined;

// Every collapse test starts from a known-clean slate: nothing collapsed. The
// module caches state across the whole test file (same singleton lib/host.ts
// uses), so this can't just be a beforeEach localStorage.clear() — that clears
// the storage but not the already-loaded in-memory copy.
function resetGroups() {
  for (const title of ["Workloads", "Storage", "Network", "Observability", "System"]) {
    setGroupCollapsed(title, false);
  }
  localStorage.clear();
}

function groupButton(title: string): HTMLButtonElement {
  const el = [...container.querySelectorAll("button")].find((b) => b.textContent?.trim() === title);
  if (!el) throw new Error(`no group header button for ${title}`);
  return el as HTMLButtonElement;
}

function hasLink(label: string): boolean {
  return [...container.querySelectorAll("a")].some((a) => a.textContent?.trim() === label);
}

function linkFor(label: string): HTMLAnchorElement {
  const el = [...container.querySelectorAll("a")].find((a) => a.textContent?.trim() === label);
  if (!el) throw new Error(`no nav link for ${label}`);
  return el as HTMLAnchorElement;
}

async function renderShell(initialEntry = "/") {
  await act(async () => {
    root!.render(
      <MemoryRouter initialEntries={[initialEntry]}>
        <DialogProvider>
          <Shell>
            <div />
          </Shell>
        </DialogProvider>
      </MemoryRouter>,
    );
  });
}

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  resetGroups();
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container.remove();
  resetGroups();
  vi.clearAllMocks();
});

describe("sidebar group folding", () => {
  it("renders each group title as a real button, not a bare div", async () => {
    await renderShell();
    const btn = groupButton("Storage");
    expect(btn.tagName).toBe("BUTTON");
    expect(btn.getAttribute("type")).toBe("button");
    expect(btn.getAttribute("aria-expanded")).toBe("true");
  });

  it("hides a group's items when its header is clicked, and shows them again on a second click", async () => {
    await renderShell();
    expect(hasLink("Images")).toBe(true);
    expect(hasLink("Volumes")).toBe(true);

    await act(async () => groupButton("Storage").click());
    expect(hasLink("Images")).toBe(false);
    expect(hasLink("Volumes")).toBe(false);
    expect(groupButton("Storage").getAttribute("aria-expanded")).toBe("false");

    await act(async () => groupButton("Storage").click());
    expect(hasLink("Images")).toBe(true);
    expect(hasLink("Volumes")).toBe(true);
    expect(groupButton("Storage").getAttribute("aria-expanded")).toBe("true");
  });

  it("leaves other groups and active-route highlighting alone", async () => {
    await renderShell("/containers");
    await act(async () => groupButton("Storage").click());

    expect(hasLink("Images")).toBe(false);
    // Workloads (containing the active route) is untouched.
    const containersLink = linkFor("Containers");
    expect(containersLink.className).toContain("bg-accent/15");
  });

  it("auto-expands a collapsed group that holds the active route", async () => {
    setGroupCollapsed("Storage", true);
    await renderShell("/volumes");

    expect(hasLink("Volumes")).toBe(true);
    expect(groupButton("Storage").getAttribute("aria-expanded")).toBe("true");
  });

  it("keeps a collapsed group collapsed when the active route is elsewhere", async () => {
    setGroupCollapsed("Storage", true);
    await renderShell("/containers");

    expect(hasLink("Volumes")).toBe(false);
    expect(groupButton("Storage").getAttribute("aria-expanded")).toBe("false");
  });

  it("persists the collapsed state across a remount", async () => {
    await renderShell();
    await act(async () => groupButton("Network").click());
    expect(hasLink("Networks")).toBe(false);

    await act(async () => root!.unmount());
    root = createRoot(container);
    await renderShell();

    expect(hasLink("Networks")).toBe(false);
    expect(groupButton("Network").getAttribute("aria-expanded")).toBe("false");
  });
});
