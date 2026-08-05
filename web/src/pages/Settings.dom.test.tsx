/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { Settings } from "./Settings";

// Both cards on this page save through one endpoint, which is why the status
// message used to be a single string shown by both: saving Enabled features lit
// up the 2FA card in Security as well — with wording about nav changes that says
// nothing true about the exemption.

const settings = vi.hoisted(() => vi.fn());
const setSettings = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", () => ({
  api: {
    settings,
    setSettings,
    // PageHeader renders the active-host badge, and the LDAP/Email tabs mount
    // their own components — none of that is under test here, but it must not
    // explode when React renders it.
    hosts: () => Promise.resolve([]),
    version: () => Promise.resolve({ version: "test" }),
    ldapConfig: () => Promise.resolve({}),
    smtpConfig: () => Promise.resolve({}),
    // The Security tab also renders the MCP token-lifetime editor.
    mcpAdminTokenPolicy: () => Promise.resolve({ defaultDays: 30, maxDays: 365, allowUnlimited: false }),
  },
}));

let container: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  settings.mockResolvedValue({
    allSections: ["dashboard", "containers"],
    disabledSections: [],
    localhostNo2fa: false,
  });
  setSettings.mockResolvedValue({ ok: true });

  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root.render(
      <MemoryRouter>
        <Settings />
      </MemoryRouter>,
    );
  });
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

function tab(name: string): HTMLElement {
  const el = [...container.querySelectorAll("button")].find((b) => b.textContent?.includes(name));
  if (!el) throw new Error(`tab ${name} not found`);
  return el;
}

function saveButton(): HTMLElement {
  const el = [...container.querySelectorAll("button")].find((b) => b.textContent?.includes("Save settings"));
  if (!el) throw new Error("save button not found");
  return el;
}

describe("Settings save feedback", () => {
  it("shows the result on the card that saved", async () => {
    await act(async () => saveButton().click());
    expect(container.textContent).toContain("nav changes");
  });

  it("does not carry the Features message over to Security", async () => {
    await act(async () => saveButton().click());
    expect(container.textContent).toContain("nav changes");

    await act(async () => tab("Security").click());
    expect(container.textContent).not.toContain("nav changes");
    expect(container.textContent).not.toContain("Saved.");
  });

  it("shows a failed save in the failure colour, not the success one", async () => {
    setSettings.mockRejectedValue(new Error("server said no"));
    await act(async () => saveButton().click());

    const msg = [...container.querySelectorAll("span")].find((s) => s.textContent?.includes("Save failed"));
    expect(msg).toBeTruthy();
    // Green for a save that did not happen is worse than no message: it tells the
    // admin the setting is live when it is not.
    expect(msg!.className).toContain("text-danger");
    expect(msg!.className).not.toContain("text-ok");
  });

  it("gives Security its own wording, which stays out of Features", async () => {
    await act(async () => tab("Security").click());
    await act(async () => saveButton().click());
    expect(container.textContent).toContain("next sign-in");

    await act(async () => tab("Features").click());
    expect(container.textContent).not.toContain("next sign-in");
  });
});
