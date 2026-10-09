/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { Profile } from "./Profile";
import { DialogProvider } from "../components/Dialog";

// Changing your own password (Account tab), and adding a second factor to an
// account whose only factor is a passkey (Security tab).
//
// The second used to be a dead end: the form asked for the password only when an
// authenticator APP was paired, while the server asks whenever ANY factor is, so
// a passkey-only account was refused with no field to answer in.

const changeMyPassword = vi.hoisted(() => vi.fn());
const totpSetup = vi.hoisted(() => vi.fn());
const passkeyRegisterBegin = vi.hoisted(() => vi.fn());
const refresh = vi.hoisted(() => vi.fn());
const factors = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", () => ({
  api: {
    changeMyPassword,
    totpSetup,
    passkeyRegisterBegin,
    factors,
    setMyEmail: () => Promise.resolve({ ok: true }),
    sessions: () => Promise.resolve([]),
    myAccess: () => Promise.resolve({ admin: false, effective: [] }),
    hosts: () => Promise.resolve([]),
    passkeySupport: () => Promise.resolve({ available: true, reason: "" }),
    version: () => Promise.resolve({ version: "test" }),
  },
}));

vi.mock("../lib/webauthn", () => ({
  passkeysSupported: () => true,
  createPasskey: () => Promise.reject(new Error("not in a test")),
  describePasskeyError: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}));

const mockUser = vi.hoisted(() => ({ current: {} as Record<string, unknown> }));
vi.mock("../auth/AuthContext", () => ({
  useAuth: () => ({ user: mockUser.current, refresh }),
}));

const PASSKEY = { id: 3, kind: "passkey", name: "Laptop", createdAt: "2026-08-01T09:00:00Z", lastUsedAt: "2026-08-05T09:00:00Z" };

let container: HTMLDivElement;
let root: Root | undefined;

function button(text: string): HTMLButtonElement {
  const el = [...container.querySelectorAll("button")].find((b) => (b.textContent ?? "").includes(text));
  if (!el) throw new Error(`button ${text} not found`);
  return el as HTMLButtonElement;
}

function type(id: string, value: string) {
  const input = container.querySelector(`#${id}`) as HTMLInputElement | null;
  if (!input) throw new Error(`no #${id}`);
  // React tracks the value through the prototype setter, not the property.
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  act(() => {
    setter.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function mount(user: Record<string, unknown>, tab?: string) {
  mockUser.current = { id: 1, username: "alice", role: "user", ...user };
  if (root) {
    act(() => root!.unmount());
    container.remove();
  }
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter>
        <DialogProvider>
          <Profile />
        </DialogProvider>
      </MemoryRouter>,
    );
  });
  if (tab) await act(async () => button(tab).click());
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  changeMyPassword.mockResolvedValue({});
  totpSetup.mockResolvedValue({ secret: "S", qrDataUri: "data:," });
  passkeyRegisterBegin.mockResolvedValue({});
  refresh.mockResolvedValue(undefined);
  factors.mockResolvedValue([]);
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container.remove();
  vi.clearAllMocks();
});

describe("Profile → Account → password", () => {
  it("sends the current and the new password, then refreshes the account", async () => {
    await mount({});
    type("pw-current", "old-password");
    type("pw-new", "brand-new-pass");
    type("pw-again", "brand-new-pass");
    await act(async () => button("Change password").click());
    expect(changeMyPassword).toHaveBeenCalledWith("old-password", "brand-new-pass");
    expect(refresh).toHaveBeenCalled();
    expect(container.textContent).toContain("Every other session has been signed out");
    expect((container.querySelector("#pw-current") as HTMLInputElement).value).toBe("");
  });

  it("uses password fields, so nothing typed is shown on screen", async () => {
    await mount({});
    for (const id of ["pw-current", "pw-new", "pw-again"]) {
      expect((container.querySelector(`#${id}`) as HTMLInputElement).type, id).toBe("password");
    }
  });

  it("won't send two new passwords that differ, and says why", async () => {
    await mount({});
    type("pw-current", "old-password");
    type("pw-new", "brand-new-pass");
    type("pw-again", "brand-new-pasS");
    expect(button("Change password").disabled).toBe(true);
    expect(container.textContent).toContain("don't match");
  });

  it("won't send a new password under the minimum, and says why", async () => {
    await mount({});
    type("pw-current", "old-password");
    type("pw-new", "short");
    type("pw-again", "short");
    expect(button("Change password").disabled).toBe(true);
    expect(container.textContent).toContain("shorter than 10");
  });

  it("shows the server's refusal instead of pretending it worked", async () => {
    changeMyPassword.mockRejectedValue(new Error("your current password is not right"));
    await mount({});
    type("pw-current", "wrong-password");
    type("pw-new", "brand-new-pass");
    type("pw-again", "brand-new-pass");
    await act(async () => button("Change password").click());
    expect(container.textContent).toContain("your current password is not right");
    expect(container.textContent).not.toContain("Every other session has been signed out");
  });

  it("is not offered to an account whose password lives elsewhere", async () => {
    for (const source of ["ldap", "oidc"]) {
      await mount({ authSource: source });
      expect(container.querySelector("#pw-current"), source).toBeNull();
    }
  });
});

describe("Profile → Security → adding a factor to a passkey-only account", () => {
  const passkeyOnly = { mfaEnabled: true, totpEnabled: false };

  it("asks for the password", async () => {
    factors.mockResolvedValue([PASSKEY]);
    await mount(passkeyOnly, "Security");
    const field = container.querySelector("#repair-password") as HTMLInputElement | null;
    expect(field).not.toBeNull();
    expect(field!.type).toBe("password");
  });

  it("sends it when adding an authenticator app", async () => {
    factors.mockResolvedValue([PASSKEY]);
    await mount(passkeyOnly, "Security");
    type("repair-password", "my-password-1");
    await act(async () => button("Add an authenticator").click());
    expect(totpSetup).toHaveBeenCalledWith("my-password-1");
  });

  it("sends it when adding another passkey", async () => {
    factors.mockResolvedValue([PASSKEY]);
    await mount(passkeyOnly, "Security");
    type("repair-password", "my-password-1");
    await act(async () => button("Add a passkey").click());
    expect(passkeyRegisterBegin).toHaveBeenCalledWith("my-password-1");
  });

  it("asks for nothing on a first enrolment", async () => {
    await mount({ mfaEnabled: false, totpEnabled: false }, "Security");
    expect(container.querySelector("#repair-password")).toBeNull();
    await act(async () => button("Set up 2FA").click());
    expect(totpSetup).toHaveBeenCalledWith(undefined);
  });
});
