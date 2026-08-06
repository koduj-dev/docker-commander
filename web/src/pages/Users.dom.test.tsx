/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { Users } from "./Users";
import { DialogProvider } from "../components/Dialog";

// The 2FA column answers "is this account protected?", and an admin auditing
// their users acts on it. It used to read `totpEnabled` — "does this account have
// an authenticator app?" — which is a different question the moment passkeys
// exist: an account holding only a passkey is protected and was shown as **off**.
//
// The server has sent `mfaEnabled` for exactly this since passkeys landed; the
// table simply did not read it.

const users = vi.hoisted(() => vi.fn());
const roles = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", () => ({
  api: {
    users,
    roles,
    // PageHeader renders the active-host badge; not under test, must not explode.
    hosts: () => Promise.resolve([]),
    version: () => Promise.resolve({ version: "test" }),
    // The page reads app-wide settings to know which sections are disabled.
    settings: () => Promise.resolve({ disabledSections: [] }),
  },
}));

vi.mock("../auth/AuthContext", () => ({
  useAuth: () => ({ user: { id: 1, username: "admin", role: "admin" }, refresh: () => Promise.resolve() }),
}));

let container: HTMLDivElement;
let root: Root | undefined;

function rowFor(username: string): HTMLTableRowElement {
  const row = [...container.querySelectorAll("tr")].find((r) => r.textContent?.includes(username));
  if (!row) throw new Error(`no row for ${username}`);
  return row as HTMLTableRowElement;
}

const account = (over: Record<string, unknown>) => ({
  id: 1, username: "someone", role: "user", readOnly: false, sections: null,
  roleIds: [], effectiveSections: [], totpEnabled: false, lastLoginAt: "",
  ...over,
});

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  roles.mockResolvedValue([]);
  users.mockResolvedValue([
    account({ id: 1, username: "appuser", totpEnabled: true, mfaEnabled: true }),
    account({ id: 2, username: "keyuser", totpEnabled: false, mfaEnabled: true }),
    account({ id: 3, username: "nakeduser", totpEnabled: false, mfaEnabled: false }),
  ]);
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(<MemoryRouter><DialogProvider><Users /></DialogProvider></MemoryRouter>);
  });
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container.remove();
  vi.clearAllMocks();
});

describe("the admin 2FA column", () => {
  it("does not report a passkey-only account as unprotected", () => {
    // The whole point: totpEnabled is false here and the account IS protected.
    expect(rowFor("keyuser").textContent).not.toContain("off");
  });

  it("distinguishes an authenticator app from a passkey", () => {
    expect(rowFor("appuser").textContent).toContain("enabled");
    expect(rowFor("keyuser").textContent).toContain("passkey");
  });

  it("still says off for an account with no second factor at all", () => {
    expect(rowFor("nakeduser").textContent).toContain("off");
  });

  it("falls back to totpEnabled when the server sends no mfaEnabled", async () => {
    // An older server, or a payload that predates the field: an account with an
    // authenticator must not suddenly read as unprotected.
    users.mockResolvedValue([account({ id: 4, username: "legacy", totpEnabled: true })]);
    // A fresh key forces a remount: re-rendering the same instance would not
    // re-run the effect that fetches, so the old rows would still be on screen.
    await act(async () => {
      root!.render(<MemoryRouter><DialogProvider><Users key="legacy" /></DialogProvider></MemoryRouter>);
    });
    expect(rowFor("legacy").textContent).toContain("enabled");
  });
});
