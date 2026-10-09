/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { DialogProvider } from "../components/Dialog";
import { ProjectEditor } from "./Projects";
import { api } from "../lib/api";
import type { Project } from "../lib/types";

// The editor opens a project's secrets modal in place — before this shortcut
// the only route was to close the editor and use the Projects list.

vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      projectProfiles: vi.fn().mockResolvedValue({ profiles: [] }),
      projectFiles: vi.fn().mockResolvedValue([]),
      listProjectSecrets: vi.fn().mockResolvedValue([]),
      projectDownloadUrl: () => "/dl",
      projectFileDownloadUrl: () => "/dl",
      savePrefs: () => Promise.resolve(),
    },
  };
});

const project = {
  id: 7, name: "shop", slug: "shop", composeFile: "compose.yml", hostId: 0, allowRemoteHostPaths: false,
  lastDeployedProfiles: [],
} as unknown as Project;

let container: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root.render(
      <MemoryRouter>
        <DialogProvider>
          <ProjectEditor project={project} composeAvailable deployed={false} onClose={() => {}} onOutput={() => {}} />
        </DialogProvider>
      </MemoryRouter>,
    );
  });
});
afterEach(() => { act(() => root.unmount()); container.remove(); vi.clearAllMocks(); });

describe("ProjectEditor secrets shortcut", () => {
  it("has a Secrets button that opens the project's secrets modal, and it closes again", async () => {
    expect(api.listProjectSecrets).not.toHaveBeenCalled();
    const btn = container.querySelector('button[title="Secrets"]') as HTMLElement;
    expect(btn).not.toBeNull();
    await act(async () => btn.click());
    expect(api.listProjectSecrets).toHaveBeenCalledWith(7);

    // The modal renders above the editor (z-[55] over z-50) — and the editor stays mounted below it.
    const modal = [...container.querySelectorAll("div")].find((d) => d.className.includes("z-[55]"));
    expect(modal).toBeDefined();
    expect(container.textContent).toContain("shop");

    const close = [...modal!.querySelectorAll("button")].find((b) => b.querySelector("svg.lucide-x"));
    await act(async () => (close as HTMLElement).click());
    expect([...container.querySelectorAll("div")].some((d) => d.className.includes("z-[55]"))).toBe(false);
  });
});
