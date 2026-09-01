import { api } from "./api";
import type { PolicyViolation } from "./types";
import type { ComposeRunResult } from "./composeOutput";

function formatViolations(vs: PolicyViolation[]): string {
  return vs.map((v) => `${v.rule} on "${v.service}": ${v.detail}`).join("\n");
}

/** The subset of useDialogs() this needs — kept minimal so callers don't have
 * to import the Dialog module's internal type. */
type DialogGate = {
  confirm: (o: { title: string; message: string; confirmLabel?: string; danger?: boolean }) => Promise<boolean>;
  alert: (o: { title: string; message: string }) => Promise<void>;
};

/**
 * Wraps api.deployProject with the deploy-time policy check's confirmation
 * flow, so both places that can trigger a deploy (the project list and the
 * project editor) handle the response identically:
 * - a warn-mode violation asks the operator to confirm before proceeding,
 *   then resubmits with confirmPolicyWarnings: true;
 * - a block-mode violation has no per-deploy override — it's surfaced as a
 *   plain acknowledgement naming the rule(s), pointing at Policy rules in
 *   Settings as the only way past it.
 */
export async function deployProjectWithPolicyGate(
  id: number,
  profiles: string[],
  dialogs: DialogGate,
  opts?: { pull?: boolean },
): Promise<ComposeRunResult & { ok: boolean }> {
  const r = await api.deployProject(id, profiles, opts);
  if (r.needsConfirmation && r.policy?.warnings?.length) {
    const proceed = await dialogs.confirm({
      title: "Deploy has policy warnings",
      message: `This deploy triggers the following policy rule(s):\n\n${formatViolations(r.policy.warnings)}\n\nDeploy anyway?`,
      confirmLabel: "Deploy anyway",
      danger: true,
    });
    if (!proceed) {
      return { ok: false, error: "Deploy cancelled — policy warning(s) not confirmed." };
    }
    return api.deployProject(id, profiles, { ...opts, confirmPolicyWarnings: true });
  }
  if (!r.ok && r.policy?.blocked?.length) {
    await dialogs.alert({
      title: "Deploy blocked by policy",
      message: `This deploy is blocked by policy rule(s) with no per-deploy override:\n\n${formatViolations(r.policy.blocked)}\n\nAn admin can change a rule's mode under Policy rules.`,
    });
    return { ok: false, error: "Deploy blocked by policy — see Policy rules in Settings." };
  }
  return r;
}
