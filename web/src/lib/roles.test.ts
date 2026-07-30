import { describe, it, expect } from "vitest";
import {
  canWriteAnywhere,
  describeAccess,
  roleSummary,
  rolesForUser,
  sectionState,
  toggleSection,
} from "./roles";
import type { ManagedUser, Role } from "./types";

function role(over: Partial<Role> = {}): Role {
  return { id: 1, name: "R", description: "", builtin: false, sections: [], ...over };
}

function user(over: Partial<ManagedUser> = {}): ManagedUser {
  return {
    id: 1, username: "u", role: "user", readOnly: false, sections: [],
    totpEnabled: false, lastLoginAt: "", ...over,
  };
}

describe("rolesForUser", () => {
  it("resolves assigned roles", () => {
    const roles = [role({ id: 1, name: "A" }), role({ id: 2, name: "B" })];
    expect(rolesForUser(user({ roleIds: [2] }), roles).map((r) => r.name)).toEqual(["B"]);
  });

  it("skips ids that no longer exist rather than rendering undefined", () => {
    expect(rolesForUser(user({ roleIds: [99] }), [role({ id: 1 })])).toEqual([]);
  });

  it("treats a missing roleIds as none", () => {
    expect(rolesForUser(user(), [role()])).toEqual([]);
    expect(rolesForUser(user({ roleIds: null }), [role()])).toEqual([]);
  });
});

describe("roleSummary", () => {
  it("lists writable sections first, then read-only ones", () => {
    const r = role({
      sections: [
        { section: "images", write: false },
        { section: "containers", write: true },
      ],
    });
    expect(roleSummary(r)).toBe("Containers · Images (read-only)");
  });

  it("marks a role that grants nothing", () => {
    expect(roleSummary(role({ sections: [] }))).toBe("no sections");
    expect(roleSummary(role({ sections: null }))).toBe("no sections");
  });

  it("omits the read-only clause when everything is writable", () => {
    const r = role({ sections: [{ section: "logs", write: true }] });
    expect(roleSummary(r)).toBe("Logs");
    expect(roleSummary(r)).not.toContain("read-only");
  });
});

describe("describeAccess", () => {
  it("says everything for an admin, regardless of sections", () => {
    expect(describeAccess(user({ role: "admin", sections: [] }), [])).toBe("everything");
  });

  it("uses the server's effective sections, which include role-granted ones", () => {
    const roles = [role({ id: 5, name: "Deployer", sections: [{ section: "projects", write: true }] })];
    const u = user({ sections: [], roleIds: [5], effectiveSections: ["projects"] });
    const got = describeAccess(u, roles);
    expect(got).toContain("Projects");
    // Naming the role matters: otherwise an admin can't tell where access came from.
    expect(got).toContain("Deployer");
  });

  it("does not claim access when a role grants nothing", () => {
    const roles = [role({ id: 5, name: "Empty", sections: [] })];
    const u = user({ sections: [], roleIds: [5], effectiveSections: [] });
    expect(describeAccess(u, roles)).toBe("no sections");
  });

  it("shows an em dash for an account with neither sections nor roles", () => {
    expect(describeAccess(user(), [])).toBe("—");
  });

  it("falls back to the per-account list when the server sent no effective set", () => {
    expect(describeAccess(user({ sections: ["logs"] }), [])).toBe("Logs");
  });
});

describe("canWriteAnywhere", () => {
  it("is true for an admin", () => {
    expect(canWriteAnywhere(user({ role: "admin" }), [])).toBe(true);
  });

  // The account-level flag caps everything — mirrors the server's precedence.
  it("is false for a read-only account even with a writable role", () => {
    const roles = [role({ id: 1, sections: [{ section: "containers", write: true }] })];
    expect(canWriteAnywhere(user({ readOnly: true, roleIds: [1] }), roles)).toBe(false);
  });

  it("is true when a role grants write", () => {
    const roles = [role({ id: 1, sections: [{ section: "containers", write: true }] })];
    expect(canWriteAnywhere(user({ roleIds: [1] }), roles)).toBe(true);
  });

  it("is false when every role is read-only and there are no per-account sections", () => {
    const roles = [role({ id: 1, sections: [{ section: "containers", write: false }] })];
    expect(canWriteAnywhere(user({ roleIds: [1] }), roles)).toBe(false);
  });

  it("is true for a per-account section, which is always writable", () => {
    expect(canWriteAnywhere(user({ sections: ["logs"] }), [])).toBe(true);
  });
});

describe("toggleSection / sectionState", () => {
  it("cycles a section through none → read → write → none", () => {
    let s = toggleSection([], "containers", "read");
    expect(sectionState(s, "containers")).toBe("read");
    s = toggleSection(s, "containers", "write");
    expect(sectionState(s, "containers")).toBe("write");
    expect(s).toHaveLength(1); // replaced, not duplicated
    s = toggleSection(s, "containers", "none");
    expect(sectionState(s, "containers")).toBe("none");
    expect(s).toEqual([]);
  });

  it("leaves other sections alone and keeps the list sorted", () => {
    let s = toggleSection([], "logs", "write");
    s = toggleSection(s, "containers", "read");
    expect(s.map((x) => x.section)).toEqual(["containers", "logs"]);
    expect(sectionState(s, "logs")).toBe("write");
  });

  it("reports none for an unknown section", () => {
    expect(sectionState([{ section: "logs", write: true }], "hosts")).toBe("none");
  });
});

describe("roleSummary host scope", () => {
  it("says nothing about hosts for an unscoped role", () => {
    const r: Role = { id: 1, name: "R", description: "", builtin: false, sections: [{ section: "logs", write: true }] };
    expect(roleSummary(r)).toBe("Logs");
  });

  it("flags a role limited to specific hosts", () => {
    const r: Role = {
      id: 1, name: "R", description: "", builtin: false,
      sections: [{ section: "logs", write: true }], hostIds: [3, 4],
    };
    expect(roleSummary(r)).toBe("Logs · 2 host(s) only");
  });

  it("flags the scope even when the role grants nothing", () => {
    const r: Role = { id: 1, name: "R", description: "", builtin: false, sections: [], hostIds: [3] };
    expect(roleSummary(r)).toBe("no sections · 1 host(s) only");
  });
});
