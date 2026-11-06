/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act, type ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { DialogProvider } from "../components/Dialog";
import { Topology } from "./Topology";
import { Images } from "./Images";
import { Volumes } from "./Volumes";
import { Networks } from "./Networks";
import { Containers } from "./Containers";

// A change made outside the open page — `docker run` in a terminal, a compose
// deploy, another user — must show up without a reload. These pages used to
// fetch once on mount; each now re-reads on the Docker event tick.

const tick = vi.hoisted(() => ({ value: 0, set: null as null | ((n: number) => void) }));
vi.mock("../lib/dockerEvents", async () => {
  const { useState } = await import("react");
  return {
    useDockerEventTick: () => {
      const [t, setT] = useState(tick.value);
      tick.set = setT;
      return t;
    },
  };
});

const api = vi.hoisted(() => ({
  topology: vi.fn(),
  images: vi.fn(),
  volumes: vi.fn(),
  backupJobs: vi.fn(),
  networks: vi.fn(),
  containers: vi.fn(),
  prefs: vi.fn(),
  setPrefs: vi.fn(),
  hosts: () => Promise.resolve([]),
  version: () => Promise.resolve({ version: "test" }),
}));
vi.mock("../lib/api", () => ({ api, fileApiForVolume: () => ({}) }));
vi.mock("../auth/AuthContext", () => ({
  useAuth: () => ({ user: { id: 1, username: "admin", role: "admin", readOnly: false, sections: [] } }),
}));
vi.mock("../lib/prefs", () => ({
  getPref: (key: string, fallback: unknown) => (key === "topology.prefs" ? { view: "list", showStopped: true } : fallback),
  setPref: () => {},
}));

const topo = (names: string[]) => ({
  networks: [{ id: "n1", name: "bridge", driver: "bridge", subnets: [] }],
  containers: names.map((n) => ({ id: `id-${n}`, name: n, image: "nginx", state: "running", ports: [] })),
  links: names.map((n) => ({ networkId: "n1", containerId: `id-${n}`, ipAddress: "" })),
});

let container: HTMLDivElement;
let root: Root | undefined;

async function render(el: ReactElement) {
  root = createRoot(container);
  await act(async () => root!.render(<MemoryRouter><DialogProvider>{el}</DialogProvider></MemoryRouter>));
}

async function dockerEvent() {
  await act(async () => tick.set!((tick.value += 1)));
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  tick.value = 0;
  for (const f of Object.values(api)) if (vi.isMockFunction(f)) f.mockResolvedValue([]);
  api.topology.mockResolvedValue(topo([]));
  container = document.createElement("div");
  document.body.appendChild(container);
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container.remove();
  vi.clearAllMocks();
});

describe("pages re-read on Docker events", () => {
  it("Topology shows a container started after the page opened", async () => {
    api.topology.mockResolvedValueOnce(topo(["web"])).mockResolvedValueOnce(topo(["web", "worker"]));
    await render(<Topology />);
    expect(container.textContent).toContain("web");
    expect(container.textContent).not.toContain("worker");

    await dockerEvent();
    expect(api.topology).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain("worker");
  });

  it("Topology keeps the last good graph when a refresh fails", async () => {
    api.topology.mockResolvedValueOnce(topo(["web"])).mockRejectedValueOnce(new Error("boom"));
    await render(<Topology />);
    await dockerEvent();
    expect(container.textContent).toContain("web");
  });

  it.each([
    ["Images", () => <Images />, api.images],
    ["Volumes", () => <Volumes />, api.volumes],
    ["Networks", () => <Networks />, api.networks],
    ["Containers", () => <Containers />, api.containers],
  ])("%s reloads its list", async (_name, el, fetch) => {
    await render(el());
    const before = fetch.mock.calls.length;
    expect(before).toBeGreaterThan(0);
    await dockerEvent();
    expect(fetch.mock.calls.length).toBeGreaterThan(before);
  });
});
