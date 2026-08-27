/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act, type ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { DeployPreviewModal } from "./Projects";
import type { DeployPreview } from "../lib/types";

const ignoreDrift = vi.hoisted(() => vi.fn().mockResolvedValue({ ok: true }));
const unignoreDrift = vi.hoisted(() => vi.fn().mockResolvedValue({ ok: true }));

vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return { ...actual, api: { ...actual.api, ignoreDrift, unignoreDrift } };
});

// The preview modal is the first-class UI counterpart of the `preview_deploy`
// MCP tool and internal/docker/preview.go's DeployPreview — these tests check
// it renders what that data actually means: an invalid compose surfaces the
// error, "no changes" reads as reassurance rather than an empty list, and
// every change shows its downtime-risk ("recreates") state honestly.

let container: HTMLDivElement;
let root: Root | undefined;

function render(ui: ReactElement) {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  act(() => { root!.render(ui); });
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container?.remove();
});

describe("DeployPreviewModal", () => {
  it("shows the error for an invalid compose file, not a change list", () => {
    const preview: DeployPreview = { valid: false, error: "yaml: line 3: bad indentation" };
    render(<DeployPreviewModal preview={preview} projectId={1} projectName="app" onClose={() => {}} onChanged={() => {}} />);
    expect(container.textContent).toContain("bad indentation");
  });

  it("reads as reassurance, not an empty list, when nothing would change", () => {
    const preview: DeployPreview = { valid: true, changes: [], unchanged: 3 };
    render(<DeployPreviewModal preview={preview} projectId={1} projectName="app" onClose={() => {}} onChanged={() => {}} />);
    expect(container.textContent).toContain("Nothing would change");
    expect(container.textContent).toContain("3");
  });

  it("an 'added' service never claims it recreates anything — it doesn't exist yet", () => {
    const preview: DeployPreview = {
      valid: true, unchanged: 0,
      changes: [{ service: "cache", kind: "added", to: "redis:7", detail: "not running; a deploy would create it", existing: false, recreates: false, ignored: false }],
    };
    render(<DeployPreviewModal preview={preview} projectId={1} projectName="app" onClose={() => {}} onChanged={() => {}} />);
    expect(container.textContent).toContain("cache");
    expect(container.textContent).not.toContain("recreates");
  });

  it("a field-level change (env/ports/etc.) shows the recreates warning", () => {
    const preview: DeployPreview = {
      valid: true, unchanged: 0,
      changes: [{ service: "web", kind: "env", detail: "1 missing (FOO)", existing: true, recreates: true, ignored: false }],
    };
    render(<DeployPreviewModal preview={preview} projectId={1} projectName="app" onClose={() => {}} onChanged={() => {}} />);
    expect(container.textContent).toContain("recreates");
    expect(container.textContent).toContain("1 missing (FOO)");
  });

  it("an orphaned (removed) service never shows recreates — it's left running, untouched", () => {
    const preview: DeployPreview = {
      valid: true, unchanged: 0,
      changes: [{ service: "old", kind: "removed", from: "old:1", detail: "running but no longer in the compose file", existing: true, recreates: false, ignored: false }],
    };
    render(<DeployPreviewModal preview={preview} projectId={1} projectName="app" onClose={() => {}} onChanged={() => {}} />);
    expect(container.textContent).toContain("old");
    expect(container.textContent).not.toContain("recreates");
  });

  it("shows from → to for a digest drift change", () => {
    const preview: DeployPreview = {
      valid: true, unchanged: 0,
      changes: [{ service: "web", kind: "digest", from: "sha256:aaaa", to: "sha256:bbbb", existing: true, recreates: true, ignored: false }],
    };
    render(<DeployPreviewModal preview={preview} projectId={1} projectName="app" onClose={() => {}} onChanged={() => {}} />);
    expect(container.textContent).toContain("sha256:aaaa");
    expect(container.textContent).toContain("sha256:bbbb");
  });
});

// Drift-detection behaviour: a change can be reviewed/accepted ("Ignore")
// without disappearing, and "active" (not raw change count) is what the
// headline and the Reconcile button key off — an all-ignored preview has
// nothing left that needs reconciling.
describe("DeployPreviewModal — ignore / unignore / reconcile", () => {
  beforeEach(() => {
    ignoreDrift.mockClear();
    unignoreDrift.mockClear();
  });

  function findButton(text: string): HTMLButtonElement {
    const btn = [...container.querySelectorAll("button")].find((b) => b.textContent?.includes(text));
    if (!btn) throw new Error(`no button containing ${JSON.stringify(text)}`);
    return btn as HTMLButtonElement;
  }

  it("clicking Ignore calls the API with the change's service+kind and refetches", async () => {
    const onChanged = vi.fn();
    const preview: DeployPreview = {
      valid: true, unchanged: 0, active: 1,
      changes: [{ service: "web", kind: "env", detail: "1 missing (FOO)", existing: true, recreates: true, ignored: false }],
    };
    render(<DeployPreviewModal preview={preview} projectId={7} projectName="app" onClose={() => {}} onChanged={onChanged} />);
    await act(async () => findButton("Ignore").click());
    expect(ignoreDrift).toHaveBeenCalledWith(7, "web", "env");
    expect(onChanged).toHaveBeenCalled();
  });

  it("an ignored change shows an 'ignored' badge and an Unignore action instead", async () => {
    const preview: DeployPreview = {
      valid: true, unchanged: 0, active: 0,
      changes: [{ service: "web", kind: "restart", from: "no", to: "unless-stopped", existing: true, recreates: true, ignored: true }],
    };
    render(<DeployPreviewModal preview={preview} projectId={7} projectName="app" onClose={() => {}} onChanged={() => {}} />);
    expect(container.textContent).toContain("ignored");
    await act(async () => findButton("Unignore").click());
    expect(unignoreDrift).toHaveBeenCalledWith(7, "web", "restart");
  });

  it("the headline counts active changes, not raw ones, and calls out ignored separately", () => {
    const preview: DeployPreview = {
      valid: true, unchanged: 0, active: 1,
      changes: [
        { service: "web", kind: "env", existing: true, recreates: true, ignored: false },
        { service: "web", kind: "restart", existing: true, recreates: true, ignored: true },
      ],
    };
    render(<DeployPreviewModal preview={preview} projectId={1} projectName="app" onClose={() => {}} onChanged={() => {}} />);
    expect(container.textContent).toContain("1 active change");
    expect(container.textContent).toContain("1 ignored");
  });

  it("Reconcile is offered when there is active drift, and triggers onReconcile", async () => {
    const onReconcile = vi.fn();
    const preview: DeployPreview = {
      valid: true, unchanged: 0, active: 1,
      changes: [{ service: "web", kind: "env", existing: true, recreates: true, ignored: false }],
    };
    render(<DeployPreviewModal preview={preview} projectId={1} projectName="app" onClose={() => {}} onChanged={() => {}} onReconcile={onReconcile} />);
    await act(async () => findButton("Reconcile now").click());
    expect(onReconcile).toHaveBeenCalled();
  });

  it("Reconcile is NOT offered once every change is ignored — nothing active to fix", () => {
    const preview: DeployPreview = {
      valid: true, unchanged: 0, active: 0,
      changes: [{ service: "web", kind: "env", existing: true, recreates: true, ignored: true }],
    };
    render(<DeployPreviewModal preview={preview} projectId={1} projectName="app" onClose={() => {}} onChanged={() => {}} onReconcile={() => {}} />);
    expect(container.textContent).not.toContain("Reconcile now");
  });
});
