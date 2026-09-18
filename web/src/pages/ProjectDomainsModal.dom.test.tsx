/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ProjectDomainsModal } from "./Projects";
import { DialogProvider } from "../components/Dialog";
import { api } from "../lib/api";
import type { Project, DomainMapping } from "../lib/types";

// Phase 1 of "Per-container domain + TLS" (NEXT.md) only stores intent — no
// reverse proxy exists yet to route these domains. The modal must say so, and
// its CRUD must round-trip through the domains API without ever implying a
// live proxy is running.

vi.mock("../lib/api", () => ({
  api: {
    listDomainMappings: vi.fn(),
    listDomainMappingServices: vi.fn(),
    createDomainMapping: vi.fn(),
    updateDomainMapping: vi.fn(),
    deleteDomainMapping: vi.fn(),
  },
}));

const project: Project = { id: 1, name: "app", slug: "app", hostId: 0, composeFile: "compose.yml", createdBy: "admin" } as Project;

const mapping: DomainMapping = {
  id: 1, projectId: 1, domain: "app.example.com", service: "web", targetPort: 8080,
  tlsMode: "acme", createdBy: "admin", createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z",
};

let container: HTMLDivElement;
let root: Root | undefined;

async function renderModal() {
  await act(async () => {
    root!.render(
      <DialogProvider>
        <ProjectDomainsModal project={project} onClose={() => {}} />
      </DialogProvider>,
    );
  });
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  vi.mocked(api.listDomainMappings).mockResolvedValue([]);
  vi.mocked(api.listDomainMappingServices).mockResolvedValue({ services: ["web"] });
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container.remove();
  vi.clearAllMocks();
});

describe("ProjectDomainsModal", () => {
  it("says up front that nothing proxies traffic yet", async () => {
    await renderModal();
    expect(container.textContent).toContain("Not yet active");
  });

  it("lists existing mappings with their service:port", async () => {
    vi.mocked(api.listDomainMappings).mockResolvedValue([mapping]);
    await renderModal();
    expect(container.textContent).toContain("app.example.com");
    expect(container.textContent).toContain("web:8080");
  });

  it("populates the service select from the project's actual compose services", async () => {
    vi.mocked(api.listDomainMappingServices).mockResolvedValue({ services: ["web", "worker"] });
    await renderModal();
    const select = container.querySelector("select") as HTMLSelectElement;
    const options = [...select.options].map((o) => o.value);
    expect(options).toEqual(["web", "worker"]);
  });

  function fillAndSubmit(domain: string, port: string) {
    const domainInput = container.querySelector('input[placeholder="app.example.com"]') as HTMLInputElement;
    const portInput = container.querySelector("input[type=number]") as HTMLInputElement;
    const nativeInputSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")!.set!;
    nativeInputSetter.call(domainInput, domain);
    domainInput.dispatchEvent(new Event("input", { bubbles: true }));
    nativeInputSetter.call(portInput, port);
    portInput.dispatchEvent(new Event("input", { bubbles: true }));
    const addButton = [...container.querySelectorAll("button")].find((b) => b.textContent?.includes("Add")) as HTMLButtonElement;
    addButton.click();
  }

  it("creates a mapping and reloads the list", async () => {
    vi.mocked(api.createDomainMapping).mockResolvedValue({ id: 2 });
    await renderModal();
    await act(async () => fillAndSubmit("new.example.com", "9000"));

    expect(api.createDomainMapping).toHaveBeenCalledWith(1, {
      domain: "new.example.com", service: "web", targetPort: 9000, tlsMode: "acme",
    });
    expect(api.listDomainMappings).toHaveBeenCalledTimes(2); // initial load + reload after create
  });

  it("shows a create failure without silently dropping it", async () => {
    vi.mocked(api.createDomainMapping).mockRejectedValue(new Error("this domain is already mapped"));
    await renderModal();
    await act(async () => fillAndSubmit("dup.example.com", "80"));

    expect(container.textContent).toContain("this domain is already mapped");
  });

  it("edits a mapping's service/port, sending the domain back unchanged, and reloads", async () => {
    vi.mocked(api.listDomainMappings).mockResolvedValue([mapping]);
    vi.mocked(api.listDomainMappingServices).mockResolvedValue({ services: ["web", "worker"] });
    vi.mocked(api.updateDomainMapping).mockResolvedValue({ ok: true });
    await renderModal();

    const editButton = [...container.querySelectorAll("button")].find((b) => b.getAttribute("title") === "Edit") as HTMLButtonElement;
    await act(async () => editButton.click());

    const card = editButton.closest(".card") as HTMLElement;
    const select = card.querySelector("select") as HTMLSelectElement;
    const portInput = card.querySelector("input[type=number]") as HTMLInputElement;

    const nativeSelectSetter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, "value")!.set!;
    nativeSelectSetter.call(select, "worker");
    select.dispatchEvent(new Event("change", { bubbles: true }));
    const nativeInputSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")!.set!;
    nativeInputSetter.call(portInput, "9999");
    portInput.dispatchEvent(new Event("input", { bubbles: true }));

    const saveButton = [...card.querySelectorAll("button")].find((b) => b.type === "submit") as HTMLButtonElement;
    await act(async () => saveButton.click());

    expect(api.updateDomainMapping).toHaveBeenCalledWith(1, 1, {
      domain: "app.example.com", service: "worker", targetPort: 9999, tlsMode: "acme",
    });
    expect(api.listDomainMappings).toHaveBeenCalledTimes(2); // initial load + reload after save
  });
});
