/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { Profile } from "./Profile";
import { DialogProvider } from "../components/Dialog";

// The session list is a security control the user operates by hand, so the
// wiring is the feature: a list that renders but revokes nothing, or a revoke
// that never asks, would both look fine in a screenshot.
//
// Two things are pinned here that a careless refactor would break silently:
// revoking goes through the app's confirm dialog (never one-click), and ending
// the CURRENT session leaves the page rather than sitting on a screen whose next
// request is unauthorized.

const sessions = vi.hoisted(() => vi.fn());
const revokeSession = vi.hoisted(() => vi.fn());
const revokeOtherSessions = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", () => ({
  api: {
    sessions,
    revokeSession,
    revokeOtherSessions,
    myAccess: () => Promise.resolve({ admin: false, effective: [] }),
    // The Security tab also renders the authenticator list; it is not under test
    // here, but it must not explode.
    factors: () => Promise.resolve([{ id: 1, kind: "totp", name: "Phone", createdAt: "2026-08-01T09:00:00Z", lastUsedAt: "2026-08-05T09:00:00Z" }]),
    hosts: () => Promise.resolve([]),
    version: () => Promise.resolve({ version: "test" }),
  },
}));

vi.mock("../auth/AuthContext", () => ({
  useAuth: () => ({
    user: { id: 1, username: "alice", role: "user", totpEnabled: true },
    refresh: () => Promise.resolve(),
  }),
}));

const THIS_ONE = "sess-current";
const OTHER = "sess-laptop";

// Real agent strings: the card runs them through describeClient, and a made-up
// "Firefox on Linux" would sail past a parser that does nothing.
const UA_FIREFOX = "Mozilla/5.0 (X11; Linux x86_64; rv:127.0) Gecko/20100101 Firefox/127.0";
const UA_IPHONE = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1";

let container: HTMLDivElement;
let root: Root | undefined;

// Page buttons only — the modal lives in the same container and its confirm
// button reads "Sign out", which would otherwise match "Sign out everywhere
// else" and quietly test the wrong control.
function button(text: string): HTMLButtonElement {
  const el = [...container.querySelectorAll("button")]
    .filter((b) => !modal()?.contains(b))
    .find((b) => (b.textContent ?? "").includes(text) || b.getAttribute("title")?.includes(text));
  if (!el) throw new Error(`button ${text} not found`);
  return el as HTMLButtonElement;
}

function modal(): HTMLElement | null {
  return container.querySelector("div.fixed.inset-0 form");
}

function dialogConfirm(): HTMLButtonElement {
  const m = modal();
  if (!m) throw new Error("no dialog is open — the action ran without asking");
  return m.querySelector("button[type=submit]") as HTMLButtonElement;
}

function dialogCancel(): HTMLButtonElement {
  const m = modal();
  if (!m) throw new Error("no dialog is open");
  return [...m.querySelectorAll("button")].find((b) => b.textContent === "Cancel") as HTMLButtonElement;
}

async function openSecurity() {
  await act(async () => button("Security").click());
}

// mount renders a fresh Profile. Re-rendering into the existing root would keep
// the card's state (and its already-resolved fetch), so tests that change what
// the API returns must start over.
async function mount() {
  if (root) {
    act(() => root.unmount());
    container.remove();
  }
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root.render(
      <MemoryRouter>
        <DialogProvider>
          <Profile />
        </DialogProvider>
      </MemoryRouter>,
    );
  });
  await openSecurity();
}

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  sessions.mockResolvedValue([
    {
      id: THIS_ONE, ip: "10.0.0.9", userAgent: UA_FIREFOX,
      createdAt: "2026-08-05T09:00:00Z", lastSeenAt: "2026-08-05T10:00:00Z", current: true,
    },
    {
      id: OTHER, ip: "203.0.113.7", userAgent: UA_IPHONE,
      createdAt: "2026-08-01T09:00:00Z", lastSeenAt: "2026-08-04T18:00:00Z", current: false,
    },
  ]);
  revokeSession.mockResolvedValue({ ok: true });
  revokeOtherSessions.mockResolvedValue({ revoked: 1 });

  await mount();
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container.remove();
  vi.clearAllMocks();
});

describe("Profile → Security → signed-in sessions", () => {
  it("lists every session and marks the one you are using", () => {
    expect(container.textContent).toContain("Firefox on Linux");
    expect(container.textContent).toContain("Safari on iPhone");
    expect(container.textContent).toContain("203.0.113.7");
    expect(container.textContent).toContain("this device");
  });

  it("asks before revoking, and does nothing if you say no", async () => {
    await act(async () => button("Sign this session out").click());
    expect(revokeSession).not.toHaveBeenCalled();
    expect(container.textContent).toContain("Sign out that session?");

    await act(async () => dialogCancel().click());
    expect(revokeSession).not.toHaveBeenCalled();
  });

  it("revokes the session you picked, not the one you are using", async () => {
    await act(async () => button("Sign this session out").click());
    await act(async () => dialogConfirm().click());
    expect(revokeSession).toHaveBeenCalledWith(OTHER);
    // …and reloads, so a revoked row cannot linger as something to revoke again.
    expect(sessions).toHaveBeenCalledTimes(2);
  });

  it("leaves the page when you end the session you are using", async () => {
    const assign = vi.fn();
    Object.defineProperty(window, "location", { value: { assign }, writable: true });

    await act(async () => button("Sign out here").click());
    await act(async () => dialogConfirm().click());

    expect(revokeSession).toHaveBeenCalledWith(THIS_ONE);
    expect(assign).toHaveBeenCalledWith("/");
  });

  it("offers sign-out-everywhere-else only while there is somewhere else", async () => {
    expect(() => button("Sign out everywhere else")).not.toThrow();

    await act(async () => button("Sign out everywhere else").click());
    await act(async () => dialogConfirm().click());
    expect(revokeOtherSessions).toHaveBeenCalled();

    // One session left: nothing else to sign out, so the button goes away.
    sessions.mockResolvedValue([
      {
        id: THIS_ONE, ip: "10.0.0.9", userAgent: UA_FIREFOX,
        createdAt: "2026-08-05T09:00:00Z", lastSeenAt: "2026-08-05T10:00:00Z", current: true,
      },
    ]);
    await mount();
    expect(() => button("Sign out everywhere else")).toThrow();
  });

  it("says so when the list cannot be loaded, instead of spinning forever", async () => {
    sessions.mockRejectedValue(new Error("network down"));
    await mount();
    expect(container.textContent).toContain("network down");
    expect(container.textContent).not.toContain("Loading…");
  });

  it("clears the failure once a retry succeeds", async () => {
    sessions.mockRejectedValue(new Error("network down"));
    await mount();
    expect(container.textContent).toContain("network down");

    sessions.mockResolvedValue([
      {
        id: THIS_ONE, ip: "10.0.0.9", userAgent: UA_FIREFOX,
        createdAt: "2026-08-05T09:00:00Z", lastSeenAt: "2026-08-05T10:00:00Z", current: true,
      },
    ]);
    await act(async () => button("Try again").click());
    expect(container.textContent).toContain("Firefox on Linux");
    expect(container.textContent).not.toContain("network down");
  });
});
