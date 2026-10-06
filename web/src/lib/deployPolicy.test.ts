import { describe, it, expect, vi, beforeEach } from "vitest";
import { deployProjectWithPolicyGate, restoreRevisionWithPolicyGate } from "./deployPolicy";

const deployProject = vi.hoisted(() => vi.fn());
const restoreRevision = vi.hoisted(() => vi.fn());
vi.mock("./api", () => ({ api: { deployProject, restoreRevision } }));

const confirm = vi.fn();
const alert = vi.fn();
const dialogs = { confirm, alert };

beforeEach(() => {
  deployProject.mockReset();
  restoreRevision.mockReset();
  confirm.mockReset();
  alert.mockReset();
});

describe("deployProjectWithPolicyGate", () => {
  it("passes a plain success straight through with no dialog", async () => {
    deployProject.mockResolvedValue({ ok: true, output: "done" });
    const r = await deployProjectWithPolicyGate(1, [], dialogs);
    expect(r).toEqual({ ok: true, output: "done" });
    expect(confirm).not.toHaveBeenCalled();
    expect(alert).not.toHaveBeenCalled();
    expect(deployProject).toHaveBeenCalledTimes(1);
  });

  it("on a warn-mode violation, confirming resubmits with confirmPolicyWarnings: true", async () => {
    deployProject
      .mockResolvedValueOnce({ ok: false, needsConfirmation: true, policy: { warnings: [{ rule: "latest_tag", service: "web", mode: "warn", detail: "unpinned" }] } })
      .mockResolvedValueOnce({ ok: true, output: "deployed" });
    confirm.mockResolvedValue(true);

    const r = await deployProjectWithPolicyGate(1, ["p"], dialogs, { pull: true });

    expect(confirm).toHaveBeenCalledTimes(1);
    expect(confirm.mock.calls[0][0].message).toContain("latest_tag");
    expect(deployProject).toHaveBeenNthCalledWith(2, 1, ["p"], { pull: true, confirmPolicyWarnings: true });
    expect(r).toEqual({ ok: true, output: "deployed" });
  });

  it("on a warn-mode violation, declining does not resubmit", async () => {
    deployProject.mockResolvedValueOnce({
      ok: false, needsConfirmation: true,
      policy: { warnings: [{ rule: "missing_healthcheck", service: "web", mode: "warn", detail: "no healthcheck" }] },
    });
    confirm.mockResolvedValue(false);

    const r = await deployProjectWithPolicyGate(1, [], dialogs);

    expect(deployProject).toHaveBeenCalledTimes(1);
    expect(r.ok).toBe(false);
    expect(r.error).toMatch(/not confirmed/i);
  });

  it("on a block-mode violation, alerts and never resubmits", async () => {
    deployProject.mockResolvedValue({
      ok: false,
      policy: { blocked: [{ rule: "privileged", service: "web", mode: "block", detail: "privileged" }] },
    });

    const r = await deployProjectWithPolicyGate(1, [], dialogs);

    expect(alert).toHaveBeenCalledTimes(1);
    expect(alert.mock.calls[0][0].message).toContain("privileged");
    expect(confirm).not.toHaveBeenCalled();
    expect(deployProject).toHaveBeenCalledTimes(1);
    expect(r.ok).toBe(false);
  });
});

// A restore redeploys, so it passes the same policy check. Its dialog used to
// call the API once and show the refusal: a warn-mode rule could never be
// confirmed, so such a restore was impossible from the UI.
describe("restoreRevisionWithPolicyGate", () => {
  const warned = { ok: false, needsConfirmation: true, policy: { warnings: [{ rule: "latest_tag", service: "web", mode: "warn", detail: "unpinned" }] } };

  it("asks first without confirming anything", async () => {
    restoreRevision.mockResolvedValue({ ok: true, output: "restored" });
    const r = await restoreRevisionWithPolicyGate(3, 7, dialogs);
    expect(restoreRevision).toHaveBeenCalledWith(3, 7, undefined, false);
    expect(confirm).not.toHaveBeenCalled();
    expect(r.ok).toBe(true);
  });

  it("on a warn-mode violation, confirming restores with the warnings confirmed", async () => {
    restoreRevision.mockResolvedValueOnce(warned).mockResolvedValueOnce({ ok: true, output: "restored" });
    confirm.mockResolvedValue(true);

    const r = await restoreRevisionWithPolicyGate(3, 7, dialogs);

    expect(confirm.mock.calls[0][0].title).toBe("Restore has policy warnings");
    expect(restoreRevision).toHaveBeenNthCalledWith(2, 3, 7, undefined, true);
    expect(r).toEqual({ ok: true, output: "restored" });
  });

  it("on a warn-mode violation, declining restores nothing", async () => {
    restoreRevision.mockResolvedValueOnce(warned);
    confirm.mockResolvedValue(false);

    const r = await restoreRevisionWithPolicyGate(3, 7, dialogs);

    expect(restoreRevision).toHaveBeenCalledTimes(1);
    expect(r.ok).toBe(false);
    expect(r.error).toMatch(/Restore cancelled/);
  });

  it("on a block-mode violation, alerts and never retries", async () => {
    restoreRevision.mockResolvedValue({ ok: false, policy: { blocked: [{ rule: "privileged", service: "web", mode: "block", detail: "privileged" }] } });

    const r = await restoreRevisionWithPolicyGate(3, 7, dialogs);

    expect(alert).toHaveBeenCalledTimes(1);
    expect(restoreRevision).toHaveBeenCalledTimes(1);
    expect(r.error).toMatch(/Restore blocked/);
  });
});
