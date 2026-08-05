/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { Profile } from "./Profile";
import { DialogProvider } from "../components/Dialog";

// The authenticator list is where a lockout happens if the wiring is wrong.
//
// Two things are pinned: the only remaining factor cannot be removed (the button
// is disabled AND the reason is on screen — a dead button with no explanation is
// its own bug), and removal asks for the password in a real password field rather
// than a text input that would put it on screen.

const factors = vi.hoisted(() => vi.fn());
const removeFactor = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", () => ({
  api: {
    factors,
    removeFactor,
    sessions: () => Promise.resolve([]),
    myAccess: () => Promise.resolve({ admin: false, effective: [] }),
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

const PHONE = { id: 7, kind: "totp", name: "Phone", createdAt: "2026-07-01T09:00:00Z", lastUsedAt: "2026-08-05T09:00:00Z" };
const TABLET = { id: 8, kind: "totp", name: "Tablet", createdAt: "2026-08-01T09:00:00Z", lastUsedAt: "0001-01-01T00:00:00Z" };

let container: HTMLDivElement;
let root: Root | undefined;

function buttons(): HTMLButtonElement[] {
  return [...container.querySelectorAll("button")] as HTMLButtonElement[];
}

function removeButton(name: string): HTMLButtonElement {
  const row = [...container.querySelectorAll("li")].find((li) => li.textContent?.includes(name));
  if (!row) throw new Error(`row ${name} not found`);
  const btn = [...row.querySelectorAll("button")].at(-1);
  if (!btn) throw new Error(`no remove button in row ${name}`);
  return btn as HTMLButtonElement;
}

async function mount() {
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
  const security = buttons().find((b) => b.textContent?.includes("Security"));
  await act(async () => security!.click());
}

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  factors.mockResolvedValue([PHONE, TABLET]);
  removeFactor.mockResolvedValue({ ok: true });
  await mount();
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container.remove();
  vi.clearAllMocks();
});

describe("Profile → Security → authenticators", () => {
  it("lists each paired authenticator by name", () => {
    expect(container.textContent).toContain("Phone");
    expect(container.textContent).toContain("Tablet");
    // One that has never produced a code says so, rather than showing the zero time.
    expect(container.textContent).toContain("never used");
    expect(container.textContent).not.toContain("0001");
  });

  it("asks for the password in a password field, and sends it", async () => {
    await act(async () => removeButton("Phone").click());

    const input = container.querySelector("#remove-pw-7") as HTMLInputElement;
    expect(input).toBeTruthy();
    // A text input here would put the password on screen — the reason this is not
    // the shared confirm dialog.
    expect(input.type).toBe("password");

    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")?.set;
    await act(async () => {
      setter?.call(input, "correcthorse123");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const form = input.closest("form")!;
    await act(async () => form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));

    expect(removeFactor).toHaveBeenCalledWith(7, "correcthorse123");
  });

  it("does not remove anything just because the button was clicked", async () => {
    await act(async () => removeButton("Tablet").click());
    expect(removeFactor).not.toHaveBeenCalled();
  });

  it("refuses to remove the only remaining authenticator, and says why", async () => {
    factors.mockResolvedValue([PHONE]);
    await mount();

    expect(removeButton("Phone").disabled).toBe(true);
    expect(container.textContent).toContain("only second factor");
  });

  it("surfaces the server's refusal instead of pretending it worked", async () => {
    removeFactor.mockRejectedValue(new Error("password required to remove an authenticator"));
    await act(async () => removeButton("Phone").click());

    const input = container.querySelector("#remove-pw-7") as HTMLInputElement;
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")?.set;
    await act(async () => {
      setter?.call(input, "wrong");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => input.closest("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));

    expect(container.textContent).toContain("password required");
    // …and the row is still there.
    expect(container.textContent).toContain("Phone");
  });
});
