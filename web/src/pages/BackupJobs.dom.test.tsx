/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { BackupJobs } from "./BackupJobs";
import { DialogProvider } from "../components/Dialog";
import type { BackupJob } from "../lib/types";

const backupJobs = vi.hoisted(() => vi.fn());
const updateBackupJob = vi.hoisted(() => vi.fn());
const createBackupJob = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", () => ({
  api: {
    backupJobs,
    updateBackupJob,
    createBackupJob,
    toggleBackupJob: vi.fn(),
    deleteBackupJob: vi.fn(),
    runBackupJob: vi.fn(),
    backupJobRuns: vi.fn(),
    hosts: () => Promise.resolve([]),
    projects: () => Promise.resolve({ projects: [] }),
  },
}));

const existingJob: BackupJob = {
  id: 1, name: "nightly", enabled: true, scope: "volume", volumeName: "data",
  projectId: 0, hostId: 0, image: "restic/restic", command: "restic backup /data",
  intervalMinutes: 60, createdBy: "admin", createdAt: "", updatedAt: "",
  lastRunAt: null, lastRunOk: false, lastRunDetail: "",
};

let container: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  backupJobs.mockResolvedValue([existingJob]);
  updateBackupJob.mockResolvedValue({ ok: true });
  createBackupJob.mockResolvedValue({ id: 2 });

  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root.render(
      <MemoryRouter>
        <DialogProvider>
          <BackupJobs />
        </DialogProvider>
      </MemoryRouter>,
    );
  });
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

function editButton(): HTMLElement {
  const el = [...container.querySelectorAll("button")].find((b) => b.title === "Edit");
  if (!el) throw new Error("edit button not found");
  return el;
}

function saveButton(): HTMLElement {
  const el = [...container.querySelectorAll("button")].find((b) => b.textContent?.includes("Save changes"));
  if (!el) throw new Error("save button not found");
  return el;
}

function typeInto(el: Element, value: string) {
  const input = el as HTMLTextAreaElement;
  const setter = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, "value")?.set;
  setter?.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

function clearEnvCheckbox(): HTMLInputElement {
  const el = [...container.querySelectorAll("input[type=checkbox]")].find((i) =>
    i.closest("label")?.textContent?.includes("Clear stored environment"),
  ) as HTMLInputElement | undefined;
  if (!el) throw new Error("clear-env checkbox not found");
  return el;
}

describe("BackupJobs — clear stored environment", () => {
  it("does not offer a clear-env checkbox when creating a new job", async () => {
    await act(async () => {
      [...container.querySelectorAll("button")].find((b) => b.textContent?.includes("New job"))?.click();
    });
    expect(container.textContent).not.toContain("Clear stored environment");
  });

  it("an ordinary save (checkbox untouched) sends no clearEnv and the typed env", async () => {
    await act(async () => { editButton().click(); });

    const textarea = container.querySelector("textarea") as HTMLTextAreaElement;
    await act(async () => { typeInto(textarea, "RESTIC_PASSWORD=rotated"); });
    await act(async () => { saveButton().click(); });

    expect(updateBackupJob).toHaveBeenCalledTimes(1);
    const [, body] = updateBackupJob.mock.calls[0];
    expect(body.clearEnv).toBe(false);
    expect(body.env).toEqual({ RESTIC_PASSWORD: "rotated" });
  });

  it("checking 'clear stored environment' sends clearEnv:true and no env, ignoring the textarea", async () => {
    await act(async () => { editButton().click(); });

    const textarea = container.querySelector("textarea") as HTMLTextAreaElement;
    await act(async () => { typeInto(textarea, "SHOULD_BE_IGNORED=1"); });
    await act(async () => { clearEnvCheckbox().click(); });
    // Checking the box disables further edits to the textarea.
    expect(textarea.disabled).toBe(true);

    await act(async () => { saveButton().click(); });

    expect(updateBackupJob).toHaveBeenCalledTimes(1);
    const [, body] = updateBackupJob.mock.calls[0];
    expect(body.clearEnv).toBe(true);
    expect(body.env).toBeUndefined();
  });
});
