import type { ManagedUser, Role, RoleSection } from "./types";
import { sectionLabel } from "./sections";

/**
 * Pure helpers for presenting roles and effective access. They live here (rather
 * than inside the Users page) so the logic that decides what an admin *believes*
 * an account can do is unit-tested — getting this display wrong is how someone
 * grants more than they intended.
 */

/** roleById indexes roles for quick lookup from a user's roleIds. */
export function roleById(roles: Role[]): Map<number, Role> {
  return new Map(roles.map((r) => [r.id, r]));
}

/** rolesForUser resolves a user's assigned roles, skipping ids that no longer exist. */
export function rolesForUser(user: ManagedUser, roles: Role[]): Role[] {
  const byId = roleById(roles);
  return (user.roleIds ?? []).map((id) => byId.get(id)).filter((r): r is Role => !!r);
}

/**
 * roleSummary renders a role's grants compactly, e.g.
 * "Containers, Images (read-only), Logs". Writable sections come first because
 * they're the ones that carry risk.
 */
export function roleSummary(role: Role): string {
  const sections = role.sections ?? [];
  if (sections.length === 0) return "no sections";
  const write = sections.filter((s) => s.write).map((s) => sectionLabel(s.section));
  const read = sections.filter((s) => !s.write).map((s) => sectionLabel(s.section));
  const parts: string[] = [];
  if (write.length > 0) parts.push(write.join(", "));
  if (read.length > 0) parts.push(`${read.join(", ")} (read-only)`);
  return parts.join(" · ");
}

/**
 * describeAccess is what the users table shows in its Access column. It must not
 * overstate: an admin has everything, a read-only account can only ever read, and
 * a section reached through a role counts just as much as one granted directly.
 */
export function describeAccess(user: ManagedUser, roles: Role[]): string {
  if (user.role === "admin") return "everything";
  const assigned = rolesForUser(user, roles);
  // Prefer the server's computed set: it already folds in roles and removes
  // app-wide disabled sections, which the client cannot know about.
  const effective = user.effectiveSections ?? user.sections ?? [];
  if (effective.length === 0) return assigned.length > 0 ? "no sections" : "—";
  const labels = effective.map(sectionLabel).join(", ");
  if (assigned.length === 0) return labels;
  return `${labels} — via ${assigned.map((r) => r.name).join(", ")}`;
}

/**
 * canWriteAnywhere reports whether an account can change anything at all. Used to
 * flag accounts that look restricted but aren't — a writable role on a
 * non-read-only account still means write access.
 */
export function canWriteAnywhere(user: ManagedUser, roles: Role[]): boolean {
  if (user.role === "admin") return true;
  if (user.readOnly) return false; // the account-level flag caps everything
  if ((user.sections ?? []).length > 0) return true; // per-account grants are writable
  return rolesForUser(user, roles).some((r) => (r.sections ?? []).some((s) => s.write));
}

/** toggleSection returns the grants with one section's state advanced. */
export function toggleSection(
  sections: RoleSection[],
  section: string,
  next: "none" | "read" | "write",
): RoleSection[] {
  const rest = sections.filter((s) => s.section !== section);
  if (next === "none") return rest;
  return [...rest, { section, write: next === "write" }].sort((a, b) => a.section.localeCompare(b.section));
}

/** sectionState reports how a role currently grants one section. */
export function sectionState(sections: RoleSection[], section: string): "none" | "read" | "write" {
  const found = sections.find((s) => s.section === section);
  if (!found) return "none";
  return found.write ? "write" : "read";
}
