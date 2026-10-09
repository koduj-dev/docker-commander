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

type PolicyRefusal = {
  ok: boolean;
  error?: string;
  needsConfirmation?: boolean;
  policy?: { blocked?: PolicyViolation[]; warnings?: PolicyViolation[]; error?: string; code?: string };
};

/**
 * Runs an action that passes the deploy-time policy check (a deploy, or a
 * revision restore, which redeploys) and handles its answer the same way
 * everywhere:
 * - a warn-mode violation asks the operator to confirm, then runs the action
 *   again with the warnings confirmed;
 * - a block-mode violation has no per-action override. It is surfaced as a
 *   plain acknowledgement naming the rule(s), pointing at Policy rules in
 *   Settings as the only way past it.
 */
export async function runWithPolicyGate<R extends PolicyRefusal>(
  verb: "Deploy" | "Restore",
  run: (confirmPolicyWarnings: boolean) => Promise<R>,
  dialogs: DialogGate,
): Promise<R | { ok: false; error: string }> {
  const r = await run(false);
  if (r.needsConfirmation && r.policy?.warnings?.length) {
    const proceed = await dialogs.confirm({
      title: `${verb} has policy warnings`,
      message: `This ${verb.toLowerCase()} triggers the following policy rule(s):\n\n${formatViolations(r.policy.warnings)}\n\n${verb} anyway?`,
      confirmLabel: `${verb} anyway`,
      danger: true,
    });
    if (!proceed) {
      return { ok: false, error: `${verb} cancelled — policy warning(s) not confirmed.` };
    }
    return run(true);
  }
  if (!r.ok && r.policy?.blocked?.length) {
    await dialogs.alert({
      title: `${verb} blocked by policy`,
      message: `This ${verb.toLowerCase()} is blocked by policy rule(s) with no per-${verb.toLowerCase()} override:\n\n${formatViolations(r.policy.blocked)}\n\nAn admin can change a rule's mode under Policy rules.`,
    });
    return { ok: false, error: `${verb} blocked by policy — see Policy rules in Settings.` };
  }
  return r;
}

/** A deploy through runWithPolicyGate. Used by the project list and the editor. */
export async function deployProjectWithPolicyGate(
  id: number,
  profiles: string[],
  dialogs: DialogGate,
  opts?: { pull?: boolean },
): Promise<ComposeRunResult & { ok: boolean }> {
  return runWithPolicyGate(
    "Deploy",
    (confirm) => (confirm ? api.deployProject(id, profiles, { ...opts, confirmPolicyWarnings: true }) : api.deployProject(id, profiles, opts)),
    dialogs,
  );
}

/**
 * A revision restore through runWithPolicyGate. A restore redeploys, so it
 * passes the same policy check; without this, one that tripped a warn-mode
 * rule was refused with no way to confirm it from the UI.
 */
export async function restoreRevisionWithPolicyGate(
  id: number,
  revision: number,
  dialogs: DialogGate,
): Promise<ComposeRunResult & { ok: boolean }> {
  return runWithPolicyGate("Restore", (confirm) => api.restoreRevision(id, revision, undefined, confirm), dialogs);
}
