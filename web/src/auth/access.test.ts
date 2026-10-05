import { describe, it, expect, vi } from "vitest";
import type { MyAccess } from "../lib/types";

vi.mock("../lib/api", () => ({ api: {} }));
vi.mock("./AuthContext", () => ({ useAuth: () => ({ user: null }) }));
const { canWrite } = await import("./access");

const user = { role: "user", readOnly: false };
const access = (grants: { section: string; write: boolean }[], readOnly = false): MyAccess =>
  ({ admin: false, readOnly, roles: [], sections: null, effective: grants.map((g) => ({ ...g, from: [] })) });

// canWrite decides whether to offer a raw download (export, file download,
// image save). Those need write; offering one that the server refuses opens a
// bare 403 page instead of a file.
describe("canWrite", () => {
  it("is true for an admin, whatever the grants say", () => {
    expect(canWrite({ role: "admin", readOnly: false }, null, "containers")).toBe(true);
  });

  it("is false for a read-only account, even with a write grant", () => {
    expect(canWrite({ role: "user", readOnly: true }, access([{ section: "containers", write: true }]), "containers")).toBe(false);
  });

  it("follows the section's write grant", () => {
    const a = access([{ section: "containers", write: true }, { section: "images", write: false }]);
    expect(canWrite(user, a, "containers")).toBe(true);
    expect(canWrite(user, a, "images")).toBe(false);
    expect(canWrite(user, a, "volumes")).toBe(false);
  });

  it("is false until access is known", () => {
    expect(canWrite(user, null, "containers")).toBe(false);
  });

  it("is false when the server reports the account read-only", () => {
    expect(canWrite(user, access([{ section: "containers", write: true }], true), "containers")).toBe(false);
  });
});
