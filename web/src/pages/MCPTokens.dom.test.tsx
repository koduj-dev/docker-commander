/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { MCPTokens } from "./MCPTokens";
import { DialogProvider } from "../components/Dialog";
import type { MCPSession, MCPToken } from "../lib/types";

// The self-service Sessions tab is the user-facing half of per-session MCP
// revocation: it must show what a connector session actually IS (which
// client, from where), and revoking one must go through the same
// destructive-confirm dialog every other revoke/delete in this app uses —
// never a one-click action — per this repo's own confirm-destructive-actions
// convention.

const mcpTokens = vi.hoisted(() => vi.fn());
const mcpSessions = vi.hoisted(() => vi.fn());
const revokeMcpSession = vi.hoisted(() => vi.fn());

vi.mock("../lib/api", () => ({
  api: {
    mcpTokens,
    mcpSessions,
    mcpStatus: () => Promise.resolve({ enabled: true, oauth: true, tokenPolicy: { defaultDays: 30, maxDays: 365, allowUnlimited: false } }),
    deleteMcpToken: vi.fn(),
    revokeMcpSession,
    createMcpToken: vi.fn(),
    hosts: () => Promise.resolve([]),
  },
}));

vi.mock("../auth/AuthContext", () => ({
  useAuth: () => ({ user: { id: 1, username: "alice", role: "user", readOnly: false, sections: [] } }),
}));

const session: MCPSession = {
  id: "sess-1", clientId: "dcmcp_x", clientName: "Claude Desktop",
  ip: "203.0.113.5", userAgent: "claude-desktop/1.0",
  createdAt: "2026-08-01T09:00:00Z", lastUsedAt: "2026-09-01T09:00:00Z", expiresAt: "2026-10-01T09:00:00Z",
};
const token: MCPToken = {
  id: 1, name: "my token", sections: [], readOnly: false, createdAt: "2026-08-01T09:00:00Z",
};

let container: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  mcpTokens.mockResolvedValue([token]);
  mcpSessions.mockResolvedValue([session]);
  revokeMcpSession.mockResolvedValue({ ok: true });

  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root.render(
      <MemoryRouter>
        <DialogProvider>
          <MCPTokens />
        </DialogProvider>
      </MemoryRouter>,
    );
  });
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

function buttons(): HTMLButtonElement[] {
  return [...container.querySelectorAll("button")] as HTMLButtonElement[];
}

function clickByText(text: string) {
  const btn = buttons().find((b) => b.textContent?.includes(text));
  if (!btn) throw new Error(`button ${text} not found`);
  return act(async () => btn.click());
}

// Tab sections stay mounted (hidden via a CSS class, not removed from the DOM
// — see MCPAdmin/BackupJobs for the same pattern), so a button lookup must be
// scoped to the card that actually names the session, not just "the first
// button titled Revoke" — the token list has one of those too.
function sessionRevokeButton(): HTMLButtonElement {
  const card = [...container.querySelectorAll(".card")].find((c) => c.textContent?.includes("Claude Desktop"));
  if (!card) throw new Error("session card not found");
  const btn = card.querySelector('button[title="Revoke"]');
  if (!btn) throw new Error("revoke button not found in session card");
  return btn as HTMLButtonElement;
}

describe("MCPTokens — Sessions tab", () => {
  it("switching to Sessions reveals the connector session card", async () => {
    const sessionsSection = [...container.querySelectorAll("section")].find((s) => s.textContent?.includes("Claude Desktop"));
    if (!sessionsSection) throw new Error("sessions section not found");
    expect(sessionsSection.classList.contains("hidden")).toBe(true);

    await clickByText("Sessions");

    expect(sessionsSection.classList.contains("hidden")).toBe(false);
    expect(sessionsSection.textContent).toContain("203.0.113.5");
  });

  it("revoking a session requires confirmation and only then calls the API", async () => {
    await clickByText("Sessions");

    await act(async () => sessionRevokeButton().click());

    // The API must not be called before the confirm dialog is answered.
    expect(revokeMcpSession).not.toHaveBeenCalled();

    const confirm = buttons().find((b) => b.textContent === "Revoke" && !b.title);
    if (!confirm) throw new Error("confirm dialog button not found");
    await act(async () => confirm.click());

    expect(revokeMcpSession).toHaveBeenCalledWith("sess-1");
  });

  it("cancelling the confirm dialog never calls the API", async () => {
    await clickByText("Sessions");
    await act(async () => sessionRevokeButton().click());

    const cancel = buttons().find((b) => b.textContent === "Cancel");
    if (!cancel) throw new Error("cancel button not found");
    await act(async () => cancel.click());

    expect(revokeMcpSession).not.toHaveBeenCalled();
  });
});
