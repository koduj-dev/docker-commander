/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { act, type ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ComposeSummaryModal } from "./Projects";
import type { ComposeModel, Stack } from "../lib/types";

// Renders the "Summary" modal (services/ports/volumes overview) with a live
// stack + the profiles actually used at the last deploy, and checks that a
// service excluded by those profiles reads as "not in active profile" — not
// "Stopped", which is the wrong-but-common answer this feature exists to fix.

const model: ComposeModel = {
  services: {
    web: { image: "nginx:alpine" },
    worker: { image: "alpine:latest", profiles: ["extra"] },
    cache: { image: "redis:alpine", profiles: ["extra"] },
  },
};

function stackWith(containers: Stack["containers"]): Stack {
  return { project: "app", containers, running: containers.filter((c) => c.state === "running").length };
}

let container: HTMLDivElement;
let root: Root | undefined;

function render(ui: ReactElement) {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  act(() => { root!.render(ui); });
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container?.remove();
});

// The service block for `name` — used to scope text queries to one service.
function serviceBlock(name: string): HTMLElement {
  const label = [...container.querySelectorAll("span")].find((s) => s.textContent === name);
  if (!label) throw new Error(`no service block for ${name}`);
  return label.closest("div")!.parentElement as HTMLElement;
}

describe("ComposeSummaryModal — per-service state vs. active profiles", () => {
  it("a running service reads Running", () => {
    const stack = stackWith([{ id: "1", name: "app-web-1", service: "web", state: "running", status: "Up", image: "nginx:alpine" }]);
    render(<ComposeSummaryModal model={model} stack={stack} lastDeployedProfiles={[]} onClose={() => {}} />);
    expect(serviceBlock("web").textContent).toContain("running");
  });

  it("a service excluded by the DEPLOYED profiles reads 'Not in active profile', not Stopped", () => {
    // "worker" needs the "extra" profile, which was NOT part of the last deploy.
    const stack = stackWith([{ id: "1", name: "app-web-1", service: "web", state: "running", status: "Up", image: "nginx:alpine" }]);
    render(<ComposeSummaryModal model={model} stack={stack} lastDeployedProfiles={[]} onClose={() => {}} />);
    const text = serviceBlock("worker").textContent ?? "";
    expect(text).toContain("Not in active profile");
    expect(text).not.toContain("Stopped");
  });

  it("the SAME service reads Stopped once its profile was actually deployed but its container is missing", () => {
    // No container for "worker", but "extra" WAS used at deploy — so a missing
    // container now means it crashed or was manually stopped, not "excluded".
    const stack = stackWith([{ id: "1", name: "app-web-1", service: "web", state: "running", status: "Up", image: "nginx:alpine" }]);
    render(<ComposeSummaryModal model={model} stack={stack} lastDeployedProfiles={["extra"]} onClose={() => {}} />);
    const text = serviceBlock("worker").textContent ?? "";
    expect(text).toContain("Stopped");
    expect(text).not.toContain("Not in active profile");
  });

  it("with no stack at all (never deployed), every service reads 'Not deployed', not Stopped or excluded", () => {
    render(<ComposeSummaryModal model={model} stack={undefined} lastDeployedProfiles={[]} onClose={() => {}} />);
    for (const name of ["web", "worker", "cache"]) {
      const text = serviceBlock(name).textContent ?? "";
      expect(text).toContain("Not deployed");
      expect(text).not.toContain("Stopped");
      expect(text).not.toContain("Not in active profile");
    }
  });
});
