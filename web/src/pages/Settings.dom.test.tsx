/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter, useLocation } from "react-router-dom";
import { Settings } from "./Settings";
import { DialogProvider } from "../components/Dialog";

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
    // The Security tab also renders the MCP token-lifetime editor and the
    // self-update auto-apply policy editor.
    mcpAdminTokenPolicy: () => Promise.resolve({ defaultDays: 30, maxDays: 365, allowUnlimited: false }),
    // Policy rules, MCP Admin and Recovery bundle are tabs of this page.
    policyRules: () => Promise.resolve({ rules: ["privileged"], modes: { privileged: "warn" } }),
    mcpAdminTokens: () => Promise.resolve([]),
    mcpAdminOAuthClients: () => Promise.resolve([]),
    mcpAdminSessions: () => Promise.resolve([]),
    updateStatus: () => Promise.resolve({ updateAvailable: false, selfUpdate: true, selfUpdatePolicy: { enabled: false, granularity: "minor" } }),
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
        <DialogProvider><Settings /></DialogProvider>
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

// Reports the router's current search string, so a test can see what a tab
// click wrote to the URL (the URL is how a link or reload lands on a tab).
function SearchProbe() {
  return <div data-testid="search">{useLocation().search}</div>;
}
const search = () => container.querySelector('[data-testid="search"]')?.textContent;

describe("Settings hosts the admin pages as tabs", () => {
  it("writes the chosen tab to the URL, and the default tab leaves it clean", async () => {
    act(() => root.unmount());
    root = createRoot(container);
    await act(async () => {
      root.render(<MemoryRouter><DialogProvider><Settings /><SearchProbe /></DialogProvider></MemoryRouter>);
    });
    expect(search()).toBe("");
    await act(async () => tab("Policy rules").click());
    expect(search()).toBe("?tab=policy");
    await act(async () => tab("MCP Admin").click());
    expect(search()).toBe("?tab=mcp");
    await act(async () => tab("Features").click());
    expect(search()).toBe("");
  });

  it("ignores an unknown ?tab= and falls back to Features", async () => {
    act(() => root.unmount());
    root = createRoot(container);
    await act(async () => {
      root.render(<MemoryRouter initialEntries={["/settings?tab=nope"]}><DialogProvider><Settings /></DialogProvider></MemoryRouter>);
    });
    expect(container.textContent).toContain("Enabled features");
  });

  const headings = () => [...container.querySelectorAll("h1")].map((h) => h.textContent?.trim());

  it("shows Policy rules, MCP Admin and Recovery bundle without a second page header", async () => {
    for (const [name, marker] of [["Policy rules", "Checked against every project deploy"], ["MCP Admin", "API tokens"], ["Recovery bundle", "Export"]] as const) {
      await act(async () => tab(name).click());
      expect(container.textContent, name).toContain(marker);
      // One page, one header: the embedded page must not bring its own.
      expect(headings(), name).toEqual(["Settings"]);
    }
  });

  it("opens the tab named in the URL", async () => {
    act(() => root.unmount());
    root = createRoot(container);
    await act(async () => {
      root.render(<MemoryRouter initialEntries={["/settings?tab=policy"]}><DialogProvider><Settings /></DialogProvider></MemoryRouter>);
    });
    expect(container.textContent).toContain("Checked against every project deploy");
  });
});
