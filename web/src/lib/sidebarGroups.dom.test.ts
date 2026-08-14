/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, vi } from "vitest";

// The module caches collapsed state in a module-level Set, seeded once from
// localStorage at import time (same pattern as lib/host.ts). Each test that
// cares about "what a fresh page load sees" resets the module registry and
// re-imports, so the seed actually runs against whatever localStorage holds —
// exercising the same path a real reload takes, not just the in-memory cache.

// A fresh module instance per test: resetModules() drops the cached singleton
// so the re-import re-runs its top-level `read()` against whatever localStorage
// holds right now — the same thing a real page reload does. Without this, the
// module-level `collapsed` Set set up by test N leaks into test N+1.
async function freshModule() {
  vi.resetModules();
  return import("./sidebarGroups");
}

beforeEach(() => {
  localStorage.clear();
});

describe("sidebar group collapse persistence", () => {
  it("starts with nothing collapsed when localStorage is empty", async () => {
    const { getCollapsedGroups } = await freshModule();
    expect(getCollapsedGroups().size).toBe(0);
  });

  it("remembers a collapsed group after the module reloads (simulating a page reload)", async () => {
    const { setGroupCollapsed } = await freshModule();
    setGroupCollapsed("Storage", true);

    const fresh = await freshModule();
    expect(fresh.getCollapsedGroups().has("Storage")).toBe(true);
  });

  it("forgets a group once it's expanded again, and that sticks across a reload", async () => {
    const { setGroupCollapsed } = await freshModule();
    setGroupCollapsed("Storage", true);
    setGroupCollapsed("Network", true);
    setGroupCollapsed("Storage", false);

    const fresh = await freshModule();
    expect(fresh.getCollapsedGroups().has("Storage")).toBe(false);
    expect(fresh.getCollapsedGroups().has("Network")).toBe(true);
  });

  it("tracks multiple collapsed groups independently", async () => {
    const { setGroupCollapsed, getCollapsedGroups } = await freshModule();
    setGroupCollapsed("Storage", true);
    setGroupCollapsed("System", true);
    const collapsed = getCollapsedGroups();
    expect(collapsed.has("Storage")).toBe(true);
    expect(collapsed.has("System")).toBe(true);
    expect(collapsed.has("Network")).toBe(false);
  });

  it("survives corrupted localStorage content instead of throwing", async () => {
    localStorage.setItem("dc.sidebar.collapsed", "{not json");
    const { getCollapsedGroups } = await freshModule();
    expect(getCollapsedGroups().size).toBe(0);
  });
});
