/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Webhooks } from "./Alerts";
import { DialogProvider } from "../components/Dialog";
import type { Webhook } from "../lib/types";

// Adding and deleting a webhook is admin-only on the server; the tab offers the
// actions only to an admin, and shows each webhook by host, never by URL.

const webhooks = vi.hoisted(() => vi.fn());
const user = vi.hoisted(() => ({ current: { id: 1, username: "admin", role: "admin", readOnly: false, sections: [] as string[] } }));

vi.mock("../auth/AuthContext", () => ({ useAuth: () => ({ user: user.current }) }));
vi.mock("../lib/api", () => ({ api: { webhooks, createWebhook: vi.fn(), deleteWebhook: vi.fn() } }));

const hook: Webhook = {
  id: 1, name: "ops", method: "POST", host: "hooks.example.com",
  headerNames: ["Authorization"], hasBodyTemplate: true, createdAt: "2026-10-15T00:00:00Z",
};

let container: HTMLDivElement;
let root: Root;

async function render() {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root.render(
      <DialogProvider>
        <Webhooks />
      </DialogProvider>,
    );
  });
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  webhooks.mockResolvedValue([hook]);
});
afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

const buttons = () => [...container.querySelectorAll("button")];

describe("Webhooks tab", () => {
  it("lets an admin add and delete, and shows the host and header names", async () => {
    user.current = { ...user.current, role: "admin" };
    await render();
    expect(buttons().some((b) => b.textContent?.includes("New webhook"))).toBe(true);
    expect(buttons().some((b) => b.title === "Delete")).toBe(true);
    expect(container.textContent).toContain("POST hooks.example.com");
    expect(container.textContent).toContain("Authorization");
    expect(container.textContent).toContain("Custom body");
  });

  it("offers a non-admin no add or delete", async () => {
    user.current = { ...user.current, role: "user", sections: ["alerts"] };
    await render();
    expect(buttons().some((b) => b.textContent?.includes("New webhook"))).toBe(false);
    expect(buttons().some((b) => b.title === "Delete")).toBe(false);
    expect(container.textContent).toContain("Only an admin can add or remove webhooks.");
    expect(container.textContent).toContain("POST hooks.example.com");
  });
});
