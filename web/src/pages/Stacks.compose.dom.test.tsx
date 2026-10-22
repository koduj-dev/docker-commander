/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { Stacks } from "./Stacks";
import { DialogProvider } from "../components/Dialog";
import { clearPrefs } from "../lib/prefs";
import type { Stack } from "../lib/types";

// The compose viewer has to make a read-only file obvious: a file the server
// can't write used to open in a plain editor whose Save could only fail. And a
// file that is only unwritable still redeploys, since compose up just reads it.

const stackCompose = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", () => ({
  api: {
    stacks: () => Promise.resolve([{ project: "web", configFile: "/home/u/apps/web/compose.yml", workingDir: "/home/u/apps/web", containers: [], running: 0 }] as Stack[]),
    projects: () => Promise.resolve({ projects: [] }),
    stackCompose,
    hosts: () => Promise.resolve([]),
    savePrefs: () => Promise.resolve({ ok: true }),
  },
}));
vi.mock("../lib/dockerEvents", () => ({ useDockerEventTick: () => 0 }));

let container: HTMLDivElement;
let root: Root;

const open = async (r: Record<string, unknown>) => {
  stackCompose.mockResolvedValue({ ok: true, path: "/home/u/apps/web/compose.yml", content: "services: {}\n", ...r });
  await act(async () => {
    root.render(<MemoryRouter><DialogProvider><Stacks /></DialogProvider></MemoryRouter>);
  });
  const btn = container.querySelector('button[title="View compose file"]') as HTMLButtonElement;
  await act(async () => btn.click());
};
const button = (label: string) => [...container.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  clearPrefs();
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(() => { act(() => root.unmount()); container.remove(); vi.clearAllMocks(); });

describe("Stacks compose viewer: read-only", () => {
  it("an unwritable file shows a read-only badge and banner with the reason, no editor, no Save, but Redeploy", async () => {
    await open({ editable: false, redeployable: true, readOnlyReason: 'Docker Commander runs as user "dockercmd" and can\'t reach it' });
    expect(container.textContent).toContain("Read-only");
    const banner = container.querySelector('[role="status"]');
    expect(banner?.textContent).toContain("can't be saved from here");
    expect(banner?.textContent).toContain('runs as user "dockercmd"');
    expect(container.querySelector("textarea")).toBeNull();
    expect(button("Save")).toBeUndefined();
    expect(button("Redeploy")).toBeDefined();
  });

  it("a file nothing may act on offers no Redeploy either", async () => {
    await open({ editable: false, redeployable: false, readOnlyReason: "a tcp connection exposes no filesystem" });
    expect(container.querySelector('[role="status"]')?.textContent).toContain("exposes no filesystem");
    expect(button("Redeploy")).toBeUndefined();
  });

  it("an editable file has an editor and Save, and no read-only marking", async () => {
    await open({ editable: true, redeployable: true });
    expect(container.querySelector("textarea")).not.toBeNull();
    expect(button("Save")).toBeDefined();
    expect(container.querySelector('[role="status"]')).toBeNull();
    expect(container.textContent).not.toContain("Read-only");
  });
});
