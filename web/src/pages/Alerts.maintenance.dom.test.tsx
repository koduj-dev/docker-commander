/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MaintenanceWindows } from "./Alerts";
import { DialogProvider } from "../components/Dialog";
import type { MaintenanceWindow, AlertRule, Host } from "../lib/types";

// Maintenance windows suppress alert delivery — this UI is how an operator
// silences a planned-work window and, just as importantly, how they end one
// early. Ending/deleting are destructive to the paging behaviour a window
// controls, so both must go through the app's confirm dialog, never a
// one-click action, per this repo's confirm-destructive-actions convention.

const maintenanceWindows = vi.hoisted(() => vi.fn());
const createMaintenanceWindow = vi.hoisted(() => vi.fn());
const updateMaintenanceWindow = vi.hoisted(() => vi.fn());
const endMaintenanceWindow = vi.hoisted(() => vi.fn());
const deleteMaintenanceWindow = vi.hoisted(() => vi.fn());
const myAccess = vi.hoisted(() => vi.fn());
const hosts = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", () => ({
  api: {
    maintenanceWindows,
    alertRules: () => Promise.resolve([] as AlertRule[]),
    hosts,
    createMaintenanceWindow,
    updateMaintenanceWindow,
    endMaintenanceWindow,
    deleteMaintenanceWindow,
    myAccess,
  },
}));

const window1: MaintenanceWindow = {
  id: 1, name: "DB upgrade", reason: "planned Postgres bump", authorId: 1, author: "alice",
  hostIds: [], project: "", container: "", ruleId: null, severities: [],
  recurring: false,
  startsAt: new Date(Date.now() - 60_000).toISOString(),
  endsAt: new Date(Date.now() + 3_600_000).toISOString(),
  ended: false, createdAt: new Date().toISOString(),
};

// timezone: "" means UTC (the server's own convention) — this is the exact
// shape that exposed the "editing silently switches to the browser's
// timezone" bug, since "" is falsy and `existing.timezone || browserTZ`
// picks the wrong branch for it.
const recurringWindow: MaintenanceWindow = {
  id: 2, name: "Nightly backup window", reason: "planned", authorId: 1, author: "alice",
  hostIds: [], project: "", container: "", ruleId: null, severities: [],
  recurring: true, startsAt: new Date(Date.now() - 86_400_000).toISOString(),
  weekdays: [0], timeOfDay: "02:00", durationMin: 60, timezone: "",
  ended: false, createdAt: new Date().toISOString(),
};

let container: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  maintenanceWindows.mockResolvedValue([window1, recurringWindow]);
  endMaintenanceWindow.mockResolvedValue({ ok: true });
  deleteMaintenanceWindow.mockResolvedValue({ ok: true });
  createMaintenanceWindow.mockResolvedValue({ id: 3 });
  updateMaintenanceWindow.mockResolvedValue({ ok: true });
  myAccess.mockResolvedValue({ admin: false, readOnly: false, roles: [], sections: ["alerts"] });
  hosts.mockResolvedValue([{ id: 0, name: "local" }]);

  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root.render(
      <DialogProvider>
        <MaintenanceWindows />
      </DialogProvider>,
    );
  });
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

function buttons(): HTMLButtonElement[] {
  return [...container.querySelectorAll("button")] as HTMLButtonElement[];
}

function typeInto(el: Element, value: string) {
  const input = el as HTMLInputElement;
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")?.set;
  setter?.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

// Stands in for a browser whose local timezone is NOT UTC, so a test can
// tell "used the window's own tz" from "fell back to the browser's"
// regardless of what timezone the test runner itself is in. Only the no-arg
// "what's my own timezone" call (Intl.DateTimeFormat().resolvedOptions()) is
// faked; an explicit-args call (zonedDateString/fromZonedDate's actual date
// math, always called with `new`) still goes to the real formatter — must be
// a real `function`, not an arrow function, since arrow functions cannot be
// used as constructors and `new` on one throws.
function stubBrowserTimeZone(fakeTZ: string): () => void {
  const originalDTF = globalThis.Intl.DateTimeFormat;
  function fake(...args: unknown[]) {
    if (args.length === 0) return { resolvedOptions: () => ({ timeZone: fakeTZ }) };
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    return new (originalDTF as any)(...args);
  }
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (globalThis.Intl as any).DateTimeFormat = fake;
  return () => {
    globalThis.Intl.DateTimeFormat = originalDTF;
  };
}

function rowButton(rowText: string, title: string): HTMLButtonElement {
  const row = [...container.querySelectorAll("tr")].find((r) => r.textContent?.includes(rowText));
  if (!row) throw new Error(`row containing ${JSON.stringify(rowText)} not found`);
  const btn = row.querySelector(`button[title="${title}"]`);
  if (!btn) throw new Error(`button titled ${title} not found in row`);
  return btn as HTMLButtonElement;
}

describe("MaintenanceWindows", () => {
  it("puts New window on the same row as the description, like the Webhooks tab", () => {
    const btn = [...container.querySelectorAll("button")].find((b) => b.textContent?.includes("New window")) as HTMLElement;
    const desc = [...container.querySelectorAll("p")].find((p) => p.textContent?.includes("Suppress alert delivery")) as HTMLElement;
    expect(btn.parentElement).toBe(desc.parentElement);
  });

  it("lists an existing window with its name and reason", async () => {
    expect(container.textContent).toContain("DB upgrade");
    expect(container.textContent).toContain("planned Postgres bump");
  });

  it("ending a window requires confirmation and only then calls the API", async () => {
    await act(async () => rowButton("DB upgrade", "End now").click());

    expect(endMaintenanceWindow).not.toHaveBeenCalled();

    const confirm = buttons().find((b) => b.textContent === "End now");
    if (!confirm) throw new Error("confirm dialog button not found");
    await act(async () => confirm.click());

    expect(endMaintenanceWindow).toHaveBeenCalledWith(1);
  });

  it("cancelling the end-window confirm dialog never calls the API", async () => {
    await act(async () => rowButton("DB upgrade", "End now").click());

    const cancel = buttons().find((b) => b.textContent === "Cancel");
    if (!cancel) throw new Error("cancel button not found");
    await act(async () => cancel.click());

    expect(endMaintenanceWindow).not.toHaveBeenCalled();
  });

  it("deleting a window requires confirmation and only then calls the API", async () => {
    await act(async () => rowButton("DB upgrade", "Delete").click());

    expect(deleteMaintenanceWindow).not.toHaveBeenCalled();

    const confirm = buttons().find((b) => b.textContent === "Delete" && !b.title);
    if (!confirm) throw new Error("confirm dialog button not found");
    await act(async () => confirm.click());

    expect(deleteMaintenanceWindow).toHaveBeenCalledWith(1);
  });

  it("creating a one-off window submits name, reason and a start/end derived from the duration", async () => {
    const newBtn = buttons().find((b) => b.textContent?.includes("New window"));
    if (!newBtn) throw new Error("New window button not found");
    await act(async () => newBtn.click());

    const nameInput = container.querySelector('input[required]') as HTMLInputElement;
    const reasonInput = [...container.querySelectorAll("input")].find((i) => i.placeholder?.startsWith("Why")) as HTMLInputElement;
    await act(async () => {
      typeInto(nameInput, "Router reboot");
      typeInto(reasonInput, "swapping the edge router");
    });

    const form = container.querySelector("form");
    if (!form) throw new Error("form not found");
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });

    expect(createMaintenanceWindow).toHaveBeenCalledTimes(1);
    const body = createMaintenanceWindow.mock.calls[0][0];
    expect(body.name).toBe("Router reboot");
    expect(body.reason).toBe("swapping the edge router");
    expect(body.recurring).toBe(false);
    expect(new Date(body.endsAt).getTime()).toBeGreaterThan(new Date(body.startsAt).getTime());
  });

  // A regression test for a real bug: the browser used to send `endsAt: ""`
  // for an indefinite recurring series, which Go's time.Time JSON decoder
  // rejects (only a missing key or an explicit null leaves it at zero) — so
  // creating an open-ended recurring window failed outright.
  it("creating a recurring window with no series end omits endsAt entirely", async () => {
    const newBtn = buttons().find((b) => b.textContent?.includes("New window"));
    if (!newBtn) throw new Error("New window button not found");
    await act(async () => newBtn.click());

    const recurringToggle = buttons().find((b) => b.textContent === "Recurring");
    if (!recurringToggle) throw new Error("Recurring toggle not found");
    await act(async () => recurringToggle.click());

    const sunday = buttons().find((b) => b.textContent === "Sun");
    if (!sunday) throw new Error("Sunday weekday chip not found");
    await act(async () => sunday.click());

    const nameInput = container.querySelector('input[required]') as HTMLInputElement;
    const reasonInput = [...container.querySelectorAll("input")].find((i) => i.placeholder?.startsWith("Why")) as HTMLInputElement;
    await act(async () => {
      typeInto(nameInput, "Weekly maintenance");
      typeInto(reasonInput, "recurring cleanup");
    });

    const form = container.querySelector("form");
    if (!form) throw new Error("form not found");
    await act(async () => form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));

    expect(createMaintenanceWindow).toHaveBeenCalledTimes(1);
    const body = createMaintenanceWindow.mock.calls[0][0];
    expect(body.recurring).toBe(true);
    expect("endsAt" in body ? body.endsAt : undefined).toBeUndefined();
  });

  // A regression test for a real bug: editing an existing window whose
  // stored timezone is "" (the server's own convention for UTC) silently
  // switched it to the browser's own timezone, changing the window's actual
  // wall-clock schedule without the user asking for that.
  it("editing an existing recurring window preserves its stored UTC timezone, not the browser's", async () => {
    const restore = stubBrowserTimeZone("America/New_York");
    try {
      const editBtn = rowButton("Nightly backup window", "Edit");
      await act(async () => editBtn.click());

      const form = container.querySelector("form");
      if (!form) throw new Error("form not found");
      await act(async () => form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));

      expect(updateMaintenanceWindow).toHaveBeenCalledTimes(1);
      const [, body] = updateMaintenanceWindow.mock.calls[0];
      expect(body.timezone).toBe("UTC");
    } finally {
      restore();
    }
  });
});

// A regression test for a real bug: toInputDate/fromInputDate round-tripped
// a recurring window's series start/end through the BROWSER's own calendar
// date, but the server interprets that date in the WINDOW's own schedule
// timezone. Editing (and re-saving unchanged) a UTC window from a browser in
// a different timezone silently shifted its series start to a different
// absolute instant — one the server reads back as a different calendar date.
describe("MaintenanceWindows — recurring series date across timezones", () => {
  const fixedWindow: MaintenanceWindow = {
    id: 9, name: "Fixed schedule", reason: "planned", authorId: 1, author: "alice",
    hostIds: [], project: "", container: "", ruleId: null, severities: [],
    recurring: true, startsAt: "2026-09-11T00:00:00Z",
    weekdays: [5], timeOfDay: "02:00", durationMin: 60, timezone: "",
    ended: false, createdAt: "2026-09-01T00:00:00Z",
  };

  beforeEach(async () => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    maintenanceWindows.mockResolvedValue([fixedWindow]);
    hosts.mockResolvedValue([{ id: 0, name: "local" }]);
    updateMaintenanceWindow.mockResolvedValue({ ok: true });

    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => {
      root.render(
        <DialogProvider>
          <MaintenanceWindows />
        </DialogProvider>,
      );
    });
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.clearAllMocks();
  });

  it("does not shift a UTC window's series start date when edited from a non-UTC browser", async () => {
    const restore = stubBrowserTimeZone("America/New_York");
    try {
      const editBtn = rowButton("Fixed schedule", "Edit");
      await act(async () => editBtn.click());

      // Save with NO changes to the series start date field.
      const form = container.querySelector("form");
      if (!form) throw new Error("form not found");
      await act(async () => form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));

      expect(updateMaintenanceWindow).toHaveBeenCalledTimes(1);
      const [, body] = updateMaintenanceWindow.mock.calls[0];
      // Still September 11 in the window's own (UTC) timezone — not shifted
      // to the 10th by round-tripping through America/New_York.
      expect(body.startsAt.slice(0, 10)).toBe("2026-09-11");
    } finally {
      restore();
    }
  });
});

// A regression test for a real gap: the host picker used to call only
// api.hosts(), gated by the "hosts" section — which the built-in Operator
// role deliberately does NOT grant even though it does grant "alerts". Such
// a caller would see an empty picker and be unable to scope any window to a
// specific host at all.
describe("MaintenanceWindows — host picker without the hosts section", () => {
  beforeEach(async () => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    maintenanceWindows.mockResolvedValue([]);
    hosts.mockRejectedValue(new Error("forbidden"));
    myAccess.mockResolvedValue({
      admin: false, readOnly: false, roles: [], sections: ["alerts"],
      effective: [{ section: "alerts", write: true, from: [], allHosts: false, hosts: [7] }],
    });

    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => {
      root.render(
        <DialogProvider>
          <MaintenanceWindows />
        </DialogProvider>,
      );
    });
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.clearAllMocks();
  });

  it("falls back to the caller's own reachable host ids when /api/hosts is refused", async () => {
    const newBtn = [...container.querySelectorAll("button")].find((b) => b.textContent?.includes("New window"));
    if (!newBtn) throw new Error("New window button not found");
    await act(async () => newBtn.click());

    expect(container.textContent).toContain("host #7");
  });
});
