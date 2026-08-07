/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { ContainerTable } from "./Containers";
import { DialogProvider } from "../components/Dialog";

// Kill is SIGKILL: no shutdown handler runs, nothing in flight is flushed. Stop
// asks politely and waits. They are not interchangeable, and the button sits
// beside Stop — so it goes through the app's own confirm dialog, never one click,
// which is the rule every other destructive action here follows.

const containers = vi.hoisted(() => vi.fn());
const containerAction = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", () => ({
  api: {
    containers,
    containerAction,
    hosts: () => Promise.resolve([]),
    version: () => Promise.resolve({ version: "test" }),
  },
}));

let container: HTMLDivElement;
let root: Root | undefined;

function button(title: string): HTMLButtonElement {
  const el = [...container.querySelectorAll("button")].find((b) => b.getAttribute("title")?.includes(title));
  if (!el) throw new Error(`no button titled ${title}`);
  return el as HTMLButtonElement;
}

// The dialog renders into the same container; its confirm button carries the
// label we asked for.
function dialogButton(text: string): HTMLButtonElement | undefined {
  return [...container.querySelectorAll("form button")].find((b) => b.textContent?.trim() === text) as HTMLButtonElement | undefined;
}

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  containers.mockResolvedValue([
    { id: "abc123def456", name: "web", image: "nginx:latest", state: "running", status: "Up 3 hours", networks: [], ports: [] },
  ]);
  containerAction.mockResolvedValue({ ok: true });
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(<MemoryRouter><DialogProvider><ContainerTable /></DialogProvider></MemoryRouter>);
  });
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container.remove();
  vi.clearAllMocks();
});

describe("killing a container", () => {
  it("offers Kill beside Stop for a running container", () => {
    expect(() => button("Kill")).not.toThrow();
    expect(() => button("Stop")).not.toThrow();
  });

  it("asks before killing, and does nothing until confirmed", async () => {
    await act(async () => button("Kill").click());

    // The request must not have gone out yet.
    expect(containerAction).not.toHaveBeenCalled();
    expect(container.textContent).toContain("SIGKILL");
  });

  it("kills once confirmed", async () => {
    await act(async () => button("Kill").click());
    const confirm = dialogButton("Kill");
    expect(confirm, "the dialog should offer a Kill button").toBeDefined();

    await act(async () => confirm!.click());
    expect(containerAction).toHaveBeenCalledWith("abc123def456", "kill");
  });

  it("does not kill when the dialog is dismissed", async () => {
    await act(async () => button("Kill").click());
    const cancel = dialogButton("Cancel");
    expect(cancel, "the dialog should offer a way out").toBeDefined();

    await act(async () => cancel!.click());
    expect(containerAction).not.toHaveBeenCalled();
  });

  it("still stops in one click — the confirmation is for Kill only", async () => {
    await act(async () => button("Stop").click());
    expect(containerAction).toHaveBeenCalledWith("abc123def456", "stop");
  });
});
