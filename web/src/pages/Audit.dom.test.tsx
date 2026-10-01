/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Audit, hostLabel } from "./Audit";
import type { AuditEntry } from "../lib/types";

// The audit log stores what happened (detail) and where (host) for every entry.
// The page used to show neither, so "how did this login happen" and "on which
// server was this container stopped" weren't answerable from the UI.

const entries: AuditEntry[] = [
  { id: 3, username: "", action: "auth.login", target: "admin", detail: "password + 2fa", ip: "10.0.0.5", hostId: 0, createdAt: "2026-09-30T10:00:00Z" },
  { id: 2, username: "admin", action: "container.stop", target: "4d75a58e5fdc", detail: "", ip: "127.0.0.1", hostId: 2, createdAt: "2026-09-30T09:00:00Z" },
  { id: 1, username: "admin", action: "container.start", target: "aa11", detail: "", ip: "127.0.0.1", hostId: 9, createdAt: "2026-09-30T08:00:00Z" },
];

vi.mock("../lib/api", () => ({
  api: {
    audit: () => Promise.resolve(entries),
    hosts: () => Promise.resolve([{ id: 2, name: "prod-eu", kind: "ssh" }]),
  },
}));
vi.mock("../layout/Shell", () => ({ PageHeader: ({ title }: { title: string }) => <h1>{title}</h1> }));

let container: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  localStorage.clear();
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => { root.render(<Audit />); });
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

function rowText(target: string): string {
  const row = [...container.querySelectorAll("tbody tr")].find((r) => r.textContent?.includes(target));
  return row?.textContent ?? "";
}

describe("Audit page", () => {
  it("shows each entry's detail", () => {
    expect(rowText("admin")).toContain("password + 2fa");
  });

  it("names the host, shows — for none and #id for a host it can't see", () => {
    expect(rowText("4d75a58e5fdc")).toContain("prod-eu");
    expect(rowText("aa11")).toContain("#9");
    expect(hostLabel(0, new Map())).toBe("—");
  });

  it("searches the detail and the host name", async () => {
    const input = container.querySelector("input") as HTMLInputElement;
    const type = async (v: string) => {
      await act(async () => {
        const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
        set.call(input, v);
        input.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    await type("2fa");
    expect(container.querySelectorAll("tbody tr")).toHaveLength(1);
    expect(rowText("admin")).toContain("password + 2fa");

    await type("prod-eu");
    expect(container.querySelectorAll("tbody tr")).toHaveLength(1);
    expect(rowText("4d75a58e5fdc")).toContain("prod-eu");
  });
});
