/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { BackupJobs } from "./BackupJobs";
import { DialogProvider } from "../components/Dialog";
import { api } from "../lib/api";
import type { BackupJob, BackupRun } from "../lib/types";

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

const failedRun: BackupRun = {
  id: 9, jobId: 1, startedAt: "2026-09-25T10:00:00Z", finishedAt: "2026-09-25T10:00:03Z", ok: false, exitCode: 2,
  output: "Fatal: unable to open repository at /repo: permission denied", error: "exit status 2", triggeredBy: "admin",
};
const olderRun: BackupRun = { ...failedRun, id: 8, ok: true, exitCode: 0, output: "snapshot abc saved", error: "", triggeredBy: "schedule", startedAt: "2026-09-24T10:00:00Z", finishedAt: "2026-09-24T10:01:00Z" };

// A failed job used to show only a "failed" badge with a hover tooltip — the
// captured output was stored (and served by /runs) but nothing in the UI read it.
describe("BackupJobs — run history and logs", () => {
  const historyButton = () => [...container.querySelectorAll("button")].find((b) => b.title === "Run history & logs") as HTMLElement;

  it("opens the run history from the History button, newest run expanded with its output and error", async () => {
    vi.mocked(api.backupJobRuns).mockResolvedValue([failedRun, olderRun]);
    await act(async () => historyButton().click());
    expect(api.backupJobRuns).toHaveBeenCalledWith(1);
    const text = container.textContent ?? "";
    expect(text).toContain("Run history — nightly");
    expect(text).toContain("permission denied"); // the newest (failed) run is open
    expect(text).toContain("exit status 2");
    expect(text).not.toContain("snapshot abc saved"); // older run stays collapsed until clicked
    const older = [...container.querySelectorAll("button[aria-expanded]")].find((b) => b.textContent?.includes("scheduled")) as HTMLElement;
    await act(async () => older.click());
    expect(container.textContent).toContain("snapshot abc saved");
  });

  it("the failed badge opens the same history", async () => {
    backupJobs.mockResolvedValue([{ ...existingJob, lastRunAt: "2026-09-25T10:00:00Z", lastRunOk: false, lastRunDetail: "exit status 2" }]);
    act(() => root.unmount());
    root = createRoot(container);
    await act(async () => { root.render(<MemoryRouter><DialogProvider><BackupJobs /></DialogProvider></MemoryRouter>); });
    vi.mocked(api.backupJobRuns).mockResolvedValue([failedRun]);
    const badge = [...container.querySelectorAll("button")].find((b) => b.textContent === "failed") as HTMLElement;
    await act(async () => badge.click());
    expect(container.textContent).toContain("permission denied");
  });

  it("a Run-now that fails opens the log instead of vanishing", async () => {
    vi.mocked(api.runBackupJob).mockRejectedValue(new Error("backup failed"));
    vi.mocked(api.backupJobRuns).mockResolvedValue([failedRun]);
    const run = [...container.querySelectorAll("button")].find((b) => b.title === "Run now") as HTMLElement;
    await act(async () => run.click());
    expect(container.textContent).toContain("Run history — nightly");
    expect(container.textContent).toContain("permission denied");
    expect(container.textContent).toContain("backup failed"); // the server's own error is shown too
  });

  it("says so when a job has never run", async () => {
    vi.mocked(api.backupJobRuns).mockResolvedValue([]);
    await act(async () => historyButton().click());
    expect(container.textContent).toContain("No runs yet");
  });
});
