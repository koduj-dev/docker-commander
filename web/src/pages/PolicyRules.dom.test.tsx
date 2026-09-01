/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { PolicyRules } from "./PolicyRules";

const policyRules = vi.hoisted(() => vi.fn());
const setPolicyRules = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", () => ({
  api: {
    policyRules,
    setPolicyRules,
    hosts: () => Promise.resolve([]),
  },
}));

let container: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  policyRules.mockResolvedValue({
    rules: ["privileged", "host_network", "host_pid", "docker_socket_mount", "latest_tag", "missing_resource_limits", "missing_healthcheck"],
    modes: {
      privileged: "off", host_network: "off", host_pid: "off", docker_socket_mount: "off",
      latest_tag: "off", missing_resource_limits: "off", missing_healthcheck: "off",
    },
  });
  setPolicyRules.mockResolvedValue({ ok: true });

  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root.render(
      <MemoryRouter>
        <PolicyRules />
      </MemoryRouter>,
    );
  });
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

function modeButton(rule: string, mode: string): HTMLElement {
  const row = [...container.querySelectorAll("div")].find((d) => d.textContent?.includes(rule) && d.querySelector("button"));
  const btn = row && [...row.querySelectorAll("button")].find((b) => b.textContent === mode);
  if (!btn) throw new Error(`button ${mode} for ${rule} not found`);
  return btn;
}

function saveButton(): HTMLElement {
  const el = [...container.querySelectorAll("button")].find((b) => b.textContent?.includes("Save policy rules"));
  if (!el) throw new Error("save button not found");
  return el;
}

describe("PolicyRules", () => {
  it("renders every rule defaulted to off", async () => {
    expect(container.textContent).toContain("Privileged containers");
    expect(container.textContent).toContain("Missing healthcheck");
  });

  it("selecting a mode and saving sends the full modes map", async () => {
    await act(async () => modeButton("Privileged containers", "Block").click());
    await act(async () => saveButton().click());

    expect(setPolicyRules).toHaveBeenCalledWith(expect.objectContaining({ privileged: "block" }));
  });

  it("shows a failed save distinctly from a successful one", async () => {
    setPolicyRules.mockRejectedValue(new Error("nope"));
    await act(async () => saveButton().click());
    const msg = [...container.querySelectorAll("span")].find((s) => s.textContent?.includes("Save failed"));
    expect(msg).toBeTruthy();
    expect(msg!.className).toContain("text-danger");
  });
});
