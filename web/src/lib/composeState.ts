// Resolves what a compose service's state badge should say, by matching it
// against the project's live containers and the profiles ACTUALLY USED at its
// last deploy — not just what's currently declared in the compose file, and
// not just what's *selected* client-side for the next deploy.
//
// This is the fix for a gap that Portainer, Dockge and Arcane all share: a
// service that's simply excluded by the active profile set (no container was
// ever created for it) has historically been shown identically to a service
// that crashed or was manually stopped. Both look the same — no matching
// container — but they mean very different things to an operator.
import type { ComposeService, StackContainer } from "./types";

export interface ServiceStateResult {
  /** Fed straight into <StateBadge state={…}>. "excluded"/"not-deployed" are
   * synthetic states (unknown to the docker daemon) that render as a neutral,
   * non-alarming badge — deliberately distinct from "exited"/"dead". */
  state: string;
  label?: string;
}

// isProfileExcluded reports whether a compose service is left out by the
// profiles actually used at the project's last deploy. A service with no
// `profiles` key carries no restriction and is always included (compose's
// implicit default set), regardless of which named profiles were passed.
export function isProfileExcluded(serviceProfiles: string[] | undefined, lastDeployedProfiles: string[]): boolean {
  if (!serviceProfiles || serviceProfiles.length === 0) return false;
  return !serviceProfiles.some((p) => lastDeployedProfiles.includes(p));
}

// resolveServiceState decides a single service's displayed state:
//   - one or more matching containers → their aggregate container state
//     (all running → "running"; none running → that container's state;
//     otherwise → a "partial" state distinct from both)
//   - no container, and the project isn't deployed at all → "not-deployed"
//     (checked BEFORE profile exclusion: with lastDeployedProfiles == [], every
//     profiled service would otherwise look "excluded" even though nothing —
//     profiled or not — has ever been deployed)
//   - no container, but excluded by the profiles actually deployed → "excluded"
//   - no container, deployed, not excluded → genuinely "exited" ("Stopped")
export function resolveServiceState(
  svc: Pick<ComposeService, "profiles">,
  containers: Pick<StackContainer, "state">[],
  hasStack: boolean,
  lastDeployedProfiles: string[],
): ServiceStateResult {
  if (containers.length > 0) {
    const running = containers.filter((c) => c.state === "running").length;
    if (running === containers.length) return { state: "running" };
    if (running === 0) return { state: containers[0].state };
    return { state: "partial", label: "Partial" };
  }
  if (!hasStack) return { state: "not-deployed", label: "Not deployed" };
  if (isProfileExcluded(svc.profiles, lastDeployedProfiles)) {
    return { state: "excluded", label: "Not in active profile" };
  }
  return { state: "exited", label: "Stopped" };
}
