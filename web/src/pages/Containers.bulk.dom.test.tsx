/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { ContainerTable } from "./Containers";
import { DialogProvider } from "../components/Dialog";

// Bulk restart/stop across a multi-selection: checkboxes + "select all" gated
// behind withControls (so the dashboard's ContainerTable embed, which renders
// with withControls=false, never grows them), a preview inside the app's own
// confirm dialog before anything fires, and a per-container success/failure
// summary afterwards — never a one-click bulk action.

const containers = vi.hoisted(() => vi.fn());
const bulkContainerAction = vi.hoisted(() => vi.fn());
const bulkPullImagesUrl = vi.hoisted(() => vi.fn(() => "ws://test/bulk-pull"));

vi.mock("../lib/api", () => ({
  api: {
    containers,
    containerAction: vi.fn().mockResolvedValue({ ok: true }),
    bulkContainerAction,
    bulkPullImagesUrl,
    hosts: () => Promise.resolve([]),
    version: () => Promise.resolve({ version: "test" }),
  },
}));

// A minimal fake WebSocket so BulkPullModal's flow can be driven frame by
// frame without a real network — the modal only ever uses onopen/onmessage/
// onerror/onclose, send() and close(), so that's all this needs to implement.
class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: (() => void) | null = null;
  closed = false;
  sent: string[] = [];
  constructor(_url: string) {
    FakeWebSocket.instances.push(this);
    // Deferred so the caller's onopen assignment (right after `new
    // WebSocket(...)`) runs first, same ordering a real connection has.
    queueMicrotask(() => this.onopen?.());
  }
  send(data: string) {
    this.sent.push(data);
  }
  close() {
    if (this.closed) return;
    this.closed = true;
    this.onclose?.();
  }
  emit(frame: unknown) {
    this.onmessage?.({ data: JSON.stringify(frame) });
  }
}

const TWO_CONTAINERS = [
  { id: "aaa111", name: "web", image: "nginx:latest", state: "running", status: "Up 3 hours", networks: [], ports: [] },
  { id: "bbb222", name: "db", image: "postgres:16", state: "running", status: "Up 3 hours", networks: [], ports: [] },
];

let container: HTMLDivElement;
let root: Root | undefined;

function checkbox(label: string): HTMLInputElement {
  const el = container.querySelector(`input[aria-label="${label}"]`);
  if (!el) throw new Error(`no checkbox aria-labelled ${label}`);
  return el as HTMLInputElement;
}

function button(text: string): HTMLButtonElement {
  const el = [...container.querySelectorAll("button")].find((b) => b.textContent?.includes(text));
  if (!el) throw new Error(`no button containing ${text}`);
  return el as HTMLButtonElement;
}

// The confirm/alert dialog renders into the same container; its action button
// carries the label we asked for.
function dialogButton(text: string): HTMLButtonElement | undefined {
  return [...container.querySelectorAll("form button")].find((b) => b.textContent?.trim() === text) as HTMLButtonElement | undefined;
}

// The dialog's own text, scoped away from the table underneath it — the table
// always lists every container regardless of selection, so asserting against
// the whole page would pass even if the preview named the wrong containers.
function dialogText(): string {
  return container.querySelector("form")?.textContent ?? "";
}

async function renderTable() {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(<MemoryRouter><DialogProvider><ContainerTable withControls /></DialogProvider></MemoryRouter>);
  });
}

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  containers.mockResolvedValue(TWO_CONTAINERS);
  bulkContainerAction.mockResolvedValue({
    results: [{ id: "aaa111", ok: true }, { id: "bbb222", ok: true }],
    succeeded: 2,
    failed: 0,
  });
  FakeWebSocket.instances = [];
  vi.stubGlobal("WebSocket", FakeWebSocket);
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container.remove();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

describe("bulk select is gated behind withControls", () => {
  it("adds no checkboxes when withControls is not set (e.g. the dashboard embed)", async () => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => {
      root!.render(<MemoryRouter><DialogProvider><ContainerTable /></DialogProvider></MemoryRouter>);
    });
    expect(container.querySelectorAll('input[type="checkbox"]').length).toBe(0);
  });

  it("shows a row checkbox per container plus a select-all when withControls is set", async () => {
    await renderTable();
    expect(() => checkbox("Select all containers")).not.toThrow();
    expect(() => checkbox("Select web")).not.toThrow();
    expect(() => checkbox("Select db")).not.toThrow();
  });
});

describe("selection and the bulk toolbar", () => {
  it("shows no toolbar until something is selected", async () => {
    await renderTable();
    expect(container.textContent).not.toContain("selected");
  });

  it("selecting one row shows a toolbar with a count of 1", async () => {
    await renderTable();
    await act(async () => checkbox("Select web").click());
    expect(container.textContent).toContain("1 selected");
  });

  it("select-all selects every visible row", async () => {
    await renderTable();
    await act(async () => checkbox("Select all containers").click());
    expect(container.textContent).toContain("2 selected");
    expect(checkbox("Select web").checked).toBe(true);
    expect(checkbox("Select db").checked).toBe(true);
  });

  it("Clear empties the selection and hides the toolbar", async () => {
    await renderTable();
    await act(async () => checkbox("Select all containers").click());
    await act(async () => button("Clear").click());
    expect(container.textContent).not.toContain("selected");
  });
});

describe("bulk restart/stop preview + confirm", () => {
  it("previews exactly the selected containers and does not call the API until confirmed", async () => {
    await renderTable();
    await act(async () => checkbox("Select web").click());
    await act(async () => button("Restart").click());

    expect(bulkContainerAction).not.toHaveBeenCalled();
    expect(dialogText()).toContain("web");
    expect(dialogText()).not.toContain("db"); // only the selected one is previewed
  });

  it("does nothing when the confirm dialog is dismissed", async () => {
    await renderTable();
    await act(async () => checkbox("Select all containers").click());
    await act(async () => button("Stop").click());
    const cancel = dialogButton("Cancel");
    expect(cancel, "the dialog should offer a way out").toBeDefined();

    await act(async () => cancel!.click());
    expect(bulkContainerAction).not.toHaveBeenCalled();
  });

  it("calls bulkContainerAction with the selected ids and action once confirmed", async () => {
    await renderTable();
    await act(async () => checkbox("Select all containers").click());
    await act(async () => button("Restart").click());
    const confirm = dialogButton("Restart");
    expect(confirm, "the dialog should offer a Restart button").toBeDefined();

    await act(async () => confirm!.click());
    expect(bulkContainerAction).toHaveBeenCalledWith(["aaa111", "bbb222"], "restart");
  });

  it("marks the Stop confirmation as danger-styled, matching every other destructive action here", async () => {
    await renderTable();
    await act(async () => checkbox("Select web").click());
    await act(async () => button("Stop").click());
    const confirm = dialogButton("Stop");
    expect(confirm?.className).toContain("btn-danger");
  });
});

describe("post-action summary", () => {
  it("shows a per-container success/failure summary, not just a single toast", async () => {
    bulkContainerAction.mockResolvedValue({
      results: [
        { id: "aaa111", ok: true },
        { id: "bbb222", ok: false, error: "container is already stopped" },
      ],
      succeeded: 1,
      failed: 1,
    });
    await renderTable();
    await act(async () => checkbox("Select all containers").click());
    await act(async () => button("Stop").click());
    await act(async () => dialogButton("Stop")!.click());

    // The summary dialog names both containers individually and surfaces the
    // failure's error text — a caller can't tell that from a single toast.
    const summary = dialogText();
    expect(summary).toContain("web");
    expect(summary).toContain("db");
    expect(summary).toContain("container is already stopped");
    expect(summary).toContain("1 succeeded, 1 failed");
  });

  it("clears the selection after the summary is dismissed", async () => {
    await renderTable();
    await act(async () => checkbox("Select all containers").click());
    await act(async () => button("Restart").click());
    await act(async () => dialogButton("Restart")!.click());
    // The alert's OK button closes the summary.
    const ok = dialogButton("OK");
    expect(ok).toBeDefined();
    await act(async () => ok!.click());

    expect(container.textContent).not.toContain("selected");
  });
});

describe("bulk start", () => {
  it("previews, confirms, and calls bulkContainerAction with the start action", async () => {
    await renderTable();
    await act(async () => checkbox("Select all containers").click());
    await act(async () => button("Start").click());
    expect(bulkContainerAction).not.toHaveBeenCalled();
    expect(dialogText()).toContain("web");
    expect(dialogText()).toContain("db");

    const confirm = dialogButton("Start");
    expect(confirm, "the dialog should offer a Start button").toBeDefined();
    await act(async () => confirm!.click());
    expect(bulkContainerAction).toHaveBeenCalledWith(["aaa111", "bbb222"], "start");
  });

  it("is not danger-styled, unlike Stop", async () => {
    await renderTable();
    await act(async () => checkbox("Select web").click());
    await act(async () => button("Start").click());
    const confirm = dialogButton("Start");
    expect(confirm?.className).not.toContain("btn-danger");
  });
});

describe("bulk pull", () => {
  it("previews the DISTINCT images behind the selection before doing anything", async () => {
    await renderTable();
    await act(async () => checkbox("Select all containers").click());
    await act(async () => button("Pull").click());
    expect(dialogText()).toContain("nginx:latest");
    expect(dialogText()).toContain("postgres:16");
    expect(FakeWebSocket.instances).toHaveLength(0); // not connected until confirmed
  });

  it("does not connect when the confirm dialog is dismissed", async () => {
    await renderTable();
    await act(async () => checkbox("Select web").click());
    await act(async () => button("Pull").click());
    await act(async () => dialogButton("Cancel")!.click());
    expect(FakeWebSocket.instances).toHaveLength(0);
  });

  it("streams per-image progress and shows a final per-container summary, without restarting anything", async () => {
    await renderTable();
    await act(async () => checkbox("Select all containers").click());
    await act(async () => button("Pull").click());
    await act(async () => dialogButton("Pull")!.click());

    expect(FakeWebSocket.instances).toHaveLength(1);
    const ws = FakeWebSocket.instances[0];

    // Ids travel as the first message once the socket is open, not in the
    // connection URL (200 full container ids wouldn't reliably fit there
    // through a reverse proxy).
    await act(async () => {});
    expect(ws.sent).toEqual([JSON.stringify({ ids: ["aaa111", "bbb222"] })]);

    await act(async () => {
      ws.emit({ ref: "nginx:latest", index: 1, count: 2, started: true });
      ws.emit({ ref: "nginx:latest", index: 1, count: 2, progress: { id: "layer1", status: "Downloading", current: 50, total: 100 } });
      ws.emit({ ref: "nginx:latest", index: 1, count: 2, refDone: true, ok: true });
      ws.emit({ ref: "postgres:16", index: 2, count: 2, started: true });
      ws.emit({ ref: "postgres:16", index: 2, count: 2, refDone: true, ok: false, error: "no such host" });
      ws.emit({
        allDone: true,
        results: [
          { ref: "nginx:latest", ok: true, containerIds: ["aaa111"] },
          { ref: "postgres:16", ok: false, error: "no such host", containerIds: ["bbb222"] },
        ],
      });
    });

    expect(container.textContent).toContain("nginx:latest");
    expect(container.textContent).toContain("postgres:16");
    expect(container.textContent).toContain("no such host");
    // The per-container summary names both containers by name, not just by ref.
    expect(container.textContent).toContain("web");
    expect(container.textContent).toContain("db");
    // bulkContainerAction (restart/stop/start) must never be called by a pull.
    expect(bulkContainerAction).not.toHaveBeenCalled();
  });

  it("Cancel closes the WebSocket before it finishes", async () => {
    await renderTable();
    await act(async () => checkbox("Select web").click());
    await act(async () => button("Pull").click());
    await act(async () => dialogButton("Pull")!.click());

    const ws = FakeWebSocket.instances[0];
    expect(ws.closed).toBe(false);
    await act(async () => button("Cancel").click());
    expect(ws.closed).toBe(true);
    // An intentional Cancel is not a connection failure — must not show the
    // "connection closed before finishing" error a real drop would.
    expect(container.textContent).not.toContain("closed before finishing");
  });
});
