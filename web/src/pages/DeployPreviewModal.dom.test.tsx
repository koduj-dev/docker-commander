/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { act, type ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { DeployPreviewModal } from "./Projects";
import type { DeployPreview } from "../lib/types";

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
    render(<DeployPreviewModal preview={preview} projectName="app" onClose={() => {}} />);
    expect(container.textContent).toContain("bad indentation");
  });

  it("reads as reassurance, not an empty list, when nothing would change", () => {
    const preview: DeployPreview = { valid: true, changes: [], unchanged: 3 };
    render(<DeployPreviewModal preview={preview} projectName="app" onClose={() => {}} />);
    expect(container.textContent).toContain("Nothing would change");
    expect(container.textContent).toContain("3");
  });

  it("an 'added' service never claims it recreates anything — it doesn't exist yet", () => {
    const preview: DeployPreview = {
      valid: true, unchanged: 0,
      changes: [{ service: "cache", kind: "added", to: "redis:7", detail: "not running; a deploy would create it", existing: false, recreates: false }],
    };
    render(<DeployPreviewModal preview={preview} projectName="app" onClose={() => {}} />);
    expect(container.textContent).toContain("cache");
    expect(container.textContent).not.toContain("recreates");
  });

  it("a field-level change (env/ports/etc.) shows the recreates warning", () => {
    const preview: DeployPreview = {
      valid: true, unchanged: 0,
      changes: [{ service: "web", kind: "env", detail: "1 missing (FOO)", existing: true, recreates: true }],
    };
    render(<DeployPreviewModal preview={preview} projectName="app" onClose={() => {}} />);
    expect(container.textContent).toContain("recreates");
    expect(container.textContent).toContain("1 missing (FOO)");
  });

  it("an orphaned (removed) service never shows recreates — it's left running, untouched", () => {
    const preview: DeployPreview = {
      valid: true, unchanged: 0,
      changes: [{ service: "old", kind: "removed", from: "old:1", detail: "running but no longer in the compose file", existing: true, recreates: false }],
    };
    render(<DeployPreviewModal preview={preview} projectName="app" onClose={() => {}} />);
    expect(container.textContent).toContain("old");
    expect(container.textContent).not.toContain("recreates");
  });

  it("shows from → to for a digest drift change", () => {
    const preview: DeployPreview = {
      valid: true, unchanged: 0,
      changes: [{ service: "web", kind: "digest", from: "sha256:aaaa", to: "sha256:bbbb", existing: true, recreates: true }],
    };
    render(<DeployPreviewModal preview={preview} projectName="app" onClose={() => {}} />);
    expect(container.textContent).toContain("sha256:aaaa");
    expect(container.textContent).toContain("sha256:bbbb");
  });
});
