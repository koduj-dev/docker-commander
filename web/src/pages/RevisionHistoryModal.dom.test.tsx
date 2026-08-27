/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { RevisionHistoryModal } from "./Projects";
import { DialogProvider } from "../components/Dialog";
import type { Project, ProjectRevision, DeployPreview } from "../lib/types";

// Deployment revisions (NEXT.md's "Deployment revisions and rollback"): each
// entry is immutable, diffable against what's running now, and restorable —
// these tests check the list renders what a revision actually carries
// (author/profiles/reason/images), that Restore goes through a confirm
// dialog before calling the API (a destructive-ish action per this app's own
// convention), and that Diff opens a read-only comparison.

const listRevisions = vi.hoisted(() => vi.fn());
const diffRevision = vi.hoisted(() => vi.fn());
const restoreRevision = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return { ...actual, api: { ...actual.api, listRevisions, diffRevision, restoreRevision } };
});

const project = { id: 1, name: "app", slug: "app" } as Project;

const rev1: ProjectRevision = {
  id: 1, projectId: 1, revision: 1, hostId: 0, profiles: ["extra"],
  images: [{ service: "web", image: "nginx:1.25", digest: "sha256:abc123" }],
  valid: true, author: "alice", reason: "initial rollout", createdAt: "2026-01-01T00:00:00Z",
};
const rev2: ProjectRevision = {
  id: 2, projectId: 1, revision: 2, hostId: 0, profiles: [],
  images: [], valid: false, validationError: "bad indentation",
  author: "bob", createdAt: "2026-01-02T00:00:00Z",
};

let container: HTMLDivElement;
let root: Root | undefined;

async function render() {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <DialogProvider>
        <RevisionHistoryModal project={project} onClose={() => {}} onOutput={() => {}} />
      </DialogProvider>,
    );
  });
  await act(async () => {}); // let listRevisions resolve
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  listRevisions.mockReset().mockResolvedValue([rev2, rev1]); // newest first, as the API returns it
  diffRevision.mockReset();
  restoreRevision.mockReset().mockResolvedValue({ ok: true, output: "restored" });
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container?.remove();
});

function revisionBlock(label: string): HTMLElement {
  const el = [...container.querySelectorAll("span")].find((s) => s.textContent === label);
  if (!el) throw new Error(`no block for ${label}`);
  return el.closest("div")!.parentElement as HTMLElement;
}

describe("RevisionHistoryModal", () => {
  it("lists every revision with its author, profiles, reason and images", async () => {
    await render();
    const block = revisionBlock("Revision 1");
    expect(block.textContent).toContain("alice");
    expect(block.textContent).toContain("extra");
    expect(block.textContent).toContain("initial rollout");
    expect(block.textContent).toContain("web: nginx:1.25");
  });

  it("flags an invalid revision and shows 'no profiles' when none were used", async () => {
    await render();
    const block = revisionBlock("Revision 2");
    expect(block.textContent).toContain("invalid");
    expect(block.textContent).toContain("no profiles");
  });

  it("Diff fetches and opens a read-only comparison against current", async () => {
    const diff: DeployPreview = {
      valid: true, unchanged: 0,
      changes: [{ service: "web", kind: "env", existing: true, recreates: true, ignored: false }],
    };
    diffRevision.mockResolvedValue(diff);
    await render();

    const diffBtn = revisionBlock("Revision 1").querySelector("button") as HTMLButtonElement;
    await act(async () => diffBtn.click());

    expect(diffRevision).toHaveBeenCalledWith(1, 1, "current");
    expect(container.textContent).toContain("Revision 1 vs current");
    // Read-only: no Ignore control on the nested diff view.
    expect([...container.querySelectorAll("button")].some((b) => /^(Ignore|Unignore)$/.test(b.textContent ?? ""))).toBe(false);
  });

  it("Restore asks for confirmation before calling the API", async () => {
    await render();
    const restoreBtn = [...revisionBlock("Revision 1").querySelectorAll("button")].find((b) => b.textContent?.includes("Restore"))!;
    await act(async () => restoreBtn.click());

    expect(restoreRevision).not.toHaveBeenCalled();
    expect(container.textContent).toContain("Restore revision 1?");

    // The row's own button renders an icon + " Restore" (leading space); the
    // confirm dialog's button is plain confirmLabel text, so an EXACT
    // (un-trimmed) match uniquely identifies it.
    const confirm = [...container.querySelectorAll("button")].find((b) => b.textContent === "Restore");
    await act(async () => confirm!.click());

    expect(restoreRevision).toHaveBeenCalledWith(1, 1);
  });

  it("cancelling the confirm dialog never calls restore", async () => {
    await render();
    const restoreBtn = [...revisionBlock("Revision 1").querySelectorAll("button")].find((b) => b.textContent?.includes("Restore"))!;
    await act(async () => restoreBtn.click());

    const cancelBtn = [...container.querySelectorAll("button")].find((b) => b.textContent === "Cancel");
    await act(async () => cancelBtn!.click());

    expect(restoreRevision).not.toHaveBeenCalled();
  });
});
