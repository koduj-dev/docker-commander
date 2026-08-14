import { describe, it, expect } from "vitest";
import { isProfileExcluded, resolveServiceState } from "./composeState";

describe("isProfileExcluded", () => {
  it("a service with no profiles key is never excluded", () => {
    expect(isProfileExcluded(undefined, [])).toBe(false);
    expect(isProfileExcluded([], ["prod"])).toBe(false);
  });

  it("a service whose profile IS in the deployed set is included", () => {
    expect(isProfileExcluded(["extra"], ["extra", "prod"])).toBe(false);
  });

  it("a service whose profile is NOT among the deployed ones is excluded", () => {
    expect(isProfileExcluded(["extra"], ["prod"])).toBe(true);
    expect(isProfileExcluded(["extra"], [])).toBe(true);
  });

  it("any one matching profile is enough (a service can list several)", () => {
    expect(isProfileExcluded(["dev", "extra"], ["extra"])).toBe(false);
  });
});

// resolveServiceState is the actual bug fix: naively, a service with no
// matching container reads as "stopped" no matter why the container is
// missing — which is exactly the complaint against Portainer/Dockge/Arcane
// this feature exists to avoid repeating.
describe("resolveServiceState", () => {
  it("all matching containers running → running", () => {
    const r = resolveServiceState({}, [{ state: "running" }, { state: "running" }], true, []);
    expect(r.state).toBe("running");
  });

  it("some but not all running → a distinct partial state", () => {
    const r = resolveServiceState({}, [{ state: "running" }, { state: "exited" }], true, []);
    expect(r.state).toBe("partial");
  });

  it("none running → that container's own state (e.g. exited)", () => {
    const r = resolveServiceState({}, [{ state: "exited" }], true, []);
    expect(r.state).toBe("exited");
  });

  it("no container, service excluded by the deployed profiles → 'excluded', NOT stopped", () => {
    const r = resolveServiceState({ profiles: ["extra"] }, [], true, ["prod"]);
    expect(r.state).toBe("excluded");
    expect(r.label).toMatch(/not in active profile/i);
  });

  it("no container, project never deployed at all → 'not-deployed'", () => {
    const r = resolveServiceState({}, [], false, []);
    expect(r.state).toBe("not-deployed");
  });

  it("'not-deployed' wins over 'excluded' — a never-deployed project has no active profile set to be excluded from", () => {
    // Regression: with lastDeployedProfiles == [] (the natural default for a
    // never-deployed project), a profiled service used to read as "excluded"
    // instead of "not-deployed" because the exclusion check ran first.
    const r = resolveServiceState({ profiles: ["extra"] }, [], false, []);
    expect(r.state).toBe("not-deployed");
  });

  it("no container, deployed, and NOT profile-excluded → genuinely stopped", () => {
    const r = resolveServiceState({}, [], true, []);
    expect(r.state).toBe("exited");
    expect(r.label).toMatch(/stopped/i);
  });

  it("no container, deployed, service's profile WAS part of the deploy → genuinely stopped, not excluded", () => {
    // The profile was active at deploy time, so a missing container here means
    // the service crashed or was manually stopped — not that it was skipped.
    const r = resolveServiceState({ profiles: ["extra"] }, [], true, ["extra"]);
    expect(r.state).toBe("exited");
  });
});
