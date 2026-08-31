/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { Recovery } from "./Recovery";
import { DialogProvider } from "../components/Dialog";
import type { Host } from "../lib/types";

// Portable recovery bundle page: export streams a file with whatever
// options/passphrase were chosen; import always inspects (read-only) before
// offering to actually import, and the confirm dialog gate matches this
// app's own convention for a "creates things" action.

const hosts = vi.hoisted(() => vi.fn());
const exportRecoveryBundle = vi.hoisted(() => vi.fn());
const inspectRecoveryBundle = vi.hoisted(() => vi.fn());
const importRecoveryBundle = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return { ...actual, api: { ...actual.api, hosts, exportRecoveryBundle, inspectRecoveryBundle, importRecoveryBundle } };
});

const remoteHost: Host = { id: 3, name: "prod", kind: "tcp", address: "1.2.3.4:2376" };

let container: HTMLDivElement;
let root: Root | undefined;

async function render() {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter>
        <DialogProvider>
          <Recovery />
        </DialogProvider>
      </MemoryRouter>,
    );
  });
  await act(async () => {}); // let hosts() resolve
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  hosts.mockReset().mockResolvedValue([remoteHost]);
  exportRecoveryBundle.mockReset().mockResolvedValue(new Blob(["bundle"]));
  inspectRecoveryBundle.mockReset();
  importRecoveryBundle.mockReset();
  // downloadBlob touches these; happy-dom doesn't implement them.
  (globalThis.URL as unknown as { createObjectURL: () => string }).createObjectURL = () => "blob:mock";
  (globalThis.URL as unknown as { revokeObjectURL: () => void }).revokeObjectURL = () => {};
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container?.remove();
});

function byText(tag: string, text: string): HTMLElement {
  const el = [...container.querySelectorAll(tag)].find((e) => e.textContent?.trim() === text);
  if (!el) throw new Error(`no <${tag}> with text ${text}`);
  return el as HTMLElement;
}

function typeInto(el: Element, value: string) {
  const input = el as HTMLInputElement;
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")?.set;
  setter?.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

describe("Recovery export", () => {
  it("exports with the chosen includeSecrets and passphrase", async () => {
    await render();
    const checkbox = container.querySelector('input[type="checkbox"]') as HTMLInputElement;
    await act(async () => checkbox.click());
    const passInput = container.querySelectorAll('input[type="password"]')[0];
    await act(async () => typeInto(passInput, "hunter2"));

    const exportBtn = byText("button", "Export bundle");
    await act(async () => exportBtn.click());

    expect(exportRecoveryBundle).toHaveBeenCalledWith({ includeSecrets: true }, "hunter2");
  });
});

describe("Recovery import", () => {
  function fileInput(): HTMLInputElement {
    return container.querySelector('input[type="file"]') as HTMLInputElement;
  }

  async function selectAFile() {
    const input = fileInput();
    const file = new File(["zip-bytes"], "backup.dcbundle");
    Object.defineProperty(input, "files", { value: [file], configurable: true });
    await act(async () => input.dispatchEvent(new Event("change", { bubbles: true })));
  }

  it("Import is disabled until a compatibility check has run", async () => {
    await render();
    await selectAFile();
    const importBtn = byText("button", "Import");
    expect(importBtn.hasAttribute("disabled")).toBe(true);
  });

  it("Check compatibility calls inspect, then renders the report", async () => {
    inspectRecoveryBundle.mockResolvedValue({
      manifest: { version: 1, exportedAt: "2026-01-01T00:00:00Z", exportedBy: "admin", includesSecrets: false, hosts: 1, registries: 0, alertRules: 0, projects: 1 },
      compatibility: { missingImages: ["nginx:1.27"], missingVolumes: [], unknownHosts: [], secretsExcluded: true, warnings: [] },
    });
    await render();
    await selectAFile();
    await act(async () => byText("button", "Check compatibility").click());

    expect(inspectRecoveryBundle).toHaveBeenCalled();
    expect(container.textContent).toContain("nginx:1.27");
    const importBtn = byText("button", "Import");
    expect(importBtn.hasAttribute("disabled")).toBe(false);
  });

  it("Import asks for confirmation, then calls the API and shows a summary", async () => {
    inspectRecoveryBundle.mockResolvedValue({
      manifest: { version: 1, exportedAt: "2026-01-01T00:00:00Z", exportedBy: "", includesSecrets: false, hosts: 0, registries: 0, alertRules: 0, projects: 1 },
      compatibility: { missingImages: [], missingVolumes: [], unknownHosts: [], secretsExcluded: true, warnings: [] },
    });
    importRecoveryBundle.mockResolvedValue({
      summary: { hostsCreated: 0, registriesCreated: 0, webhooksCreated: 0, alertRulesCreated: 0, projectsCreated: 1 },
      warnings: ["project \"old-name\" already exists, skipped"],
    });
    await render();
    await selectAFile();
    await act(async () => byText("button", "Check compatibility").click());
    await act(async () => byText("button", "Import").click());

    expect(importRecoveryBundle).not.toHaveBeenCalled();
    expect(container.textContent).toContain("Import recovery bundle?");

    const confirm = [...container.querySelectorAll("button")].find((b) => b.textContent === "Yes, import");
    await act(async () => confirm!.click());

    expect(importRecoveryBundle).toHaveBeenCalled();
    expect(container.textContent).toContain("Created 1 project(s)");
    expect(container.textContent).toContain("already exists, skipped");
  });

  it("cancelling the confirm dialog never calls import", async () => {
    inspectRecoveryBundle.mockResolvedValue({
      manifest: { version: 1, exportedAt: "2026-01-01T00:00:00Z", exportedBy: "", includesSecrets: false, hosts: 0, registries: 0, alertRules: 0, projects: 1 },
      compatibility: { missingImages: [], missingVolumes: [], unknownHosts: [], secretsExcluded: true, warnings: [] },
    });
    await render();
    await selectAFile();
    await act(async () => byText("button", "Check compatibility").click());
    await act(async () => byText("button", "Import").click());

    const cancel = [...container.querySelectorAll("button")].find((b) => b.textContent === "Cancel");
    await act(async () => cancel!.click());

    expect(importRecoveryBundle).not.toHaveBeenCalled();
  });
});
