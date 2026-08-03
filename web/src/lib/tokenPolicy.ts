import type { MCPTokenPolicy } from "./types";

// Turning the server's token policy into the choices the creation form offers.
//
// The form is a courtesy, not a boundary — the backend re-checks the policy on
// every mint. What this avoids is the worse experience: showing someone "never"
// or "1 year", letting them pick it, and only then refusing. Offer what will be
// accepted, and the refusal never has to happen.

export interface LifetimeOption {
  /** Days; 0 together with never=true means the token has no expiry. */
  days: number;
  never?: boolean;
  label: string;
}

/** The lifetimes worth offering, before the policy narrows them. */
const CANDIDATES = [7, 30, 90, 365];

function label(days: number): string {
  if (days === 365) return "in 1 year";
  if (days % 365 === 0) return `in ${days / 365} years`;
  return `in ${days} days`;
}

/**
 * The lifetime choices allowed by `policy`, shortest first.
 *
 * The policy's own default is always offered even when it is not one of the
 * standard steps — an admin who sets 45 days should see 45 days, not the nearest
 * round number we happened to hard-code.
 */
export function lifetimeOptions(policy: MCPTokenPolicy): LifetimeOption[] {
  const max = policy.maxDays > 0 ? policy.maxDays : Infinity;
  const days = new Set<number>();
  for (const c of CANDIDATES) if (c <= max) days.add(c);
  if (policy.defaultDays > 0 && policy.defaultDays <= max) days.add(policy.defaultDays);
  // A ceiling below every candidate would leave nothing to pick; the ceiling
  // itself is always a legal choice, so fall back to it rather than to nothing.
  if (days.size === 0 && Number.isFinite(max)) days.add(max);

  const out: LifetimeOption[] = [...days].sort((a, b) => a - b).map((d) => ({ days: d, label: label(d) }));
  if (policy.allowUnlimited) out.push({ days: 0, never: true, label: "never" });
  return out;
}

/**
 * Index (into `options`) that a fresh form should start on: the policy's default
 * lifetime, or the shortest available if that default is not on offer.
 *
 * Returns an index rather than the option itself because the caller stores a
 * <select> value, and matching by object identity against a freshly built list
 * would never hit.
 */
export function defaultLifetimeIndex(options: LifetimeOption[], policy: MCPTokenPolicy): number {
  const i = options.findIndex((o) => !o.never && o.days === policy.defaultDays);
  return i >= 0 ? i : 0;
}
