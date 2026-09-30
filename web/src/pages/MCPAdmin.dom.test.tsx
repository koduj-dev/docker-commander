/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { MCPAdmin } from "./MCPAdmin";
import { DialogProvider } from "../components/Dialog";

vi.mock("../lib/api", () => ({
  api: {
    mcpAdminTokens: () => Promise.resolve([]),
    mcpAdminOAuthClients: () => Promise.resolve([]),
    mcpAdminSessions: () => Promise.resolve([]),
    mcpAdminRevokeToken: vi.fn(),
    mcpAdminDeleteOAuthClient: vi.fn(),
    mcpAdminRevokeSession: vi.fn(),
  },
}));

let container: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root.render(<MemoryRouter><DialogProvider><MCPAdmin embedded /></DialogProvider></MemoryRouter>);
  });
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

describe("MCPAdmin footer", () => {
  // The note is a flex row (icon + text). Loose text next to a <strong> made each
  // of them a separate flex item, so "Settings → Security" rendered as its own
  // narrow column. The text has to sit in one element beside the icon.
  it("keeps the note text in a single flex item next to the icon", () => {
    const strong = Array.from(container.querySelectorAll("strong")).find((s) => s.textContent === "Settings → Security");
    expect(strong).toBeTruthy();
    const note = strong!.closest("p")!;
    expect(note.className).toContain("flex");
    expect(note.children).toHaveLength(2);
    expect(note.children[0].tagName.toLowerCase()).toBe("svg");
    const textNodes = Array.from(note.childNodes).filter((n) => n.nodeType === Node.TEXT_NODE && n.textContent!.trim() !== "");
    expect(textNodes).toHaveLength(0);
  });
});
