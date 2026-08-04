/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { AuthProvider, useAuth } from "./AuthContext";
import { alertPulseSnapshot, pollAlertsOnce, resetAlertStream } from "../lib/alertStream";

// The wiring test for logout.
//
// session.test.ts proves clearUserState() empties the per-user caches; what it
// cannot prove is that logging out calls it — and "the function exists, is
// documented as call-on-logout, and has no call sites" is exactly the bug that
// shipped. So this drives the real provider: sign a user in, let alerts
// accumulate, log out, and require the next person to find nothing.

const alerts = vi.hoisted(() => vi.fn());
const logoutCall = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", () => ({
  api: {
    alerts,
    logout: logoutCall,
    authStatus: () => Promise.resolve({ needsSetup: false }),
    me: () => Promise.resolve({ id: 1, username: "alice", role: "user" }),
    prefs: () => Promise.resolve({}),
  },
}));

let container: HTMLDivElement;
let root: Root;
let logout: () => Promise<void>;

function Probe() {
  const auth = useAuth();
  logout = auth.logout;
  return null;
}

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  resetAlertStream();
  alerts.mockReset();
  logoutCall.mockReset();
  logoutCall.mockResolvedValue(undefined);

  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root.render(
      <AuthProvider>
        <Probe />
      </AuthProvider>,
    );
  });
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

describe("logout", () => {
  it("leaves no alert state for the next user on this browser", async () => {
    // Two polls: the first sets the baseline, the second is what a signed-in user
    // accumulates — an unread badge and events queued for toasting.
    alerts.mockResolvedValueOnce({ events: [{ id: 10 }], unread: 1 });
    await pollAlertsOnce();
    alerts.mockResolvedValueOnce({ events: [{ id: 12 }, { id: 11 }, { id: 10 }], unread: 3 });
    await pollAlertsOnce();
    expect(alertPulseSnapshot().unread).toBe(3);

    await act(async () => {
      await logout();
    });

    expect(logoutCall).toHaveBeenCalled();
    expect(alertPulseSnapshot()).toEqual({ tick: 0, unread: 0, fresh: [] });
  });

  // The baseline half: with it kept, the next user's first poll would compare
  // against the previous user's high-water mark instead of establishing its own.
  it("forgets the baseline, so the next session starts clean", async () => {
    alerts.mockResolvedValueOnce({ events: [{ id: 10 }], unread: 1 });
    await pollAlertsOnce();
    alerts.mockResolvedValueOnce({ events: [{ id: 12 }, { id: 11 }], unread: 2 });
    await pollAlertsOnce();

    await act(async () => {
      await logout();
    });

    // A NEWER event than the pre-logout baseline (12). With the baseline cleared
    // this poll only establishes a new one and announces nothing; with it kept,
    // 15 > 12 and the new user is toasted about the previous user's alert. Using
    // the same ids here made the assertion pass either way — a vacuous test that
    // mutation-testing caught.
    alerts.mockResolvedValueOnce({ events: [{ id: 15 }, { id: 12 }], unread: 2 });
    await pollAlertsOnce();
    expect(alertPulseSnapshot().fresh).toEqual([]);
  });
});
