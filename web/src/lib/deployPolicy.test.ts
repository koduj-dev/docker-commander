import { describe, it, expect, vi, beforeEach } from "vitest";
import { deployProjectWithPolicyGate } from "./deployPolicy";

const deployProject = vi.hoisted(() => vi.fn());
vi.mock("./api", () => ({ api: { deployProject } }));

const confirm = vi.fn();
const alert = vi.fn();
const dialogs = { confirm, alert };

beforeEach(() => {
  deployProject.mockReset();
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
