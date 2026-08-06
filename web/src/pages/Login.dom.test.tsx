/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Login } from "./Login";
import { ApiError } from "../lib/api";

// The server spends a challenge token on the first attempt, right or wrong. So a
// rejected code cannot be retried on the same screen — the UI has to take the
// user back to the password step rather than leave them typing into a form that
// can no longer succeed.

const login = vi.hoisted(() => vi.fn());
const verify2fa = vi.hoisted(() => vi.fn());
const passkeyLoginBegin = vi.hoisted(() => vi.fn());
const passkeyLoginFinish = vi.hoisted(() => vi.fn());
const passwordlessBegin = vi.hoisted(() => vi.fn());
const passwordlessFinish = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return { ...actual, api: { login, verify2fa, passkeyLoginBegin, passkeyLoginFinish, passwordlessBegin, passwordlessFinish } };
});
vi.mock("../auth/AuthContext", () => ({
  useAuth: () => ({ refresh: () => Promise.resolve() }),
}));

let container: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  login.mockReset();
  verify2fa.mockReset();
  passkeyLoginBegin.mockReset();
  passkeyLoginFinish.mockReset();
  passwordlessBegin.mockReset();
  passwordlessFinish.mockReset();
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => root.render(<Login />));
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

function typeInto(el: Element, value: string) {
  const input = el as HTMLInputElement;
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")?.set;
  setter?.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

// A browser that has WebAuthn, with a scripted authenticator.
function withPasskeySupport(behaviour: { get?: () => Promise<unknown> } = {}) {
  (globalThis as Record<string, unknown>).PublicKeyCredential = class {};
  Object.defineProperty(navigator, "credentials", {
    configurable: true,
    value: {
      create: vi.fn(),
      get: behaviour.get ?? (() => Promise.reject(new DOMException("dismissed", "NotAllowedError"))),
    },
  });
}

async function reachTheCodeStep(methods: string[] = ["totp"], passkeyReady = methods.includes("passkey")) {
  login.mockResolvedValue({ mfaRequired: true, mfaToken: "challenge-1", methods, passkeyReady });
  const [user, pass] = [...container.querySelectorAll("input")];
  await act(async () => {
    typeInto(user, "admin");
    typeInto(pass, "correcthorse123");
  });
  await act(async () => {
    container.querySelector("form")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  });
}

describe("the 2FA step with a passkey", () => {
  it("explains itself when the account has a passkey this connection cannot use", async () => {
    // A 2FA screen with no control on it is a lockout that looks like a bug.
    withPasskeySupport();
    await reachTheCodeStep(["passkey"], false);
    expect(container.textContent).not.toContain("Use a passkey");
    expect(container.textContent).toContain("cannot use one");
    expect(container.textContent).toContain("HTTPS");
  });

  it("does not offer a passkey to an account without one", async () => {
    withPasskeySupport();
    await reachTheCodeStep(["totp"]);
    expect(container.textContent).not.toContain("Use a passkey");
  });

  it("offers it to an account that has one", async () => {
    withPasskeySupport();
    await reachTheCodeStep(["totp", "passkey"]);
    expect(container.textContent).toContain("Use a passkey");
  });

  it("does not show the code box to an account that only has a passkey", async () => {
    withPasskeySupport();
    await reachTheCodeStep(["passkey"]);
    expect(container.querySelector("input")).toBeNull();
    expect(container.textContent).toContain("Use a passkey");
  });

  it("returns to the password step when the prompt is dismissed", async () => {
    // The challenge token is spent whether or not the user completed the prompt,
    // so there is nothing left to retry on this screen.
    withPasskeySupport();
    passkeyLoginBegin.mockResolvedValue({ publicKey: { challenge: "Y2hhbGxlbmdl", allowCredentials: [] } });
    await reachTheCodeStep(["totp", "passkey"]);

    const button = [...container.querySelectorAll("button")].find((b) => b.textContent?.includes("Use a passkey"));
    await act(async () => button!.click());

    expect(container.textContent).not.toContain("Two-factor authentication");
    expect(container.textContent).toContain("dismissed or timed out");
    expect(container.textContent).toContain("Sign in again");
  });

  it("signs in when the authenticator answers", async () => {
    withPasskeySupport({
      get: () => Promise.resolve({
        id: "cred", rawId: new Uint8Array([1, 2, 3]).buffer, type: "public-key",
        response: {
          authenticatorData: new Uint8Array([4]).buffer,
          clientDataJSON: new Uint8Array([5]).buffer,
          signature: new Uint8Array([6]).buffer,
          userHandle: null,
        },
      }),
    });
    passkeyLoginBegin.mockResolvedValue({ publicKey: { challenge: "Y2hhbGxlbmdl", allowCredentials: [] } });
    passkeyLoginFinish.mockResolvedValue({ user: { id: 1, username: "alice" } });

    await reachTheCodeStep(["passkey"]);
    const button = [...container.querySelectorAll("button")].find((b) => b.textContent?.includes("Use a passkey"));
    await act(async () => button!.click());

    expect(passkeyLoginBegin).toHaveBeenCalledWith("challenge-1");
    expect(passkeyLoginFinish).toHaveBeenCalled();
    // The challenge token, not something the page invented.
    expect(passkeyLoginFinish.mock.calls[0][0]).toBe("challenge-1");
  });
});

describe("the 2FA step", () => {
  it("returns to the password screen when the code is rejected", async () => {
    await reachTheCodeStep();
    expect(container.textContent).toContain("Two-factor authentication");

    verify2fa.mockRejectedValue(new ApiError(401, "invalid code"));
    await act(async () => {
      typeInto(container.querySelector("input")!, "000000");
    });
    await act(async () => {
      container.querySelector("form")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });

    expect(container.textContent).not.toContain("Two-factor authentication");
    expect(container.textContent).toContain("Sign in again");
  });

  it("does not blame the code when the request never arrived", async () => {
    await reachTheCodeStep();

    // A network failure is not an ApiError. Saying "that code was not accepted"
    // sends the user hunting through their authenticator for a problem that is
    // in the wire.
    verify2fa.mockRejectedValue(new TypeError("Failed to fetch"));
    await act(async () => {
      typeInto(container.querySelector("input")!, "000000");
    });
    await act(async () => {
      container.querySelector("form")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });

    expect(container.textContent).toContain("Could not reach the server");
    expect(container.textContent).not.toContain("That code was not accepted");
  });

  it("stays on the code screen while it is still usable", async () => {
    await reachTheCodeStep();
    expect(container.querySelector("input")).toBeTruthy();
    expect(container.textContent).toContain("Two-factor authentication");
  });
});

// The passwordless button is offered next to the password, never instead of it.
// A lost key must cost convenience, not the account — and this app gives admins no
// way to reset someone else's second factor, so the password IS the recovery path.
describe("passwordless sign-in", () => {
  it("offers a passkey alongside the password form", async () => {
    withPasskeySupport();
    await act(async () => root.render(<Login />));

    const buttons = [...container.querySelectorAll("button")].map((b) => b.textContent);
    expect(buttons).toContain("Sign in with a passkey");
    // …and the password form is still there. This is additive, not a replacement.
    expect(container.querySelector('input[type="password"]')).not.toBeNull();
  });

  it("stays quiet where the browser cannot do passkeys", async () => {
    delete (globalThis as Record<string, unknown>).PublicKeyCredential;
    await act(async () => root.render(<Login />));

    const buttons = [...container.querySelectorAll("button")].map((b) => b.textContent);
    expect(buttons).not.toContain("Sign in with a passkey");
  });

  it("signs in without a username or a password", async () => {
    const credential = { id: "cred", rawId: "cred", type: "public-key", response: {} };
    withPasskeySupport({ get: () => Promise.resolve(credential) });
    passwordlessBegin.mockResolvedValue({
      ceremonyId: "ceremony-1",
      publicKey: { challenge: "AAEC", allowCredentials: [] },
    });
    passwordlessFinish.mockResolvedValue({ user: { id: 1, username: "admin" } });

    await act(async () => root.render(<Login />));
    const button = [...container.querySelectorAll("button")].find(
      (b) => b.textContent === "Sign in with a passkey",
    );
    await act(async () => button?.dispatchEvent(new MouseEvent("click", { bubbles: true })));

    expect(passwordlessBegin).toHaveBeenCalled();
    // The ceremony id has to travel back, or the server cannot tie the answer to
    // the challenge it issued.
    expect(passwordlessFinish).toHaveBeenCalledWith("ceremony-1", expect.anything());
    // Neither field was touched.
    expect(login).not.toHaveBeenCalled();
  });

  it("explains a refusal rather than leaving the button dead", async () => {
    withPasskeySupport({ get: () => Promise.resolve({ id: "c", rawId: "c", type: "public-key", response: {} }) });
    passwordlessBegin.mockResolvedValue({ ceremonyId: "c1", publicKey: { challenge: "AAEC" } });
    passwordlessFinish.mockRejectedValue(
      new ApiError(401, "auth: this passkey did not verify who you are; use your password"),
    );

    await act(async () => root.render(<Login />));
    const button = [...container.querySelectorAll("button")].find(
      (b) => b.textContent === "Sign in with a passkey",
    );
    await act(async () => button?.dispatchEvent(new MouseEvent("click", { bubbles: true })));

    expect(container.textContent).toContain("did not verify who you are");
  });
});
