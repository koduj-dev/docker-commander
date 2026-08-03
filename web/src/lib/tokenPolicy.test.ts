import { describe, it, expect } from "vitest";
import { lifetimeOptions, defaultLifetimeIndex } from "./tokenPolicy";
import type { MCPTokenPolicy } from "./types";

const policy = (p: Partial<MCPTokenPolicy> = {}): MCPTokenPolicy => ({
  defaultDays: 30,
  maxDays: 365,
  allowUnlimited: false,
  ...p,
});

describe("lifetimeOptions", () => {
  it("does not offer 'never' unless the admin allowed it", () => {
    // The form offering a choice the server will refuse is the failure this
    // whole helper exists to prevent.
    expect(lifetimeOptions(policy()).some((o) => o.never)).toBe(false);
    expect(lifetimeOptions(policy({ allowUnlimited: true })).some((o) => o.never)).toBe(true);
  });

  it("hides lifetimes above the ceiling", () => {
    const opts = lifetimeOptions(policy({ maxDays: 90 }));
    expect(opts.map((o) => o.days)).toEqual([7, 30, 90]);
  });

  it("offers the policy's own default even when it is not a standard step", () => {
    // An admin who types 45 should see 45. Snapping to the nearest hard-coded
    // value would quietly hand out a different lifetime than they configured.
    const opts = lifetimeOptions(policy({ defaultDays: 45 }));
    expect(opts.map((o) => o.days)).toContain(45);
    expect(opts[defaultLifetimeIndex(opts, policy({ defaultDays: 45 }))].days).toBe(45);
  });

  it("never produces an empty list", () => {
    // A ceiling below every candidate would otherwise leave the form with
    // nothing to select and no way to create a token at all.
    const opts = lifetimeOptions(policy({ defaultDays: 3, maxDays: 3 }));
    expect(opts.length).toBeGreaterThan(0);
    expect(opts.every((o) => o.days <= 3)).toBe(true);
  });

  it("treats maxDays 0 as no ceiling", () => {
    const opts = lifetimeOptions(policy({ maxDays: 0, allowUnlimited: true }));
    expect(opts.map((o) => o.days)).toEqual([7, 30, 90, 365, 0]);
  });

  it("starts the form on the policy default, not the shortest option", () => {
    const p = policy({ defaultDays: 90 });
    const opts = lifetimeOptions(p);
    expect(opts[defaultLifetimeIndex(opts, p)].days).toBe(90);
  });

  it("falls back to a real option when the default is not on offer", () => {
    // Matching by object identity against a freshly built list would silently
    // return -1 here and select nothing.
    const opts = lifetimeOptions(policy());
    expect(defaultLifetimeIndex(opts, policy({ defaultDays: 999 }))).toBe(0);
  });
});
