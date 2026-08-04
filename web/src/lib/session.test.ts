import { describe, it, expect, vi, beforeEach } from "vitest";
import { alertPulseSnapshot, pollAlertsOnce, resetAlertStream } from "./alertStream";
import { clearUserState } from "./session";

const alerts = vi.hoisted(() => vi.fn());
vi.mock("./api", () => ({ api: { alerts } }));
vi.mock("./prefs", () => ({ clearPrefs: vi.fn() }));

// Two polls: the first only establishes the baseline, the second reports what
// arrived after it — which is what leaves per-user state behind.
async function seedAlertState() {
  alerts.mockResolvedValueOnce({ events: [{ id: 10 }], unread: 1 });
  await pollAlertsOnce();
  alerts.mockResolvedValueOnce({ events: [{ id: 12 }, { id: 11 }, { id: 10 }], unread: 3 });
  await pollAlertsOnce();
}

describe("alert stream state", () => {
  beforeEach(() => {
    resetAlertStream();
    alerts.mockReset();
  });

  it("accumulates a badge count and fresh events for the signed-in user", async () => {
    await seedAlertState();
    const p = alertPulseSnapshot();
    expect(p.unread).toBe(3);
    expect(p.fresh.map((e) => e.id)).toEqual([11, 12]); // oldest first
  });

  it("resetAlertStream forgets the counts", async () => {
    await seedAlertState();
    resetAlertStream();
    expect(alertPulseSnapshot()).toEqual({ tick: 0, unread: 0, fresh: [] });
  });

  // The subtle half: the baseline must go too. If only the counts were cleared,
  // the next user's first poll would compare against the previous user's high
  // water mark and announce nothing — or, after an ack, replay old events.
  it("resetAlertStream forgets the baseline, so the next user's first poll only establishes one", async () => {
    await seedAlertState();
    resetAlertStream();

    alerts.mockResolvedValueOnce({ events: [{ id: 12 }, { id: 11 }], unread: 2 });
    await pollAlertsOnce();
    expect(alertPulseSnapshot().fresh).toEqual([]);
  });
});

describe("clearUserState", () => {
  beforeEach(() => {
    resetAlertStream();
    alerts.mockReset();
  });

  // The bug this guards: resetAlertStream existed, was documented as "call on
  // logout", and had no call sites at all — so on a shared browser the next
  // person to sign in inherited the previous user's badge and toasts.
  it("clears the alert stream, so a logout leaves nothing for the next user", async () => {
    await seedAlertState();
    expect(alertPulseSnapshot().unread).toBe(3);

    clearUserState();

    expect(alertPulseSnapshot()).toEqual({ tick: 0, unread: 0, fresh: [] });
  });

  it("clears stored preferences too", async () => {
    const { clearPrefs } = await import("./prefs");
    clearUserState();
    expect(clearPrefs).toHaveBeenCalled();
  });
});
