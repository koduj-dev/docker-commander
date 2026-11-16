/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { Troubleshooting } from "./Troubleshooting";
import { clearPrefs } from "../lib/prefs";
import type { DiagnosticsReport, InstanceDiagnostics } from "../lib/types";

// Docker Commander's own checks are admin only, shown as their own group, and
// independent of the host checks: when the host can't be reached (often the
// very problem, e.g. no access to the Docker socket) they must still show.

const runDiagnostics = vi.hoisted(() => vi.fn());
const instanceDiagnostics = vi.hoisted(() => vi.fn());
const role = vi.hoisted(() => ({ value: "admin" }));

vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return { ...actual, api: { ...actual.api, runDiagnostics, instanceDiagnostics, savePrefs: () => Promise.resolve() } };
});

vi.mock("../auth/AuthContext", () => ({
  useAuth: () => ({ user: { id: 1, username: "u", role: role.value } }),
}));

const hostReport: DiagnosticsReport = {
  hostId: 0,
  generatedAt: "2026-01-01T00:00:00Z",
  checks: [{ id: "net", name: "Network overlap", status: "ok", message: "no overlapping subnets" }],
};

const instanceReport: InstanceDiagnostics = {
  generatedAt: "2026-01-01T00:00:00Z",
  checks: [
    { id: "dc_install", name: "Installation and updates", status: "ok", message: "Installed from a package." },
    {
      id: "dc_docker",
      name: "Local Docker access",
      status: "fail",
      message: "Docker Commander isn't allowed to use the Docker socket.",
      details: ["Add the user to the docker group."],
    },
  ],
};

let container: HTMLDivElement;
let root: Root | undefined;

async function render() {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter>
        <Troubleshooting />
      </MemoryRouter>,
    );
  });
  await act(async () => {});
}

const group = (title: string) => container.querySelector(`section[aria-label="${title}"]`);

beforeEach(() => {
  clearPrefs();
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  role.value = "admin";
  runDiagnostics.mockReset().mockResolvedValue(hostReport);
  instanceDiagnostics.mockReset().mockResolvedValue(instanceReport);
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container?.remove();
});

describe("Troubleshooting: Docker Commander checks", () => {
  it("an admin sees both groups, counted together", async () => {
    await render();
    expect(group("Docker Commander")?.textContent).toContain("Installation and updates");
    expect(group("Docker Commander")?.textContent).toContain("Add the user to the docker group."); // fail opens
    expect(group("Docker host")?.textContent).toContain("Network overlap");
    expect(container.textContent).toContain("2 of 3");
  });

  it("keeps the Docker Commander checks when the host checks fail", async () => {
    runDiagnostics.mockRejectedValue(new Error("docker error: permission denied"));
    await render();
    expect(group("Docker Commander")?.textContent).toContain("Local Docker access");
    expect(group("Docker host")?.textContent).toContain("docker error: permission denied");
  });

  it("keeps the host checks when the Docker Commander checks fail", async () => {
    instanceDiagnostics.mockRejectedValue(new Error("forbidden"));
    await render();
    expect(group("Docker host")?.textContent).toContain("Network overlap");
    expect(group("Docker Commander")?.textContent).toContain("forbidden");
  });

  it("a non-admin never requests them and sees no group", async () => {
    role.value = "user";
    await render();
    expect(instanceDiagnostics).not.toHaveBeenCalled();
    expect(group("Docker Commander")).toBeNull();
    expect(container.textContent).toContain("Network overlap");
  });
});
